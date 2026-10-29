// Package store persists reconciliation results with their outbox events.
package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	analyticsevents "github.com/BenjaminSRussell/passage/services/analytics/internal/events"
)

const eventSource = "analytics"

// ownTenant repeats the RLS predicate in queries, so a connection that bypasses
// row-level security (a superuser, the migration role) still reads one tenant.
const ownTenant = `tenant_id = current_setting('passage.tenant_id', true)::uuid`

const reconColumns = `legal_entity_id::text, metric_id, recon_date::text, currency, semantic_value, source_value,
	delta, tolerance, status, consecutive_ok_days, trusted`

// Postgres is the analytics store. Row-level security scopes every query to the
// caller's tenant and legal-entity scope.
type Postgres struct {
	pool *pgxpool.Pool
}

// New returns a store over pool.
func New(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// Ping checks the database for readiness.
func (s *Postgres) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// utc normalises a scanned timestamptz to UTC so responses do not depend on
// the server's time zone.
func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func scanRecon(row pgx.Row) (domain.Reconciliation, error) {
	var r domain.Reconciliation
	err := row.Scan(&r.LegalEntityID, &r.MetricID, &r.Date, &r.Currency, &r.SemanticValue, &r.SourceValue,
		&r.Delta, &r.Tolerance, &r.Status, &r.ConsecutiveOKDays, &r.Trusted)
	return r, err
}

func sameValues(a, b domain.Reconciliation) bool {
	return a.Currency == b.Currency && a.SemanticValue.Equal(b.SemanticValue) && a.SourceValue.Equal(b.SourceValue) &&
		a.Tolerance.Equal(b.Tolerance) && a.Status == b.Status
}

// RecordReconciliation stores one daily result. Delta and status are derived
// by domain.FinalizeReconciliation, never taken from the caller. Re-recording
// identical values returns the stored row and changed=false, emits nothing and
// keeps the trust counter. Changed values replace the row, advance the trust
// counter from the previous day and enqueue metric.reconciled in the same
// transaction.
func (s *Postgres) RecordReconciliation(ctx context.Context, in domain.Reconciliation) (domain.Reconciliation, bool, error) {
	in, verr := domain.FinalizeReconciliation(in)
	if verr != nil {
		return domain.Reconciliation{}, false, verr
	}
	p, _ := tenant.FromContext(ctx)
	var out domain.Reconciliation
	changed := false
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		existing, err := scanRecon(tx.QueryRow(ctx, `select `+reconColumns+` from metric_reconciliation
			where tenant_id = $1 and legal_entity_id = $2 and metric_id = $3 and recon_date = $4::date`,
			p.TenantID, in.LegalEntityID, in.MetricID, in.Date))
		if err == nil && sameValues(existing, in) {
			out = existing
			return nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var previous int
		err = tx.QueryRow(ctx, `select consecutive_ok_days from metric_reconciliation
			where tenant_id = $1 and legal_entity_id = $2 and metric_id = $3 and recon_date = $4::date - 1`,
			p.TenantID, in.LegalEntityID, in.MetricID, in.Date).Scan(&previous)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		next := domain.NextTrust(previous, in.Status)
		in.ConsecutiveOKDays, in.Trusted = next, domain.IsTrusted(next)
		out, err = scanRecon(tx.QueryRow(ctx, `insert into metric_reconciliation (tenant_id, legal_entity_id, metric_id, recon_date, currency,
			semantic_value, source_value, delta, tolerance, status, consecutive_ok_days, trusted)
			values ($1, $2, $3, $4::date, $5, $6, $7, $8, $9, $10, $11, $12)
			on conflict (tenant_id, legal_entity_id, metric_id, recon_date) do update set currency = excluded.currency,
				semantic_value = excluded.semantic_value, source_value = excluded.source_value, delta = excluded.delta,
				tolerance = excluded.tolerance, status = excluded.status, consecutive_ok_days = excluded.consecutive_ok_days,
				trusted = excluded.trusted, updated_at = now()
			returning `+reconColumns,
			p.TenantID, in.LegalEntityID, in.MetricID, in.Date, in.Currency, in.SemanticValue.String(), in.SourceValue.String(),
			in.Delta.String(), in.Tolerance.String(), in.Status, in.ConsecutiveOKDays, in.Trusted))
		if err != nil {
			return err
		}
		changed = true
		return enqueue(ctx, tx, p.TenantID, out)
	})
	return out, changed, err
}

func enqueue(ctx context.Context, tx pgx.Tx, tenantID string, r domain.Reconciliation) error {
	subject := r.MetricID + "/" + r.LegalEntityID + "/" + r.Date
	evt, err := events.New(analyticsevents.MetricReconciled, eventSource, subject, tenantID, analyticsevents.MetricReconciledData{
		MetricID: r.MetricID, TenantID: tenantID, LegalEntityID: r.LegalEntityID, Date: r.Date, Currency: r.Currency,
		SemanticValue: r.SemanticValue.String(), SourceValue: r.SourceValue.String(), Delta: r.Delta.String(),
		Tolerance: r.Tolerance.String(), Status: r.Status, Trusted: r.Trusted,
	})
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "metric_reconciliation", subject, evt)
}

// ListReconciliations returns the results for one day, optionally for one
// metric, ordered by metric and legal entity. RLS hides other entities.
func (s *Postgres) ListReconciliations(ctx context.Context, date, metricID string) ([]domain.Reconciliation, error) {
	p, _ := tenant.FromContext(ctx)
	items := []domain.Reconciliation{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+reconColumns+` from metric_reconciliation
			where tenant_id = $1 and recon_date = $2::date and ($3::text = '' or metric_id = $3)
			order by metric_id, legal_entity_id`, p.TenantID, date, metricID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRecon(rows)
			if err != nil {
				return err
			}
			items = append(items, r)
		}
		return rows.Err()
	})
	return items, err
}
