package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// Modification billing statuses (modification_billing.status).
const (
	BillingPending = "PENDING"
	BillingBilled  = "BILLED"
	BillingParked  = "PARKED"
	maxBillingErr  = 1000
)

// ErrStaleBilling is returned when a billing request was claimed again (its lease ran out) before the outcome was recorded.
var ErrStaleBilling = errors.New("revrec: modification billing request claimed again")

// ClaimedBilling is one leased billing request: the frozen body and key, and the attempt number.
type ClaimedBilling struct {
	ID             string
	LegalEntityID  string
	ModificationID string
	IdempotencyKey string
	Body           []byte
	Attempts       int
}

// BillingOutcome is what one attempt ended in: BILLED, PENDING with RetryAt, or PARKED.
type BillingOutcome struct {
	Status     string
	RetryAt    time.Time
	HTTPStatus int
	Error      string
}

// BillingSystemContext is the principal the reporter reads and writes one tenant's billing requests under: no user, every legal
// entity of the tenant.
func BillingSystemContext(ctx context.Context, tenantID string) context.Context {
	return tenant.With(ctx, tenant.Principal{TenantID: tenantID, Service: "revrec", Duties: map[string]struct{}{tenant.DutyAllScope: {}}})
}

// BillingDueTenants lists the tenants with a billing request due now, across tenants (SECURITY DEFINER
// revrec_modification_billing_due_tenants()).
func (s *Postgres) BillingDueTenants(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx, `select t::text from revrec_modification_billing_due_tenants() t`)
	if err != nil {
		return nil, fmt.Errorf("revrec: due billing tenants: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

// ClaimBillings leases up to limit due PENDING billing requests of the tenant in ctx: attempts grows by one and the next attempt
// moves past the lease, so another replica skips them and a reporter that dies mid-call gives them back when the lease runs out.
func (s *Postgres) ClaimBillings(ctx context.Context, limit int, lease time.Duration) ([]ClaimedBilling, error) {
	var out []ClaimedBilling
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `with claimed as (
				select id as claimed_id from modification_billing where status = 'PENDING' and next_attempt_at <= now()
				order by next_attempt_at, id limit $1 for update skip locked)
			update modification_billing b set attempts = b.attempts + 1, next_attempt_at = now() + make_interval(secs => $2), updated_at = now()
			from claimed where b.id = claimed.claimed_id
			returning b.id::text, b.legal_entity_id::text, b.modification_id::text, b.idempotency_key, b.body::text, b.attempts`,
			limit, lease.Seconds())
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (ClaimedBilling, error) {
			var c ClaimedBilling
			var body string
			err := r.Scan(&c.ID, &c.LegalEntityID, &c.ModificationID, &c.IdempotencyKey, &body, &c.Attempts)
			c.Body = []byte(body)
			return c, err
		})
		return err
	})
	return out, err
}

// FinishBilling records the outcome of attempt number attempts; a request claimed again since is left alone (ErrStaleBilling).
func (s *Postgres) FinishBilling(ctx context.Context, id string, attempts int, o BillingOutcome) error {
	var retry, httpStatus, lastError any
	if o.Status == BillingPending {
		retry = o.RetryAt
	}
	if o.HTTPStatus != 0 {
		httpStatus = o.HTTPStatus
	}
	if o.Error != "" {
		lastError = o.Error[:min(len(o.Error), maxBillingErr)]
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `update modification_billing set status = $3, next_attempt_at = coalesce($4, next_attempt_at),
				last_status = $5, last_error = $6, billed_at = case when $3 = 'BILLED' then now() else null end, updated_at = now()
			where id = $1 and attempts = $2 and status = 'PENDING'`, id, attempts, o.Status, retry, httpStatus, lastError)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrStaleBilling
		}
		return nil
	})
}

// ModificationBilling is the billing state of one modification, as the contract screens show it.
type ModificationBilling struct {
	ID             string
	ModificationID string
	Status         string
	Attempts       int
	LastError      *string
	BilledAt       *time.Time
}

// RetryBilling puts a PARKED billing request back in the queue (attempts from zero, same body and key); PENDING and BILLED rows
// are returned as they are.
func (s *Postgres) RetryBilling(ctx context.Context, modificationID string) (ModificationBilling, error) {
	var out ModificationBilling
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `update modification_billing set status = case when status = 'PARKED' then 'PENDING' else status end,
				attempts = case when status = 'PARKED' then 0 else attempts end,
				next_attempt_at = case when status = 'PARKED' then now() else next_attempt_at end, updated_at = now()
			where modification_id = $1::uuid
			returning id::text, modification_id::text, status, attempts, last_error, billed_at`, modificationID).
			Scan(&out.ID, &out.ModificationID, &out.Status, &out.Attempts, &out.LastError, &out.BilledAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("modification billing")
		}
		return err
	})
	return out, err
}
