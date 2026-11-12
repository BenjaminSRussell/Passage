package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Withholding report statuses (ap_withholding_report.status).
const (
	WithholdingPending  = "PENDING"
	WithholdingReported = "REPORTED"
	WithholdingParked   = "PARKED"
	maxWithholdingError = 1000
)

// ErrStaleWithholdingReport is returned when a report was claimed again (its lease ran out) before the outcome was recorded.
var ErrStaleWithholdingReport = errors.New("payables: withholding report claimed again")

// ClaimedWithholdingReport is one leased report: the run, the frozen entries (JSON) and the attempt number.
type ClaimedWithholdingReport struct {
	ID            string
	LegalEntityID string
	RunID         string
	Body          []byte
	Attempts      int
}

// WithholdingOutcome is what one attempt ended in.
type WithholdingOutcome struct {
	Status     string
	RetryAt    time.Time
	HTTPStatus int
	Error      string
}

// QueueWithholdingReport queues the withholding entries of a run (JSON) for the tax service: once per run. A report already PENDING
// or REPORTED is left as it is; a PARKED one is queued again with the entries given (a repeated post-journal after the cause was
// fixed), and the run's park reason is cleared.
func (s *Postgres) QueueWithholdingReport(ctx context.Context, runID, legalEntityID string, body []byte) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into ap_withholding_report (tenant_id, id, legal_entity_id, run_id, body) values ($1, $2, $3, $4, $5)
			on conflict (tenant_id, run_id) do update set body = excluded.body, status = 'PENDING', attempts = 0, next_attempt_at = now(),
			  last_error = null, last_status = null, updated_at = now()
			where ap_withholding_report.status = 'PARKED'`, p.TenantID, ids.New(), legalEntityID, runID, body)
		if err != nil {
			return err
		}
		if tag.RowsAffected() > 0 {
			_, err = tx.Exec(ctx, `update ap_payment_run set withholding_record_error = null where id = $1::uuid`, runID)
		}
		return err
	})
}

// WithholdingSystemContext is the principal the reporter reads and writes one tenant's reports under: no user, every legal entity.
func WithholdingSystemContext(ctx context.Context, tenantID string) context.Context {
	return tenant.With(ctx, tenant.Principal{TenantID: tenantID, Service: "payables", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

// WithholdingDueTenants lists the tenants with a report due now (SECURITY DEFINER payables_withholding_report_due_tenants()).
func (s *Postgres) WithholdingDueTenants(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `select t::text from payables_withholding_report_due_tenants() t`)
	if err != nil {
		return nil, fmt.Errorf("payables: due withholding report tenants: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ClaimWithholdingReports leases up to limit due PENDING reports of the tenant in ctx.
func (s *Postgres) ClaimWithholdingReports(ctx context.Context, limit int, lease time.Duration) ([]ClaimedWithholdingReport, error) {
	var out []ClaimedWithholdingReport
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with claimed as (
				select id as claimed_id from ap_withholding_report where status = 'PENDING' and next_attempt_at <= now()
				order by next_attempt_at, id limit $1 for update skip locked)
			update ap_withholding_report r set attempts = r.attempts + 1, next_attempt_at = now() + make_interval(secs => $2), updated_at = now()
			from claimed where r.id = claimed.claimed_id
			returning r.id::text, r.legal_entity_id::text, r.run_id::text, r.body::text, r.attempts`, limit, lease.Seconds())
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (ClaimedWithholdingReport, error) {
			var c ClaimedWithholdingReport
			var body string
			err := r.Scan(&c.ID, &c.LegalEntityID, &c.RunID, &body, &c.Attempts)
			c.Body = []byte(body)
			return c, err
		})
		return err
	})
	return out, err
}

// FinishWithholdingReport records the outcome of attempt number attempts. REPORTED clears the run's park reason, PARKED sets it to
// the outcome's error. A report claimed again since is left alone (ErrStaleWithholdingReport).
func (s *Postgres) FinishWithholdingReport(ctx context.Context, id string, attempts int, o WithholdingOutcome) error {
	var retry, httpStatus, lastError any
	if o.Status == WithholdingPending {
		retry = o.RetryAt
	}
	if o.HTTPStatus != 0 {
		httpStatus = o.HTTPStatus
	}
	if o.Error != "" {
		lastError = o.Error[:min(len(o.Error), maxWithholdingError)]
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var runID string
		err := tx.QueryRow(ctx, `update ap_withholding_report set status = $3, next_attempt_at = coalesce($4, next_attempt_at),
				last_status = $5, last_error = $6, reported_at = case when $3 = 'REPORTED' then now() else null end, updated_at = now()
			where id = $1 and attempts = $2 and status = 'PENDING' returning run_id::text`, id, attempts, o.Status, retry, httpStatus, lastError).Scan(&runID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrStaleWithholdingReport
		}
		if err != nil {
			return err
		}
		switch o.Status {
		case WithholdingReported:
			_, err = tx.Exec(ctx, `update ap_payment_run set withholding_record_error = null where id = $1::uuid`, runID)
		case WithholdingParked:
			_, err = tx.Exec(ctx, `update ap_payment_run set withholding_record_error = $2 where id = $1::uuid`, runID, lastError)
		}
		return err
	})
}
