package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
)

// RecomputeReturnCredit works the credit of a CREDITED return order out again: its tax reversal, invoice lines,
// repricing and totals, from the invoice as billing holds it now. It is for a credit receivables never confirmed
// and an operator released there with :cancel (#557): the stored credit would be sent again unchanged, so a
// correction could never reach it. The next :credit sends the new amounts under the same RMA key; receivables
// accepts them once the old credit is gone and refuses them (idempotency reuse) while it still holds the old one.
//
// Do not use it after a lost answer without checking receivables: while the credit may be posted there, billing's
// invoice already counts it and the recomputation would see less to credit.
func (s *Postgres) RecomputeReturnCredit(ctx context.Context, id string, version int, actor string, now time.Time) (ReturnOrder, error) {
	current, err := s.GetReturnOrder(ctx, id)
	if err != nil {
		return ReturnOrder{}, err
	}
	if current.Version != version {
		return ReturnOrder{}, versionConflict()
	}
	if current.Status != domain.RMACredited || current.CreditNoteID != nil {
		return ReturnOrder{}, rmaState("only a CREDITED return order whose credit note receivables has not confirmed can be recomputed")
	}
	invoice, err := s.readTaxBasis(ctx, current)
	if err != nil {
		return ReturnOrder{}, err
	}
	p, _ := tenant.FromContext(ctx)
	var out ReturnOrder
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		o, err := loadReturn(ctx, tx, p.TenantID, id, true)
		if err != nil {
			return err
		}
		if o.Version != version {
			return versionConflict()
		}
		if _, err := tx.Exec(ctx, `delete from return_order_tax where tenant_id = $1 and return_order_id = $2`, p.TenantID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update return_order set credit_invoice_lines = null where tenant_id = $1 and id = $2`, p.TenantID, id); err != nil {
			return err
		}
		if err := s.storeCredit(now, invoice)(ctx, tx, p.TenantID, &o); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update return_order set version = version + 1, updated_at = $3, updated_by = $4 where tenant_id = $1 and id = $2`,
			p.TenantID, id, now, actor); err != nil {
			return err
		}
		out, err = loadReturn(ctx, tx, p.TenantID, id, false)
		return err
	})
	return out, err
}
