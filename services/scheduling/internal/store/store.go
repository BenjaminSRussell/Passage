// Package store persists scheduling state in Postgres. Every query runs inside
// a tenant-scoped transaction (dbx.WithTenantTx, so row-level security applies)
// and every write commits its outbox events in that same transaction.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
)

const (
	// MaxBars is the Gantt window limit (PRD 4.23.5).
	MaxBars = 5000
	// MaxCapacityDays bounds the open-interval inquiry window.
	MaxCapacityDays = 62

	CodeResourceDuplicate  = "SCHED-0101"
	CodeResourceNotFound   = "SCHED-0102"
	CodeOperationDuplicate = "SCHED-0103"
	CodeRunNotFound        = "SCHED-0104"
	CodeJobNotFound        = "SCHED-0105"
	CodeWindowTooLarge     = "SCHED-0106"
	CodeUnknownResource    = "SCHED-0107"
	CodeConcurrentBooking  = "SCHED-0001"
	CodeIdempotencyReuse   = "idempotency_key_reuse"

	uniqueViolation    = "23505"
	exclusionViolation = "23P01"
	eventSource        = "scheduling"
	scopeMove          = "move"
)

// Idem carries an Idempotency-Key and a fingerprint of the request body. A
// replay with a different fingerprint is refused (422).
type Idem struct{ Key, Fingerprint string }

type Resource struct {
	ID        string         `json:"id"`
	Code      string         `json:"code"`
	Name      string         `json:"name"`
	Finite    bool           `json:"finite"`
	TimeZone  string         `json:"timeZone"`
	Shifts    []domain.Shift `json:"shifts"`
	Version   int            `json:"version"`
	CreatedAt time.Time      `json:"createdAt"`
}

type Operation struct {
	ID          string     `json:"id"`
	OrderID     string     `json:"orderId"`
	OperNumber  int        `json:"operNumber"`
	Minutes     int        `json:"minutes"`
	DueAt       *time.Time `json:"dueAt,omitempty"`
	ReadyAt     *time.Time `json:"readyAt,omitempty"`
	Priority    int        `json:"priority"`
	ResourceIDs []string   `json:"resourceIds"`
	Status      string     `json:"status"`
	CreatedAt   time.Time  `json:"createdAt"`
}

type Run struct {
	ID                   string    `json:"id"`
	Scope                string    `json:"scope"`
	Mode                 string    `json:"mode"`
	Direction            string    `json:"direction"`
	Finite               bool      `json:"finite"`
	Algorithm            string    `json:"algorithm"`
	Status               string    `json:"status"`
	OperationsCount      int       `json:"operationsCount"`
	MakespanMinutes      int       `json:"makespanMinutes"`
	TotalLatenessMinutes int       `json:"totalLatenessMinutes"`
	SolverStatus         *string   `json:"solverStatus"`
	Messages             []string  `json:"messages"`
	CreatedAt            time.Time `json:"createdAt"`
}

// Bar is one segment of a scheduled job, as drawn on the Gantt.
type Bar struct {
	JobID       string    `json:"jobId"`
	OrderID     string    `json:"orderId"`
	OperNumber  int       `json:"operNumber"`
	ResourceID  string    `json:"resourceId"`
	Segment     int       `json:"segment"`
	Start       time.Time `json:"start"`
	End         time.Time `json:"end"`
	LateMinutes int       `json:"lateMinutes"`
	Firmed      bool      `json:"firmed"`
}

type RunRequest struct {
	Scope        string
	Mode         string
	Finite       bool
	OperationIDs []string
	Start        time.Time
	HorizonDays  int
}

type MoveRequest struct {
	JobID       string
	ResourceID  string
	Start       time.Time
	HorizonDays int
	DryRun      bool
}

// Change is one operation whose placement moves.
type Change struct {
	OperationID string    `json:"operationId"`
	OrderID     string    `json:"orderId"`
	ResourceID  string    `json:"resourceId"`
	OldStart    time.Time `json:"oldStart"`
	OldEnd      time.Time `json:"oldEnd"`
	NewStart    time.Time `json:"newStart"`
	NewEnd      time.Time `json:"newEnd"`
	LateMinutes int       `json:"lateMinutes"`
}

