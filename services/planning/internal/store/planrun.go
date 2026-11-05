package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Statuses and phases of an asynchronous plan run (#479). A run is running
// until it succeeded (its version is stored), failed (problem says why) or was
// cancelled before its version was committed.
const (
	RunRunning   = "running"
	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"

	PhasePlanning   = "planning"
	PhasePersisting = "persisting"
	PhaseDone       = "done"

	CodeRunNotFound = "PLAN-RUN-NOT-FOUND"
	// CodeRunFinished is 409 for cancelling a run that is no longer running.
	CodeRunFinished = "PLAN-RUN-FINISHED"
	// CodeRunAbandoned is the problem of a run whose process stopped renewing
	// its heartbeat (it died or lost the database) before the run finished.
	CodeRunAbandoned = "PLAN-RUN-ABANDONED"

	// RunStaleAfter is how long a running run may go without a heartbeat
	// before it counts as abandoned; the executing process renews it every
	// few seconds.
	RunStaleAfter = 2 * time.Minute

	runOneRunningIndex   = "plan_run_one_running"
	runIdempotencyUnique = "plan_run_idempotency_key"
)

// PlanRun is an asynchronous plan run as GET /plan-runs/{id} shows it.
type PlanRun struct {
	ID       string `json:"id"`
	PlanCode string `json:"planCode"`
	Status   string `json:"status"`
	// Phase is planning (the engine), persisting (writing the version) or done.
	Phase string `json:"phase"`
	Items int    `json:"items"`
	// RowsWritten of RowsTotal are the version's rows (planned orders,
	// pegging, messages, net requirements) sent to the database so far; they
	// commit together, so a cancelled or failed run stores none of them.
	RowsWritten     int64      `json:"rowsWritten"`
	RowsTotal       int64      `json:"rowsTotal"`
	CancelRequested bool       `json:"cancelRequested"`
	RequestedBy     string     `json:"requestedBy"`
	CreatedAt       time.Time  `json:"createdAt"`
	HeartbeatAt     time.Time  `json:"heartbeatAt"`
	FinishedAt      *time.Time `json:"finishedAt"`
	// Version is the stored version of a succeeded run (absent once purged);
	// Replayed says the run's Idempotency-Key had already stored it.
	VersionNumber *int           `json:"versionNumber"`
	Version       *Version       `json:"version,omitempty"`
	Replayed      bool           `json:"replayed"`
	Problem       *httpx.Problem `json:"problem,omitempty"`

	inputHash string
	versionID *string
}

// PlanRunStart opens an asynchronous run.
type PlanRunStart struct {
	PlanCode string
	// LegalEntityID is the request's legalEntityId: the owner of a new plan,
	// an assertion for an existing one (as on RunPlan).
	LegalEntityID  string
	Items          int
	IdempotencyKey string
	InputHash      string
	RequestedBy    string
}

// PlanRunProgress is what a heartbeat records.
type PlanRunProgress struct {
	Phase       string
	RowsWritten int64
	RowsTotal   int64
}

// PlanRunFinish closes a run: Status succeeded with VersionID, or failed or
// cancelled with Problem.
type PlanRunFinish struct {
	Status      string
	VersionID   string
	Replayed    bool
	Problem     *httpx.Problem
	RowsWritten int64
}

const planRunSelect = `select r.id::text, r.plan_code, r.status, r.phase, r.items, r.rows_written, r.rows_total, r.cancel_requested,
	r.requested_by, r.created_at, r.heartbeat_at, r.finished_at, r.version_id::text, r.replayed, r.problem, r.input_hash,
	r.status = 'running' and r.heartbeat_at < now() - make_interval(secs => $1)
	from plan_run r`

func scanPlanRun(row pgx.Row) (PlanRun, error) {
	var (
		run     PlanRun
		problem []byte
		stale   bool
	)
	err := row.Scan(&run.ID, &run.PlanCode, &run.Status, &run.Phase, &run.Items, &run.RowsWritten, &run.RowsTotal, &run.CancelRequested,
		&run.RequestedBy, &run.CreatedAt, &run.HeartbeatAt, &run.FinishedAt, &run.versionID, &run.Replayed, &problem, &run.inputHash, &stale)
	if err != nil {
		return PlanRun{}, err
	}
	run.CreatedAt, run.HeartbeatAt = run.CreatedAt.UTC(), run.HeartbeatAt.UTC()
	if run.FinishedAt != nil {
		f := run.FinishedAt.UTC()
		run.FinishedAt = &f
	}
	if problem != nil {
		run.Problem = &httpx.Problem{}
		if err := json.Unmarshal(problem, run.Problem); err != nil {
			return PlanRun{}, err
		}
	}
	if stale {
		run.Status, run.Problem = RunFailed, abandoned()
	}
	return run, nil
}

func abandoned() *httpx.Problem {
	return httpx.NewProblem(http.StatusInternalServerError, CodeRunAbandoned, "the process executing the run stopped before it finished; no version was stored, start the run again")
}

func staleSecs() float64 { return RunStaleAfter.Seconds() }

func runNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeRunNotFound, "plan run not found")
}

