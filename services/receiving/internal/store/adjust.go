package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/outbox"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

func lockedReceipt(ctx context.Context, tx pgx.Tx, tenantID, id string) (Receipt, error) {
	if _, err := tx.Exec(ctx, `SELECT 1 FROM receiving_grir_open_item WHERE tenant_id = $1 AND receipt_id = $2 ORDER BY line_no FOR UPDATE`, tenantID, id); err != nil {
		return Receipt{}, err
	}
	return loadReceipt(ctx, tx, tenantID, id, true)
}

func enqueueAdjustment(ctx context.Context, tx pgx.Tx, tenantID, id string, adj Adjustment) error {
	if err := markPostingsDone(ctx, tx, tenantID, id, adj.LedgerEntryID != ""); err != nil {
		return err
	}
	if adj.Event == nil {
		return nil
	}
	return outbox.Enqueue(ctx, tx, receiptAggregate, id, *adj.Event)
}

func (s *Postgres) Correct(ctx context.Context, id string, fn AdjustFunc) (Receipt, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Receipt{}, err
	}
	var out Receipt
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := lockedReceipt(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if current.State != domain.Posted {
			return &domain.Error{Code: domain.CodeInvalidTransition, Detail: fmt.Sprintf("receipt %s is %s, not POSTED", id, current.State)}
		}
		adj, err := fn(current)
		if err != nil {
			return err
		}
		var lineRows, grirRows [][]any
		for _, c := range adj.Lines {
			lineRows = append(lineRows, []any{tenantID, id, c.LineNo, c.Qty, c.Amount, c.AmountAcct})
			grirRows = append(grirRows, []any{tenantID, id, c.LineNo, c.Qty, c.Amount})
		}
		if err := execBatch(ctx, tx, `UPDATE receiving_receipt_line SET qty = $4, amount = $5, amount_acct = $6 WHERE tenant_id = $1 AND receipt_id = $2 AND line_no = $3`, lineRows); err != nil {
			return err
		}
		if err := execBatch(ctx, tx, `UPDATE receiving_grir_open_item SET received_qty = $4::numeric, received_amount = $5,
			status = CASE WHEN invoiced_qty >= $4::numeric THEN 'CLEARED' WHEN invoiced_qty > 0 THEN 'PARTIAL' ELSE 'OPEN' END,
			cleared_at = CASE WHEN invoiced_qty >= $4::numeric THEN COALESCE(cleared_at, now()) END
			WHERE tenant_id = $1 AND receipt_id = $2 AND line_no = $3`, grirRows); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE receiving_receipt SET correction_seq = correction_seq + 1, row_version = row_version + 1, updated_at = now() WHERE tenant_id = $1 AND id = $2`, tenantID, id); err != nil {
			return err
		}
		if err := enqueueAdjustment(ctx, tx, tenantID, id, adj); err != nil {
			return err
		}
		out, err = loadReceipt(ctx, tx, tenantID, id, false)
		return err
	})
	return out, err
}

func (s *Postgres) Cancel(ctx context.Context, id string, fn AdjustFunc) (Receipt, error) {
	tenantID, err := tenantOf(ctx)
	if err != nil {
		return Receipt{}, err
	}
	var out Receipt
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		current, err := lockedReceipt(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		switch current.State {
		case domain.Arrived, domain.Registered, domain.Posted:
		default:
			return &domain.Error{Code: domain.CodeInvalidTransition, Detail: fmt.Sprintf("receipt %s is %s and cannot be cancelled", id, current.State)}
		}
		adj, err := fn(current)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE receiving_receipt SET state = 'CANCELLED', row_version = row_version + 1, updated_at = now() WHERE tenant_id = $1 AND id = $2`, tenantID, id); err != nil {
			return err
		}
		if current.State == domain.Posted {
			if _, err := tx.Exec(ctx, `UPDATE receiving_grir_open_item SET received_qty = 0, received_amount = 0, cleared_amount = 0, status = 'CLEARED', cleared_at = now()
				WHERE tenant_id = $1 AND receipt_id = $2`, tenantID, id); err != nil {
				return err
			}
		} else if _, err := tx.Exec(ctx, `DELETE FROM receiving_serial WHERE tenant_id = $1 AND receipt_id = $2`, tenantID, id); err != nil {
			return err
		}
		if err := enqueueAdjustment(ctx, tx, tenantID, id, adj); err != nil {
			return err
		}
		out, err = loadReceipt(ctx, tx, tenantID, id, false)
		return err
	})
	return out, err
}