// MoveOutcome is the preview or the committed result of a move. Conflict is
// set when the move is infeasible; nothing was written in that case.
type MoveOutcome struct {
	Changes    []Change         `json:"changes"`
	OrderID    string           `json:"orderId"`
	OrderStart time.Time        `json:"orderStart"`
	OrderEnd   time.Time        `json:"orderEnd"`
	Run        *Run             `json:"run,omitempty"`
	Conflict   *domain.Conflict `json:"-"`
}

// Firm is the result of firming or unfirming an operation's placement.
type Firm struct {
	JobID      string    `json:"jobId"`
	OrderID    string    `json:"orderId"`
	ResourceID string    `json:"resourceId"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	Firmed     bool      `json:"firmed"`
}

// LoadFilter selects resources and the week window for the load view.
type LoadFilter struct {
	ResourceIDs  []string
	From         time.Time
	Weeks        int
	ThresholdPct int
	Consecutive  int
}

// OperationFilter narrows the operation list.
type OperationFilter struct {
	OrderID string
	Status  string
	Limit   int
}

// Store is what the API layer needs.
type Store interface {
	CreateResource(ctx context.Context, in Resource, idem Idem) (Resource, bool, error)
	ListResources(ctx context.Context) ([]Resource, error)
	GetResource(ctx context.Context, id string) (Resource, error)
	ResourceCapacity(ctx context.Context, id string, from, to time.Time) ([]domain.Interval, error)
	CreateOperation(ctx context.Context, in Operation, idem Idem) (Operation, bool, error)
	ListOperations(ctx context.Context, f OperationFilter) ([]Operation, error)
	RunSchedule(ctx context.Context, req RunRequest, idem Idem) (Run, bool, error)
	GetRun(ctx context.Context, id string) (Run, error)
	ListRuns(ctx context.Context, limit int) ([]Run, error)
	Gantt(ctx context.Context, from, to time.Time, resourceIDs []string) ([]Bar, error)
	MoveJob(ctx context.Context, req MoveRequest, idem Idem) (MoveOutcome, bool, error)
	FirmJob(ctx context.Context, jobID string, firmed bool, idem Idem) (Firm, bool, error)
	CapacityLoad(ctx context.Context, f LoadFilter) ([]domain.WeekLoad, error)
	Ping(ctx context.Context) error
}

// Postgres implements Store on a pgx pool.
type Postgres struct{ pool *pgxpool.Pool }

// New returns a Postgres store.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks database reachability for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func tenantOf(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.TenantID
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func reuseProblem() error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
}

// mapWriteErr turns constraint violations into stable problem codes.
func mapWriteErr(err error, dupCode, dupDetail string) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case uniqueViolation:
			return httpx.NewProblem(http.StatusConflict, dupCode, dupDetail)
		case exclusionViolation:
			return httpx.NewProblem(http.StatusConflict, CodeConcurrentBooking, "a concurrent change booked the same resource; reload and try again")
		}
	}
	return err
}

func newEvent(ctx context.Context, eventType, subject string, data any) (events.Event, error) {
	return events.New(eventType, eventSource, subject, tenantOf(ctx), data)
}

func enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID, eventType string, data any) error {
	evt, err := newEvent(ctx, eventType, aggregateID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, aggregateType, aggregateID, evt)
}

func rfc(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// RunSchedule runs the heuristic over the selection (or every operation for
// scope site), replacing the non-firmed jobs of those operations. Firmed jobs
// and the jobs of unselected operations in the same orders stay put and hold
// back their successors. The run, its jobs, statuses and events commit together.
func (s *Postgres) RunSchedule(ctx context.Context, req RunRequest, idem Idem) (Run, bool, error) {
	var out Run
	created := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idem.Key != "" {
			var stored string
			existing, err := scanRun(tx.QueryRow(ctx, `select idempotency_fingerprint, `+runCols+` from schedule_run where idempotency_key = $1`, idem.Key), &stored)
			if err == nil {
				if stored != idem.Fingerprint {
					return reuseProblem()
				}
				out = existing
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		resources, finite, err := loadResources(ctx, tx)
		if err != nil {
			return err
		}
		selection := req.OperationIDs
		if selection == nil {
			selection = []string{}
		}
		ops, err := loadOperations(ctx, tx, "$1::boolean or id = any($2::uuid[])", true, req.Scope == "site", selection)
		if err != nil {
			return err
		}
		opIDs, orderIDs := idsOf(ops)
		prior, err := orderDates(ctx, tx, orderIDs)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from scheduled_job where job_id = any($1::uuid[]) and not firmed`, opIDs); err != nil {
			return err
		}
		kept, err := placementsOfOrders(ctx, tx, orderIDs)
		if err != nil {
			return err
		}
		fixed := make([]domain.Placement, 0, len(kept))
		todo := make([]domain.Operation, 0, len(ops))
		for _, pl := range kept {
			fixed = append(fixed, pl)
		}
		for _, op := range ops {
			if _, firmed := kept[op.ID]; !firmed {
				todo = append(todo, op)
			}
		}
		booked, err := loadBooked(ctx, tx, req.Start)
		if err != nil {
			return err
		}
		res := domain.Schedule(domain.Problem{Resources: resources, Operations: todo, Booked: booked, Fixed: fixed,
			Start: req.Start, HorizonDays: req.HorizonDays, Infinite: !req.Finite})
		run := Run{ID: ids.New(), Scope: req.Scope, Mode: req.Mode, Direction: "forward", Finite: req.Finite, Algorithm: "heuristic",
			Status: "completed", OperationsCount: len(todo), MakespanMinutes: domain.Makespan(res.Placements),
			TotalLatenessMinutes: domain.TotalLateness(res.Placements), Messages: unplacedMessages(res.Unplaced)}
		if err := insertRun(ctx, tx, &run, idem); err != nil {
			return err
		}
		rowFinite := finite
		if !req.Finite {
			rowFinite = map[string]bool{}
		}
		if err := writePlacements(ctx, tx, run.ID, res.Placements, rowFinite); err != nil {
			return err
		}
		placed := make([]string, 0, len(res.Placements))
		for _, pl := range res.Placements {
			placed = append(placed, pl.OperationID)
		}
		unplaced := make([]string, 0, len(res.Unplaced))
		for _, u := range res.Unplaced {
			unplaced = append(unplaced, u.OperationID)
		}
		if err := setStatus(ctx, tx, placed, "scheduled"); err != nil {
			return err
		}
		if err := setStatus(ctx, tx, unplaced, "unplaced"); err != nil {
			return err
		}
		if err := enqueuePlacements(ctx, tx, run.ID, res.Placements, false); err != nil {
			return err
		}
		if err := enqueueOrderDates(ctx, tx, orderIDs, prior); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, "run", run.ID, schedevents.RunCompleted, schedevents.RunCompletedData{RunID: run.ID, Scope: run.Scope,
			Operations: run.OperationsCount, MakespanMinutes: run.MakespanMinutes, TotalLatenessMinutes: run.TotalLatenessMinutes}); err != nil {
			return err
		}
		out, created = run, true
		return nil
	})
	if err != nil {
		return Run{}, false, mapWriteErr(err, CodeConcurrentBooking, "a concurrent run or move changed the same jobs; try again")
	}
	return out, created, nil
}