// withVersion reads the version a succeeded run stored.
func withVersion(ctx context.Context, tx pgx.Tx, tenantID string, run PlanRun) (PlanRun, error) {
	if run.versionID == nil {
		return run, nil
	}
	v, err := scanVersion(tx.QueryRow(ctx, versionSelect+` where v.tenant_id = $1 and v.id = $2`, tenantID, *run.versionID))
	switch {
	case errors.Is(err, pgx.ErrNoRows): // purged since (FR-22.1 retention)
		return run, nil
	case err != nil:
		return run, err
	}
	run.Version, run.VersionNumber = &v, &v.Number
	return run, nil
}

// replayRun returns the run stored under key: found with the same input, or a
// 422 for the key reused with a different request.
func replayRun(ctx context.Context, tx pgx.Tx, tenantID, key, inputHash string) (PlanRun, bool, error) {
	run, err := scanPlanRun(tx.QueryRow(ctx, planRunSelect+` where r.tenant_id = $2 and r.idempotency_key = $3`, staleSecs(), tenantID, key))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return PlanRun{}, false, nil
	case err != nil:
		return PlanRun{}, false, err
	case run.inputHash != inputHash:
		return PlanRun{}, false, idempotencyReuse()
	}
	run, err = withVersion(ctx, tx, tenantID, run)
	return run, err == nil, err
}

// ReplayPlanRun returns the run an earlier request with the Idempotency-Key
// started: found false when there is none, 422 when the key was used with a
// different request.
func (s *Postgres) ReplayPlanRun(ctx context.Context, key, inputHash string) (PlanRun, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var (
		run   PlanRun
		found bool
	)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		run, found, err = replayRun(ctx, tx, p.TenantID, key, inputHash)
		return err
	})
	return run, found, err
}

// BeginPlanRun records a new running run of the plan, or returns the run the
// Idempotency-Key already started (replayed true). A plan that has a live run,
// or whose row a synchronous run holds, refuses with 409 PLAN-0001; a running
// row of the plan whose heartbeat stopped is closed as abandoned first.
func (s *Postgres) BeginPlanRun(ctx context.Context, req PlanRunStart) (PlanRun, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var (
		run      PlanRun
		replayed bool
	)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		if run, replayed, err = replayRun(ctx, tx, p.TenantID, req.IdempotencyKey, req.InputHash); err != nil || replayed {
			return err
		}
		entity, err := planEntity(ctx, tx, p.TenantID, req.PlanCode, req.LegalEntityID)
		if err != nil {
			return err
		}
		// A synchronous run holds the plan row while it writes its version.
		var one int
		err = tx.QueryRow(ctx, `select 1 from plan where tenant_id = $1 and code = $2 for update nowait`, p.TenantID, req.PlanCode).Scan(&one)
		var pgErr *pgconn.PgError
		switch {
		case errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable:
			return alreadyRunning(req.PlanCode)
		case err != nil && !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		gone, err := json.Marshal(abandoned())
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update plan_run set status = 'failed', phase = 'done', finished_at = now(), problem = $4
			where tenant_id = $1 and plan_code = $2 and status = 'running' and heartbeat_at < now() - make_interval(secs => $3)`,
			p.TenantID, req.PlanCode, staleSecs(), gone); err != nil {
			return err
		}
		id := ids.New()
		if _, err := tx.Exec(ctx, `insert into plan_run (tenant_id, id, plan_code, legal_entity_id, status, items, idempotency_key, input_hash, requested_by)
			values ($1, $2, $3, nullif($4, '')::uuid, 'running', $5, $6, $7, $8)`,
			p.TenantID, id, req.PlanCode, entity, req.Items, req.IdempotencyKey, req.InputHash, req.RequestedBy); err != nil {
			return err
		}
		run, err = scanPlanRun(tx.QueryRow(ctx, planRunSelect+` where r.tenant_id = $2 and r.id = $3`, staleSecs(), p.TenantID, id))
		return err
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		switch pgErr.ConstraintName {
		case runOneRunningIndex:
			return PlanRun{}, false, alreadyRunning(req.PlanCode)
		case runIdempotencyUnique: // the same key started a run concurrently: answer with it
			return s.replayAfterRace(ctx, req)
		}
	}
	return run, replayed, err
}

func (s *Postgres) replayAfterRace(ctx context.Context, req PlanRunStart) (PlanRun, bool, error) {
	run, found, err := s.ReplayPlanRun(ctx, req.IdempotencyKey, req.InputHash)
	if err == nil && !found {
		err = alreadyRunning(req.PlanCode)
	}
	return run, found, err
}

func alreadyRunning(code string) error {
	return httpx.NewProblem(http.StatusConflict, CodeAlreadyRunning, "a run of plan "+code+" is already in progress")
}

// refuseActiveRun is 409 PLAN-0001 when the plan has a live asynchronous run
// other than self (empty for a synchronous run).
func refuseActiveRun(ctx context.Context, tx pgx.Tx, tenantID, planCode, self string) error {
	var active bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from plan_run where tenant_id = $1 and plan_code = $2 and status = 'running'
		and id is distinct from nullif($3, '')::uuid and heartbeat_at >= now() - make_interval(secs => $4))`,
		tenantID, planCode, self, staleSecs()).Scan(&active); err != nil {
		return err
	}
	if active {
		return alreadyRunning(planCode)
	}
	return nil
}

