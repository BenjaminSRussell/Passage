package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
	icevents "github.com/BenjaminSRussell/passage/services/intercompany/internal/events"
)

const runColumns = `r.id::text, r.period, r.currency, r.fx_tolerance_pct::text, r.matched, r.timing, r.mismatch,
	(select count(*) from ic_reconciliation_item i where i.tenant_id = r.tenant_id and i.run_id = r.id and i.resolution = 'open'), r.created_by, r.created_at`

func scanRun(row pgx.Row) (domain.ReconRun, error) {
	var r domain.ReconRun
	var tol string
	if err := row.Scan(&r.ID, &r.Period, &r.Currency, &tol, &r.Matched, &r.Timing, &r.Mismatch, &r.Open, &r.CreatedBy, &r.CreatedAt); err != nil {
		return r, err
	}
	var err error
	if r.FXTolerancePct, err = decimal.NewFromString(tol); err != nil {
		return r, fmt.Errorf("store: tolerance %q: %w", tol, err)
	}
	return r, nil
}

const itemColumns = `id::text, run_id::text, entity, counterparty, ref, amount_a::text, amount_b::text, next_period_a::text, next_period_b::text, fx_applies,
	status, difference::text, tolerance_applied, resolution, note, coalesce(resolved_by, ''), resolved_at`

func scanItem(row pgx.Row) (domain.ReconItem, error) {
	var it domain.ReconItem
	var a, b, na, nb, diff, status, resolution string
	var resolvedAt *time.Time
	if err := row.Scan(&it.ID, &it.RunID, &it.Entity, &it.Counterparty, &it.Ref, &a, &b, &na, &nb, &it.FXApplies, &status, &diff, &it.ToleranceApplied,
		&resolution, &it.Note, &it.ResolvedBy, &resolvedAt); err != nil {
		return it, err
	}
	it.Status, it.Resolution = domain.ReconStatus(status), domain.Resolution(resolution)
	if resolvedAt != nil {
		it.ResolvedAt = *resolvedAt
	}
	for _, f := range []struct {
		dst *decimal.Decimal
		raw string
	}{{&it.AmountA, a}, {&it.AmountB, b}, {&it.NextPeriodA, na}, {&it.NextPeriodB, nb}, {&it.Difference, diff}} {
		var err error
		if *f.dst, err = decimal.NewFromString(f.raw); err != nil {
			return it, fmt.Errorf("store: item amount %q: %w", f.raw, err)
		}
	}
	return it, nil
}

func errRunNotFound(id string) error {
	return &domain.Error{Code: domain.CodeReconRunNotFound, Detail: "no reconciliation run with id " + id}
}

// CreateReconRun stores a classified run with its items as immutable rows; an
// item that needs action starts open. With an Idempotency-Key a retry returns
// the stored run (replayed=true). A run that leaves Timing or Mismatch items
// publishes reconciliation.flagged.v1 in the same transaction.
func (s *Postgres) CreateReconRun(ctx context.Context, run domain.ReconRun, items []domain.ReconItem, key, hash string) (domain.ReconRun, bool, error) {
	pr, by, err := principalOf(ctx)
	if err != nil {
		return domain.ReconRun{}, false, err
	}
	var (
		out      domain.ReconRun
		replayed bool
	)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		create := func() (domain.ReconRun, error) {
			id := ids.New()
			if _, err := tx.Exec(ctx, `insert into ic_reconciliation_run (tenant_id, id, period, currency, fx_tolerance_pct, request_hash, matched, timing, mismatch, created_by)
				values ($1, $2, $3, $4, $5::numeric, $6, $7, $8, $9, $10)`,
				pr.TenantID, id, run.Period, run.Currency, run.FXTolerancePct.String(), hash, run.Matched, run.Timing, run.Mismatch, by); err != nil {
				return domain.ReconRun{}, err
			}
			var batch pgx.Batch
			for i, it := range items {
				batch.Queue(`insert into ic_reconciliation_item (tenant_id, id, run_id, seq, entity, counterparty, ref, amount_a, amount_b, next_period_a, next_period_b,
						fx_applies, status, difference, tolerance_applied, resolution)
					values ($1, $2, $3, $4, $5, $6, $7, $8::numeric, $9::numeric, $10::numeric, $11::numeric, $12, $13, $14::numeric, $15, $16)`,
					pr.TenantID, ids.New(), id, i, it.Entity, it.Counterparty, it.Ref, it.AmountA.String(), it.AmountB.String(), it.NextPeriodA.String(), it.NextPeriodB.String(),
					it.FXApplies, string(it.Status), it.Difference.String(), it.ToleranceApplied, string(it.Resolution))
			}
			res := tx.SendBatch(ctx, &batch)
			for range batch.Len() {
				if _, err := res.Exec(); err != nil {
					_ = res.Close()
					return domain.ReconRun{}, err
				}
			}
			if err := res.Close(); err != nil {
				return domain.ReconRun{}, err
			}
			created, err := scanRun(tx.QueryRow(ctx, `select `+runColumns+` from ic_reconciliation_run r where r.tenant_id = $1 and r.id = $2::uuid`, pr.TenantID, id))
			if err != nil {
				return domain.ReconRun{}, err
			}
			if created.Timing+created.Mismatch == 0 {
				return created, nil
			}
			return created, enqueue(ctx, tx, "ic_reconciliation_run", created.ID, icevents.ReconciliationFlagged, pr.TenantID, icevents.ReconciliationFlaggedData{
				RunID: created.ID, Period: created.Period, Currency: created.Currency, Timing: created.Timing, Mismatch: created.Mismatch})
		}
		if key == "" {
			out, err = create()
			return err
		}
		out, replayed, err = idempotent(ctx, tx, pr.TenantID, key, "recon.run", struct{ H string }{hash}, create)
		return err
	})
	if err != nil {
		return domain.ReconRun{}, false, wrap("create reconciliation run", err)
	}
	return out, replayed, nil
}

