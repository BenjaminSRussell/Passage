package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// Job catalog codes raised by the store (see README).
const (
	CodeJobNotFound    = "SF-0121"
	CodeJobFinished    = "SF-0122"
	CodeCheckUnknown   = "SF-0123"
	CodeFeedbackOther  = "SF-0124"
	CodeCheckValue     = "SF-0125"
	CodeMandatoryCheck = "SF-0005"
	// CodeJobProductionClosed is SF-0161: quantity feedback on a job whose operation production
	// finished (w21) and has not reopened.
	CodeJobProductionClosed = "SF-0161"

	JobReleased = "released"
	JobStarted  = "started"
	JobFinished = "finished"

	jobAggregate = "job"
)

// Job is a dispatchable operation on a resource with its reported quantities and
// quality checks (FR-25.3, FR-25.4, FR-25.5). Results is filled by GetJob and by
// feedback; the dispatch list leaves it empty.
type Job struct {
	ID            string               `json:"id"`
	OrderRef      string               `json:"orderRef"`
	OperationRef  string               `json:"operationRef"`
	ResourceID    string               `json:"resourceId"`
	Status        string               `json:"status"`
	PlannedQty    decimal.Decimal      `json:"plannedQuantity"`
	GoodQty       decimal.Decimal      `json:"goodQuantity"`
	ScrapQty      decimal.Decimal      `json:"scrapQuantity"`
	SequenceStart time.Time            `json:"sequenceStart"`
	Checks        []domain.Check       `json:"checks"`
	Results       []domain.CheckResult `json:"results"`
	FinishedAt    *time.Time           `json:"finishedAt"`
	// ProductionFinishedAt is when production (w21) finished the job's operation, null while it
	// is open or after production reopened it; job-starts at or after it are refused (SF-0157).
	ProductionFinishedAt *time.Time `json:"productionFinishedAt"`
	Version              int        `json:"version"`
	UpdatedAt            time.Time  `json:"updatedAt"`
}

// JobFilter narrows the dispatch list. An empty Status hides finished jobs.
type JobFilter struct {
	ResourceID string
	// WorkerID narrows the list to the jobs of the worker's home resource ("my jobs"); a
	// worker without one sees every job. An unknown worker is SF-0101 (404).
	WorkerID string
	Status   string
	Limit    int
}

// FeedbackInput is one operator feedback posting for a job. ID is the client UUID.
type FeedbackInput struct {
	ID             string
	GoodQty        decimal.Decimal
	ScrapQty       decimal.Decimal
	ScrapReasonID  *string
	Finished       bool
	Checks         []domain.CheckInput
	OverrideReason *string
	RecordedBy     string
}

// FeedbackResult is the job after a feedback posting. Duplicate is true when the
// feedback id was stored before for this job; nothing changed and nothing was published.
type FeedbackResult struct {
	Job       Job  `json:"job"`
	Duplicate bool `json:"duplicate"`
}

func jobNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, CodeJobNotFound, "job not found")
}

// UpsertJob writes the plan of a job from the schedule projection (w23). Plan fields
// are last-writer-wins; reported quantities, status and check results are never
// overwritten. created is true when the job was new.
func (s *Postgres) UpsertJob(ctx context.Context, j Job) (out Job, created bool, err error) {
	p, _ := tenant.FromContext(ctx)
	checks, err := json.Marshal(nonNilChecks(j.Checks))
	if err != nil {
		return Job{}, false, err
	}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `insert into shop_job (tenant_id, id, order_ref, operation_ref, resource_id, planned_qty, sequence_start, checks)
			values ($1, $2, $3, $4, $5, $6, $7, $8)
			on conflict (tenant_id, id) do update set order_ref = excluded.order_ref, operation_ref = excluded.operation_ref,
				resource_id = excluded.resource_id, planned_qty = excluded.planned_qty, sequence_start = excluded.sequence_start,
				checks = excluded.checks, version = shop_job.version + 1, updated_at = now()
			returning (xmax = 0)`,
			p.TenantID, j.ID, j.OrderRef, j.OperationRef, j.ResourceID, j.PlannedQty.String(), j.SequenceStart, checks).Scan(&created); err != nil {
			return err
		}
		out, err = getJob(ctx, tx, j.ID, false)
		return err
	})
	return out, created, err
}

// GetJob returns one job with its latest check results.
func (s *Postgres) GetJob(ctx context.Context, id string) (Job, error) {
	var out Job
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = getJob(ctx, tx, id, false)
		return err
	})
	return out, err
}