// GetPlanRun returns a run with the version it stored. A running run whose
// heartbeat stopped reads as failed with PLAN-RUN-ABANDONED.
func (s *Postgres) GetPlanRun(ctx context.Context, id string) (PlanRun, error) {
	if !ids.Valid(id) {
		return PlanRun{}, runNotFound()
	}
	p, _ := tenant.FromContext(ctx)
	var run PlanRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		if run, err = scanPlanRun(tx.QueryRow(ctx, planRunSelect+` where r.tenant_id = $2 and r.id = $3`, staleSecs(), p.TenantID, id)); err != nil {
			return err
		}
		run, err = withVersion(ctx, tx, p.TenantID, run)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PlanRun{}, runNotFound()
	}
	return run, err
}

// HeartbeatPlanRun renews a running run's heartbeat and records its progress.
// stop is true when the run should end: a cancel was requested, or the row is
// no longer running (it was closed as abandoned).
func (s *Postgres) HeartbeatPlanRun(ctx context.Context, id string, pr PlanRunProgress) (bool, error) {
	p, _ := tenant.FromContext(ctx)
	cancel := true
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `update plan_run set heartbeat_at = now(), phase = $3, rows_written = $4, rows_total = $5
			where tenant_id = $1 and id = $2 and status = 'running' returning cancel_requested`,
			p.TenantID, id, pr.Phase, pr.RowsWritten, pr.RowsTotal).Scan(&cancel)
		if errors.Is(err, pgx.ErrNoRows) {
			cancel = true
			return nil
		}
		return err
	})
	return cancel, err
}

// FinishPlanRun closes a running run with its outcome and returns it. A run
// that is no longer running (closed as abandoned meanwhile) keeps its status.
func (s *Postgres) FinishPlanRun(ctx context.Context, id string, f PlanRunFinish) (PlanRun, error) {
	p, _ := tenant.FromContext(ctx)
	var problem []byte
	if f.Problem != nil {
		var err error
		if problem, err = json.Marshal(f.Problem); err != nil {
			return PlanRun{}, err
		}
	}
	var run PlanRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `update plan_run set status = $3, phase = 'done', finished_at = now(), heartbeat_at = now(),
			version_id = nullif($4, '')::uuid, replayed = $5, problem = $6, rows_written = greatest(rows_written, $7)
			where tenant_id = $1 and id = $2 and status = 'running'`,
			p.TenantID, id, f.Status, f.VersionID, f.Replayed, problem, f.RowsWritten); err != nil {
			return err
		}
		var err error
		if run, err = scanPlanRun(tx.QueryRow(ctx, planRunSelect+` where r.tenant_id = $2 and r.id = $3`, staleSecs(), p.TenantID, id)); err != nil {
			return err
		}
		run, err = withVersion(ctx, tx, p.TenantID, run)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PlanRun{}, runNotFound()
	}
	return run, err
}

// CancelPlanRun asks a running run to stop; the process executing it sees the
// request on its next heartbeat. A run that is not running is 409
// PLAN-RUN-FINISHED.
func (s *Postgres) CancelPlanRun(ctx context.Context, id string) (PlanRun, error) {
	if !ids.Valid(id) {
		return PlanRun{}, runNotFound()
	}
	p, _ := tenant.FromContext(ctx)
	var run PlanRun
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) (err error) {
		if run, err = scanPlanRun(tx.QueryRow(ctx, planRunSelect+` where r.tenant_id = $2 and r.id = $3 for update`, staleSecs(), p.TenantID, id)); err != nil {
			return err
		}
		if run.Status != RunRunning {
			return httpx.NewProblem(http.StatusConflict, CodeRunFinished, "plan run is "+run.Status+"; only a running run can be cancelled")
		}
		if _, err := tx.Exec(ctx, `update plan_run set cancel_requested = true where tenant_id = $1 and id = $2`, p.TenantID, id); err != nil {
			return err
		}
		run.CancelRequested = true
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PlanRun{}, runNotFound()
	}
	return run, err
}