// ListReconRuns returns the tenant's runs, newest first, after the cursor (the id of the last run of the previous page).
func (s *Postgres) ListReconRuns(ctx context.Context, after string, limit int) ([]domain.ReconRun, string, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return nil, "", err
	}
	out := []domain.ReconRun{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+runColumns+` from ic_reconciliation_run r
			where r.tenant_id = $1
			  and ($2::text = '' or (r.created_at, r.id) < (select created_at, id from ic_reconciliation_run where tenant_id = $1 and id = $2::uuid))
			order by r.created_at desc, r.id desc limit $3`, pr.TenantID, after, limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanRun(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, "", wrap("list reconciliation runs", err)
	}
	next := ""
	if len(out) > limit {
		out = out[:limit]
		next = out[limit-1].ID
	}
	return out, next, nil
}

// GetReconRun returns a run with its items in the order they were submitted
// (only those with the given status when it is not empty), or IC-0850.
func (s *Postgres) GetReconRun(ctx context.Context, id, status string) (domain.ReconRun, []domain.ReconItem, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return domain.ReconRun{}, nil, err
	}
	var (
		run   domain.ReconRun
		items = []domain.ReconItem{}
	)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		run, err = scanRun(tx.QueryRow(ctx, `select `+runColumns+` from ic_reconciliation_run r where r.tenant_id = $1 and r.id = $2::uuid`, pr.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return errRunNotFound(id)
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select `+itemColumns+` from ic_reconciliation_item where tenant_id = $1 and run_id = $2::uuid and ($3::text = '' or status = $3) order by seq`,
			pr.TenantID, id, status)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			it, err := scanItem(rows)
			if err != nil {
				return err
			}
			items = append(items, it)
		}
		return rows.Err()
	})
	if err != nil {
		return domain.ReconRun{}, nil, wrap("get reconciliation run", err)
	}
	return run, items, nil
}

// ResolveItem marks an open item resolved with a note and the caller. A matched
// item has nothing to resolve and a resolved item is final: both fail with IC-0852.
func (s *Postgres) ResolveItem(ctx context.Context, itemID, note string) (domain.ReconItem, string, error) {
	pr, by, err := principalOf(ctx)
	if err != nil {
		return domain.ReconItem{}, "", err
	}
	var (
		out      domain.ReconItem
		currency string
	)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanItem(tx.QueryRow(ctx, `select `+itemColumns+` from ic_reconciliation_item where tenant_id = $1 and id = $2::uuid for update`, pr.TenantID, itemID))
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeReconItemNotFound, Detail: "no reconciliation item with id " + itemID}
		}
		if err != nil {
			return err
		}
		if cur.Resolution != domain.ResolutionOpen {
			return &domain.Error{Code: domain.CodeReconNotOpen, Detail: fmt.Sprintf("the item is %s and has nothing left to resolve", cur.Resolution)}
		}
		out, err = scanItem(tx.QueryRow(ctx, `update ic_reconciliation_item set resolution = 'resolved', note = $3, resolved_by = $4, resolved_at = now()
			where tenant_id = $1 and id = $2::uuid returning `+itemColumns, pr.TenantID, itemID, note, by))
		if err != nil {
			return err
		}
		return tx.QueryRow(ctx, `select currency from ic_reconciliation_run where tenant_id = $1 and id = $2::uuid`, pr.TenantID, out.RunID).Scan(&currency)
	})
	if err != nil {
		return domain.ReconItem{}, "", wrap("resolve reconciliation item", err)
	}
	return out, currency, nil
}