// MoveJob plans or commits a move of one scheduled job (FR-23.8). The order's
// operations are locked so concurrent moves of one order serialize; the
// exclusion constraint stops a concurrent booking on another order. A commit
// writes a move-scoped run so every change is traceable, and replays of the
// same Idempotency-Key return the stored outcome (replayed = true).
func (s *Postgres) MoveJob(ctx context.Context, req MoveRequest, idem Idem) (MoveOutcome, bool, error) {
	var out MoveOutcome
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if !req.DryRun && idem.Key != "" {
			found, err := replay(ctx, tx, idem, &out)
			if err != nil || found {
				replayed = found
				return err
			}
		}
		var orderID string
		err := tx.QueryRow(ctx, `select order_id::text from scheduled_job where job_id = $1 limit 1`, req.JobID).Scan(&orderID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeJobNotFound, "scheduled job not found")
		}
		if err != nil {
			return err
		}
		ops, err := loadOperations(ctx, tx, "order_id = $1::uuid", true, orderID)
		if err != nil {
			return err
		}
		resources, finite, err := loadResources(ctx, tx)
		if err != nil {
			return err
		}
		placements, err := placementsOfOrders(ctx, tx, []string{orderID})
		if err != nil {
			return err
		}
		firmed, err := firmedJobs(ctx, tx, orderID)
		if err != nil {
			return err
		}
		horizonStart := req.Start
		for _, pl := range placements {
			if pl.Start.Before(horizonStart) {
				horizonStart = pl.Start
			}
		}
		horizonStart = horizonStart.Add(-24 * time.Hour)
		booked, err := loadBooked(ctx, tx, horizonStart)
		if err != nil {
			return err
		}
		rid := req.ResourceID
		if rid == "" {
			rid = placements[req.JobID].ResourceID
		}
		plan := domain.PlanMove(domain.MoveInput{Resources: resources, Start: horizonStart, HorizonDays: req.HorizonDays,
			Booked: booked, Order: ops, Current: placements, Moved: req.JobID, ResourceID: rid, At: req.Start, Firmed: firmed})
		if plan.Conflict != nil {
			out.Conflict = plan.Conflict
			return nil
		}
		out.OrderID = orderID
		out.Changes = make([]Change, 0, len(plan.Changes))
		merged := map[string]domain.Placement{}
		for id, pl := range placements {
			merged[id] = pl
		}
		for _, c := range plan.Changes {
			merged[c.OperationID] = c
			old := placements[c.OperationID]
			out.Changes = append(out.Changes, Change{OperationID: c.OperationID, OrderID: c.OrderID, ResourceID: c.ResourceID,
				OldStart: old.Start, OldEnd: old.End, NewStart: c.Start, NewEnd: c.End, LateMinutes: c.LateMinutes})
		}
		for _, pl := range merged {
			if out.OrderStart.IsZero() || pl.Start.Before(out.OrderStart) {
				out.OrderStart = pl.Start
			}
			if pl.End.After(out.OrderEnd) {
				out.OrderEnd = pl.End
			}
		}
		if req.DryRun {
			return nil
		}
		prior, err := orderDates(ctx, tx, []string{orderID})
		if err != nil {
			return err
		}
		run := Run{ID: ids.New(), Scope: scopeMove, Mode: "jobs", Direction: "forward", Finite: true, Algorithm: "heuristic",
			Status: "completed", OperationsCount: len(plan.Changes), MakespanMinutes: domain.Makespan(plan.Changes),
			TotalLatenessMinutes: domain.TotalLateness(plan.Changes), Messages: []string{}}
		if err := insertRun(ctx, tx, &run, Idem{}); err != nil {
			return err
		}
		changed := make([]string, 0, len(plan.Changes))
		for _, c := range plan.Changes {
			changed = append(changed, c.OperationID)
		}
		if _, err := tx.Exec(ctx, `delete from scheduled_job where job_id = any($1::uuid[])`, changed); err != nil {
			return err
		}
		if err := writePlacements(ctx, tx, run.ID, plan.Changes, finite); err != nil {
			return err
		}
		if err := enqueuePlacements(ctx, tx, run.ID, plan.Changes, false); err != nil {
			return err
		}
		if err := enqueueOrderDates(ctx, tx, []string{orderID}, prior); err != nil {
			return err
		}
		out.Run = &run
		if idem.Key != "" {
			return remember(ctx, tx, idem, http.StatusOK, out)
		}
		return nil
	})
	if err != nil {
		return MoveOutcome{}, false, mapWriteErr(err, CodeConcurrentBooking, "a concurrent change moved the same jobs; reload and try again")
	}
	return out, replayed, nil
}

