package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// MaxAutoApplyBatch caps the payments one scheduled pass considers for a legal entity.
const MaxAutoApplyBatch = 200

// AutoApplyLegalEntities lists the legal entities of the caller's tenant that hold payments the matcher has not settled
// (RECEIVED or SUGGESTED), so a tenant-wide job can run AutoApplyDue once per legal entity under a principal scoped to it.
func (s *Postgres) AutoApplyLegalEntities(ctx context.Context) ([]string, error) {
	p, _ := tenant.FromContext(ctx)
	var out []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select distinct legal_entity_id::text from payment where tenant_id = $1 and status in ('RECEIVED', 'SUGGESTED')`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var le string
			if err := rows.Scan(&le); err != nil {
				return err
			}
			out = append(out, le)
		}
		return rows.Err()
	})
	return out, err
}

// AutoApplyDue is the scheduled auto-application (30-4): it takes up to limit unapplied payments of the caller's legal-entity
// scope, the ones considered longest ago first, marks them considered and runs the matcher's AUTO_APPLY decision on each
// (AutoApplyPayments), so payments that arrived without an advice, and items opened after a payment came in, are settled without
// a clerk. A SUGGESTED or ON_ACCOUNT payment stays for a person, and moves to the back of the line.
func (s *Postgres) AutoApplyDue(ctx context.Context, limit int) (AutoApplyResult, error) {
	p, _ := tenant.FromContext(ctx)
	if limit < 1 || limit > MaxAutoApplyBatch {
		limit = MaxAutoApplyBatch
	}
	var ids []string
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text from payment where tenant_id = $1 and status in ('RECEIVED', 'SUGGESTED')
			order by auto_apply_at nulls first, received_date, id limit $2 for update skip locked`, p.TenantID, limit)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		_, err = tx.Exec(ctx, `update payment set auto_apply_at = now() where tenant_id = $1 and id = any($2::uuid[])`, p.TenantID, ids)
		return err
	})
	if err != nil || len(ids) == 0 {
		return AutoApplyResult{AppliedPaymentIDs: []string{}}, err
	}
	return s.AutoApplyPayments(ctx, ids)
}