// ListJobs returns the dispatch list ordered by sequence start (FR-25.3). Finished
// jobs are hidden unless the filter asks for them.
func (s *Postgres) ListJobs(ctx context.Context, f JobFilter) ([]Job, error) {
	where := "true"
	var args []any
	if f.ResourceID != "" {
		args = append(args, f.ResourceID)
		where += fmt.Sprintf(" and resource_id = $%d", len(args)+1)
	}
	if f.Status != "" {
		args = append(args, f.Status)
		where += fmt.Sprintf(" and status = $%d", len(args)+1)
	} else {
		where += " and status <> 'finished'"
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 500
	}
	var out []Job
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if f.WorkerID != "" {
			p, _ := tenant.FromContext(ctx)
			var home *string
			err := tx.QueryRow(ctx, `select default_resource_id::text from worker where tenant_id = $1 and id = $2`, p.TenantID, f.WorkerID).Scan(&home)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NewProblem(http.StatusNotFound, CodeWorkerNotFound, "worker not found")
			}
			if err != nil {
				return err
			}
			if home != nil {
				args = append(args, *home)
				where += fmt.Sprintf(" and resource_id = $%d", len(args)+1)
			}
		}
		var err error
		out, err = loadJobs(ctx, tx, where, args, "", limit)
		return err
	})
	return out, err
}