// FirmJob sets the firmed flag on every segment of one operation. A firmed
// placement is frozen against later runs and moves (FR-23.4). The change is
// announced as job.scheduled in the same transaction.
func (s *Postgres) FirmJob(ctx context.Context, jobID string, firmed bool, idem Idem) (Firm, bool, error) {
	var out Firm
	replayed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idem.Key != "" {
			found, err := replay(ctx, tx, idem, &out)
			if err != nil || found {
				replayed = found
				return err
			}
		}
		var runID string
		err := tx.QueryRow(ctx, `select order_id::text, run_id::text from scheduled_job where job_id = $1::uuid limit 1 for update`, jobID).
			Scan(&out.OrderID, &runID)
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NewProblem(http.StatusNotFound, CodeJobNotFound, "scheduled job not found")
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update scheduled_job set firmed = $2 where job_id = $1::uuid`, jobID, firmed); err != nil {
			return err
		}
		pls, err := placementsOfOrders(ctx, tx, []string{out.OrderID})
		if err != nil {
			return err
		}
		pl := pls[jobID]
		out.JobID, out.ResourceID, out.Start, out.End, out.Firmed = jobID, pl.ResourceID, pl.Start, pl.End, firmed
		if err := enqueuePlacements(ctx, tx, runID, []domain.Placement{pl}, firmed); err != nil {
			return err
		}
		if idem.Key != "" {
			return remember(ctx, tx, idem, http.StatusOK, out)
		}
		return nil
	})
	if err != nil {
		return Firm{}, false, mapWriteErr(err, CodeConcurrentBooking, "a concurrent request used the same Idempotency-Key; try again")
	}
	return out, replayed, nil
}

// CapacityLoad returns weekly load for the selected resources (FR-23.9).
// Booked minutes count every job, including infinite-run jobs, so overload
// shows as load above 100%.
func (s *Postgres) CapacityLoad(ctx context.Context, f LoadFilter) ([]domain.WeekLoad, error) {
	out := []domain.WeekLoad{}
	rids := f.ResourceIDs
	if rids == nil {
		rids = []string{}
	}
	from := domain.WeekStart(f.From)
	to := from.AddDate(0, 0, 7*f.Weeks)
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		resources, _, err := loadResources(ctx, tx)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select resource_id::text, job_id::text, start_at, end_at from scheduled_job
			where end_at > $1 and start_at < $2 and ($3::boolean or resource_id = any($4::uuid[]))`, from, to, len(rids) == 0, rids)
		if err != nil {
			return err
		}
		booked, err := scanBusy(rows)
		if err != nil {
			return err
		}
		byResource := map[string][]domain.Busy{}
		for _, b := range booked {
			byResource[b.ResourceID] = append(byResource[b.ResourceID], b)
		}
		wanted := map[string]bool{}
		for _, id := range rids {
			wanted[id] = true
		}
		for _, r := range resources {
			if len(wanted) > 0 && !wanted[r.ID] {
				continue
			}
			out = append(out, domain.WeeklyLoad(r, domain.LoadIntervals(r, from, f.Weeks), byResource[r.ID], from, f.Weeks, f.ThresholdPct, f.Consecutive)...)
		}
		return nil
	})
	return out, err
}

