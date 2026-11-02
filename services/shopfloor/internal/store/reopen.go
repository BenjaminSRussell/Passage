package store

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// CodeJobNotFinished is raised when a reopen names a job that is not finished.
const CodeJobNotFinished = "SF-0155"

// JobReopening is one supervisor reopening of a finished job, kept for audit.
type JobReopening struct {
	ID                 string          `json:"id"`
	JobID              string          `json:"jobId"`
	Reason             string          `json:"reason"`
	ReopenedBy         string          `json:"reopenedBy"`
	ReopenedAt         time.Time       `json:"reopenedAt"`
	PreviousFinishedAt time.Time       `json:"previousFinishedAt"`
	GoodQty            decimal.Decimal `json:"goodQuantity"`
	ScrapQty           decimal.Decimal `json:"scrapQuantity"`
}

// ReopenJob puts a finished job back to started so it takes feedback again (FR-25.3 note).
// version is the job version the caller read (SF-0143, 412); a job that is not finished is
// SF-0155 (422). The reopening row, the job update and dev.passage.shopfloor.job.reopened.v1
// are written in one transaction; a repeated Idempotency-Key returns the stored job. Quantities
// and check results stay: a later finish still passes the mandatory-check gate (SF-0005).
func (s *Postgres) ReopenJob(ctx context.Context, jobID string, version int, reason, reopenedBy string, idem Idem) (Job, error) {
	p, _ := tenant.FromContext(ctx)
	var out Job
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		job, err := getJob(ctx, tx, jobID, true)
		if err != nil {
			return err
		}
		if job.Version != version {
			return versionMismatch("job")
		}
		if job.Status != JobFinished || job.FinishedAt == nil {
			return httpx.NewProblem(http.StatusUnprocessableEntity, CodeJobNotFinished, "job "+job.OrderRef+" "+job.OperationRef+" is not finished, so there is nothing to reopen")
		}
		reopeningID := ids.New()
		var at time.Time
		if err := tx.QueryRow(ctx, `insert into job_reopening (tenant_id, id, job_id, reason, reopened_by, previous_finished_at, good_qty, scrap_qty)
			values ($1, $2, $3, $4, $5, $6, $7, $8) returning reopened_at`,
			p.TenantID, reopeningID, jobID, reason, reopenedBy, *job.FinishedAt, job.GoodQty.String(), job.ScrapQty.String()).Scan(&at); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update shop_job set status = 'started', finished_at = null, version = version + 1, updated_at = now()
			where tenant_id = $1 and id = $2`, p.TenantID, jobID); err != nil {
			return err
		}
		if err := enqueue(ctx, tx, jobAggregate, jobID, sfevents.JobReopened, p.TenantID, sfevents.JobReopenedData{
			JobID: jobID, ReopeningID: reopeningID, OrderRef: job.OrderRef, OperationRef: job.OperationRef, ResourceID: job.ResourceID,
			GoodQuantity: job.GoodQty, ScrapQuantity: job.ScrapQty, Reason: reason, ReopenedBy: reopenedBy, ReopenedAt: at, PreviousFinishedAt: *job.FinishedAt,
		}); err != nil {
			return err
		}
		if out, err = getJob(ctx, tx, jobID, false); err != nil {
			return err
		}
		return remember(ctx, tx, idem, http.StatusOK, out)
	})
	return out, err
}

// ListJobReopenings returns the reopenings of a job, newest first, or SF-0121 when the job
// does not exist.
func (s *Postgres) ListJobReopenings(ctx context.Context, jobID string, limit int) ([]JobReopening, error) {
	p, _ := tenant.FromContext(ctx)
	if limit <= 0 {
		limit = 100
	}
	out := []JobReopening{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := getJob(ctx, tx, jobID, false); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select id::text, job_id::text, reason, reopened_by, reopened_at, previous_finished_at, good_qty::text, scrap_qty::text
			from job_reopening where tenant_id = $1 and job_id = $2 order by reopened_at desc, id limit $3`, p.TenantID, jobID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r JobReopening
			var good, scrap string
			if err := rows.Scan(&r.ID, &r.JobID, &r.Reason, &r.ReopenedBy, &r.ReopenedAt, &r.PreviousFinishedAt, &good, &scrap); err != nil {
				return err
			}
			if r.GoodQty, err = decimal.NewFromString(good); err != nil {
				return err
			}
			if r.ScrapQty, err = decimal.NewFromString(scrap); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