// RecordFeedback posts good and scrap quantities, the finished flag and check values
// for one job in one transaction (FR-25.4, FR-25.5) and publishes job.reported through
// the outbox. A replay of the feedback id on the same job changes nothing; the id on
// another job is SF-0124. A finish is refused with SF-0005 while a mandatory check has
// no passing result, unless the posting carries a supervisor override; the refused
// posting stores nothing, so the same id can be sent again with an override. A job whose
// operation production finished is SF-0161 until production reopens it; a resend of stored
// feedback is still a duplicate.
func (s *Postgres) RecordFeedback(ctx context.Context, jobID string, in FeedbackInput) (FeedbackResult, error) {
	p, _ := tenant.FromContext(ctx)
	var out FeedbackResult
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		job, err := getJob(ctx, tx, jobID, true)
		if err != nil {
			return err
		}
		var storedJob string
		err = tx.QueryRow(ctx, `select job_id::text from job_feedback where tenant_id = $1 and id = $2`, p.TenantID, in.ID).Scan(&storedJob)
		switch {
		case err == nil && storedJob == jobID:
			out = FeedbackResult{Job: job, Duplicate: true}
			return nil
		case err == nil:
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeFeedbackOther, "feedbackId was already used for another job")
		case !errors.Is(err, pgx.ErrNoRows):
			return err
		}
		if job.Status == JobFinished {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeJobFinished, fmt.Sprintf("job %s %s is finished; a supervisor must reopen it", job.OrderRef, job.OperationRef))
		}
		if job.ProductionFinishedAt != nil {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeJobProductionClosed,
				fmt.Sprintf("job %s %s: production finished its operation at %s; ask the planner to reopen the operation before reporting on it",
					job.OrderRef, job.OperationRef, job.ProductionFinishedAt.UTC().Format(time.RFC3339)))
		}

		posted, results, err := evaluateChecks(job, in.Checks)
		if err != nil {
			return err
		}
		if in.Finished {
			if blocked := domain.FinishBlockers(job.Checks, results, in.OverrideReason != nil); len(blocked) > 0 {
				var de *domain.Error
				errors.As(domain.FinishError(blocked), &de)
				return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
			}
		}
		postedJSON, err := json.Marshal(posted)
		if err != nil {
			return err
		}
		var recordedAt time.Time
		if err := tx.QueryRow(ctx, `insert into job_feedback (tenant_id, id, job_id, good_qty, scrap_qty, scrap_reason_id, finished, checks, override_reason, recorded_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, nullif($10, '')) returning recorded_at`,
			p.TenantID, in.ID, jobID, in.GoodQty.String(), in.ScrapQty.String(), in.ScrapReasonID, in.Finished, postedJSON, in.OverrideReason, in.RecordedBy).Scan(&recordedAt); err != nil {
			return err
		}
		for _, r := range posted {
			if _, err := tx.Exec(ctx, `insert into job_check_result (tenant_id, job_id, check_code, value, pass, feedback_id, recorded_at)
				values ($1, $2, $3, $4, $5, $6, $7)
				on conflict (tenant_id, job_id, check_code) do update set value = excluded.value, pass = excluded.pass,
					feedback_id = excluded.feedback_id, recorded_at = excluded.recorded_at`,
				p.TenantID, jobID, r.Code, r.Value, r.Pass, in.ID, recordedAt); err != nil {
				return err
			}
		}
		status := JobStarted
		if in.Finished {
			status = JobFinished
		}
		if _, err := tx.Exec(ctx, `update shop_job set good_qty = good_qty + $3, scrap_qty = scrap_qty + $4, status = $5,
				finished_at = case when $5 = 'finished' then $6::timestamptz end, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, jobID, in.GoodQty.String(), in.ScrapQty.String(), status, recordedAt); err != nil {
			return err
		}
		if job, err = getJob(ctx, tx, jobID, false); err != nil {
			return err
		}
		failed := []string{}
		for _, r := range job.Results {
			if !r.Pass {
				failed = append(failed, r.Code)
			}
		}
		if err := enqueue(ctx, tx, jobAggregate, jobID, sfevents.JobReported, p.TenantID, sfevents.JobReportedData{
			JobID: jobID, FeedbackID: in.ID, OrderRef: job.OrderRef, OperationRef: job.OperationRef, ResourceID: job.ResourceID,
			QtyGood: in.GoodQty, QtyScrap: in.ScrapQty, ScrapReasonID: in.ScrapReasonID,
			GoodQuantity: job.GoodQty, ScrapQuantity: job.ScrapQty, Finished: in.Finished,
			FailedChecks: failed, OverrideReason: in.OverrideReason, RecordedBy: in.RecordedBy, RecordedAt: recordedAt,
		}); err != nil {
			return err
		}
		out = FeedbackResult{Job: job}
		return nil
	})
	return out, err
}

// evaluateChecks evaluates the posted values against the job's defined checks. It
// returns the posted results and every result the job holds once they are applied.
func evaluateChecks(job Job, inputs []domain.CheckInput) ([]domain.CheckResult, map[string]domain.CheckResult, error) {
	byCode := make(map[string]domain.Check, len(job.Checks))
	for _, c := range job.Checks {
		byCode[c.Code] = c
	}
	all := make(map[string]domain.CheckResult, len(job.Results)+len(inputs))
	for _, r := range job.Results {
		all[r.Code] = r
	}
	posted := make([]domain.CheckResult, 0, len(inputs))
	for i, in := range inputs {
		c, ok := byCode[in.Code]
		if !ok {
			return nil, nil, httpx.NewProblem(http.StatusUnprocessableEntity, CodeCheckUnknown, fmt.Sprintf("check %s is not defined on this job", in.Code))
		}
		res, reason := domain.EvaluateCheck(c, in.Value)
		if reason != "" {
			p := httpx.NewProblem(http.StatusUnprocessableEntity, CodeCheckValue, fmt.Sprintf("value of check %s cannot be evaluated", in.Code))
			p.Errors = map[string]string{fmt.Sprintf("checks[%d].value", i): reason}
			return nil, nil, p
		}
		posted = append(posted, res)
		all[res.Code] = res
	}
	return posted, all, nil
}

// getJob reads one job with its check results; lock takes the row for update.
func getJob(ctx context.Context, tx pgx.Tx, id string, lock bool) (Job, error) {
	mode := ""
	if lock {
		mode = "for update"
	}
	jobs, err := loadJobs(ctx, tx, "id = $2", []any{id}, mode, 1)
	if err != nil {
		return Job{}, err
	}
	if len(jobs) == 0 {
		return Job{}, jobNotFound()
	}
	job := jobs[0]
	job.Results, err = loadResults(ctx, tx, id)
	return job, err
}

func loadResults(ctx context.Context, tx pgx.Tx, jobID string) ([]domain.CheckResult, error) {
	p, _ := tenant.FromContext(ctx)
	rows, err := tx.Query(ctx, `select check_code, value, pass from job_check_result where tenant_id = $1 and job_id = $2 order by check_code`, p.TenantID, jobID)
	if err != nil {
		return nil, err
	}
	out := []domain.CheckResult{}
	for rows.Next() {
		var r domain.CheckResult
		if err := rows.Scan(&r.Code, &r.Value, &r.Pass); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, r)
	}
	rows.Close()
	return out, rows.Err()
}

// loadJobs runs the job query. where may reference $1 (tenant) and the parameters in
// args from $2 on; it is always built from constants in this file. lock is "" or
// "for update".
func loadJobs(ctx context.Context, tx pgx.Tx, where string, args []any, lock string, limit int) ([]Job, error) {
	p, _ := tenant.FromContext(ctx)
	params := append([]any{p.TenantID}, args...)
	q := `select id::text, order_ref, operation_ref, resource_id::text, status, planned_qty::text, good_qty::text, scrap_qty::text,
		sequence_start, checks, finished_at,
		(select o.changed_at from operation_production_status o where o.tenant_id = shop_job.tenant_id and o.order_ref = lower(shop_job.order_ref)
		   and o.operation_ref = shop_job.operation_ref and o.finished),
		version, updated_at from shop_job where tenant_id = $1 and (` + where + `)
		order by sequence_start, id limit ` + fmt.Sprint(limit) + ` ` + lock
	rows, err := tx.Query(ctx, q, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		var j Job
		var checks []byte
		var planned, good, scrap string
		if err := rows.Scan(&j.ID, &j.OrderRef, &j.OperationRef, &j.ResourceID, &j.Status, &planned, &good, &scrap,
			&j.SequenceStart, &checks, &j.FinishedAt, &j.ProductionFinishedAt, &j.Version, &j.UpdatedAt); err != nil {
			return nil, err
		}
		for _, q := range []struct {
			raw string
			to  *decimal.Decimal
		}{{planned, &j.PlannedQty}, {good, &j.GoodQty}, {scrap, &j.ScrapQty}} {
			d, err := decimal.NewFromString(q.raw)
			if err != nil {
				return nil, err
			}
			*q.to = d
		}
		if err := json.Unmarshal(checks, &j.Checks); err != nil {
			return nil, fmt.Errorf("decode checks of job %s: %w", j.ID, err)
		}
		j.Checks = nonNilChecks(j.Checks)
		j.Results = []domain.CheckResult{}
		out = append(out, j)
	}
	return out, rows.Err()
}

func nonNilChecks(c []domain.Check) []domain.Check {
	if c == nil {
		return []domain.Check{}
	}
	return c
}