// Gantt returns the segments overlapping [from, to) on the given resources
// (all resources when none are given). A window over MaxBars is refused.
func (s *Postgres) Gantt(ctx context.Context, from, to time.Time, resourceIDs []string) ([]Bar, error) {
	if resourceIDs == nil {
		resourceIDs = []string{}
	}
	bars := []Bar{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select job_id::text, order_id::text, oper_number, resource_id::text, segment_no, start_at, end_at, late_minutes, firmed
			from scheduled_job where end_at > $1 and start_at < $2 and ($3::boolean or resource_id = any($4::uuid[]))
			order by start_at, resource_id, segment_no limit $5`,
			from, to, len(resourceIDs) == 0, resourceIDs, MaxBars+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var b Bar
			if err := rows.Scan(&b.JobID, &b.OrderID, &b.OperNumber, &b.ResourceID, &b.Segment, &b.Start, &b.End, &b.LateMinutes, &b.Firmed); err != nil {
				return err
			}
			bars = append(bars, b)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	if len(bars) > MaxBars {
		return nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeWindowTooLarge, "the window holds more than 5000 bars; narrow the window or the resources")
	}
	return bars, nil
}

// writePlacements inserts one scheduled_job row per segment, pipelined in a
// single batch. finite maps resource id to whether the row reserves capacity.
func writePlacements(ctx context.Context, tx pgx.Tx, runID string, ps []domain.Placement, finite map[string]bool) error {
	tenantID := tenantOf(ctx)
	batch := &pgx.Batch{}
	for _, pl := range ps {
		for i, seg := range pl.Segments {
			minutes := int(seg.End.Sub(seg.Start) / time.Minute)
			batch.Queue(`insert into scheduled_job (tenant_id, id, job_id, order_id, oper_number, resource_id, segment_no,
				start_at, end_at, minutes, run_id, finite, late_minutes) values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
				tenantID, ids.New(), pl.OperationID, pl.OrderID, pl.OperNumber, pl.ResourceID, i+1, seg.Start, seg.End, minutes,
				runID, finite[pl.ResourceID], pl.LateMinutes)
		}
	}
	return tx.SendBatch(ctx, batch).Close()
}

func setStatus(ctx context.Context, tx pgx.Tx, opIDs []string, status string) error {
	if len(opIDs) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `update scheduling_operation set status = $2 where id = any($1::uuid[])`, opIDs, status)
	return err
}

// enqueuePlacements writes one job.scheduled event per placed operation.
func enqueuePlacements(ctx context.Context, tx pgx.Tx, runID string, ps []domain.Placement, firmed bool) error {
	batch := &pgx.Batch{}
	for _, pl := range ps {
		evt, err := newEvent(ctx, schedevents.JobScheduled, pl.OperationID, schedevents.JobScheduledData{JobID: pl.OperationID,
			OrderID: pl.OrderID, OperNumber: pl.OperNumber, ResourceID: pl.ResourceID, Start: rfc(pl.Start), End: rfc(pl.End),
			RunID: runID, Firmed: firmed})
		if err != nil {
			return err
		}
		payload, err := json.Marshal(evt)
		if err != nil {
			return err
		}
		batch.Queue(`insert into outbox(id, tenant_id, aggregate_type, aggregate_id, type, payload) values($1,$2,$3,$4,$5,$6)`,
			evt.ID, evt.TenantID, "job", pl.OperationID, evt.Type, payload)
	}
	return tx.SendBatch(ctx, batch).Close()
}

// enqueueOrderDates emits order dates-changed for every order whose span
// differs from prior (or that had no prior schedule).
func enqueueOrderDates(ctx context.Context, tx pgx.Tx, orderIDs []string, prior map[string]OrderDates) error {
	after, err := orderDates(ctx, tx, orderIDs)
	if err != nil {
		return err
	}
	for _, id := range orderIDs {
		a, ok := after[id]
		if !ok {
			continue
		}
		if b, had := prior[id]; had && b.Start.Equal(a.Start) && b.End.Equal(a.End) && b.Late == a.Late {
			continue
		}
		if err := enqueue(ctx, tx, "order", id, schedevents.OrderDatesChanged, schedevents.OrderDatesChangedData{OrderID: id,
			Start: rfc(a.Start), End: rfc(a.End), LateMinutes: a.Late}); err != nil {
			return err
		}
	}
	return nil
}

// replay looks up a stored outcome for idem. It returns found=true and fills
// out when the same request was already answered.
func replay(ctx context.Context, tx pgx.Tx, idem Idem, out any) (bool, error) {
	var hash string
	var body []byte
	err := tx.QueryRow(ctx, `select request_hash, response_body from idempotency_keys where key = $1`, idem.Key).Scan(&hash, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if hash != idem.Fingerprint {
		return false, reuseProblem()
	}
	return true, json.Unmarshal(body, out)
}

// remember stores the outcome of a command under its Idempotency-Key in the
// command's transaction, so the record exists only if the change committed.
func remember(ctx context.Context, tx pgx.Tx, idem Idem, status int, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into idempotency_keys (tenant_id, key, request_hash, response_status, response_body) values ($1, $2, $3, $4, $5)`,
		tenantOf(ctx), idem.Key, idem.Fingerprint, status, raw)
	return err
}

func idsOf(ops []domain.Operation) (opIDs, orderIDs []string) {
	seen := map[string]bool{}
	opIDs = make([]string, 0, len(ops))
	orderIDs = []string{}
	for _, op := range ops {
		opIDs = append(opIDs, op.ID)
		if !seen[op.OrderID] {
			seen[op.OrderID] = true
			orderIDs = append(orderIDs, op.OrderID)
		}
	}
	return opIDs, orderIDs
}

func unplacedMessages(us []domain.Unplaced) []string {
	out := make([]string, 0, len(us))
	for _, u := range us {
		out = append(out, u.OperationID+": "+u.Reason)
	}
	return out
}
