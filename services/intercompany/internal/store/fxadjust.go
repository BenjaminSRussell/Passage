package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
)

// GetReconItem returns one item with the run it belongs to, or IC-0851.
func (s *Postgres) GetReconItem(ctx context.Context, itemID string) (domain.ReconItem, domain.ReconRun, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return domain.ReconItem{}, domain.ReconRun{}, err
	}
	var (
		it  domain.ReconItem
		run domain.ReconRun
	)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		it, err = scanItem(tx.QueryRow(ctx, `select `+itemColumns+` from ic_reconciliation_item where tenant_id = $1 and id = $2::uuid`, pr.TenantID, itemID))
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeReconItemNotFound, Detail: "no reconciliation item with id " + itemID}
		}
		if err != nil {
			return err
		}
		run, err = scanRun(tx.QueryRow(ctx, `select `+runColumns+` from ic_reconciliation_run r where r.tenant_id = $1 and r.id = $2::uuid`, pr.TenantID, it.RunID))
		return err
	})
	if err != nil {
		return domain.ReconItem{}, domain.ReconRun{}, wrap("get reconciliation item", err)
	}
	return it, run, nil
}

const fxColumns = `id::text, item_id::text, entity, legal_entity_id::text, ledger_id, currency, amount::text, increases_balance, balance_account, fx_account,
	period_end, status, coalesce(ledger_entry_id, ''), posted_on`

func scanFX(row pgx.Row) (domain.FXAdjustment, error) {
	var a domain.FXAdjustment
	var amount string
	var postedOn *time.Time
	if err := row.Scan(&a.ID, &a.ItemID, &a.Entity, &a.Target.LegalEntityID, &a.Target.LedgerID, &a.Currency, &amount, &a.IncreasesBalance, &a.BalanceAccount, &a.FXAccount,
		&a.PeriodEnd, &a.Status, &a.LedgerEntryID, &postedOn); err != nil {
		return a, err
	}
	var err error
	if a.Amount, err = decimal.NewFromString(amount); err != nil {
		return a, err
	}
	if postedOn != nil {
		a.PostedOn = *postedOn
	}
	return a, nil
}

// BeginFXAdjustment stores the adjustment of an item as pending, or returns the one already stored for it (existing=true).
func (s *Postgres) BeginFXAdjustment(ctx context.Context, a domain.FXAdjustment) (domain.FXAdjustment, bool, error) {
	pr, by, err := principalOf(ctx)
	if err != nil {
		return domain.FXAdjustment{}, false, err
	}
	var (
		out      domain.FXAdjustment
		existing bool
	)
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `insert into ic_fx_adjustment (tenant_id, id, item_id, entity, legal_entity_id, ledger_id, currency, amount, increases_balance, balance_account,
				fx_account, period_end, status, created_by)
			values ($1, $2, $3::uuid, $4, $5::uuid, $6, $7, $8::numeric, $9, $10, $11, $12::date, 'pending', $13) on conflict (tenant_id, item_id) do nothing`,
			pr.TenantID, ids.New(), a.ItemID, a.Entity, a.Target.LegalEntityID, a.Target.LedgerID, a.Currency, a.Amount.String(), a.IncreasesBalance, a.BalanceAccount,
			a.FXAccount, a.PeriodEnd.Format(time.DateOnly), by)
		if err != nil {
			return err
		}
		existing = tag.RowsAffected() == 0
		out, err = scanFX(tx.QueryRow(ctx, `select `+fxColumns+` from ic_fx_adjustment where tenant_id = $1 and item_id = $2::uuid`, pr.TenantID, a.ItemID))
		return err
	})
	if err != nil {
		return domain.FXAdjustment{}, false, wrap("begin intercompany FX adjustment", err)
	}
	return out, existing, nil
}

// FinishFXAdjustment records the ledger entry of a pending adjustment and resolves its item with the note, in one transaction. An
// adjustment that is already posted is returned unchanged.
func (s *Postgres) FinishFXAdjustment(ctx context.Context, id, ledgerEntryID string, postedOn time.Time, note string) (domain.FXAdjustment, error) {
	pr, by, err := principalOf(ctx)
	if err != nil {
		return domain.FXAdjustment{}, err
	}
	var out domain.FXAdjustment
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanFX(tx.QueryRow(ctx, `select `+fxColumns+` from ic_fx_adjustment where tenant_id = $1 and id = $2::uuid for update`, pr.TenantID, id))
		if err != nil {
			return err
		}
		if cur.Status == "posted" {
			out = cur
			return nil
		}
		if _, err := tx.Exec(ctx, `update ic_fx_adjustment set status = 'posted', ledger_entry_id = $3, posted_on = $4::date where tenant_id = $1 and id = $2::uuid`,
			pr.TenantID, id, ledgerEntryID, postedOn.Format(time.DateOnly)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update ic_reconciliation_item set resolution = 'resolved', note = $3, resolved_by = $4, resolved_at = now()
			where tenant_id = $1 and id = $2::uuid and resolution = 'open'`, pr.TenantID, cur.ItemID, note, by); err != nil {
			return err
		}
		out, err = scanFX(tx.QueryRow(ctx, `select `+fxColumns+` from ic_fx_adjustment where tenant_id = $1 and id = $2::uuid`, pr.TenantID, id))
		return err
	})
	if err != nil {
		return domain.FXAdjustment{}, wrap("finish intercompany FX adjustment", err)
	}
	return out, nil
}

// GetFXAdjustment returns the adjustment stored for an item, if any.
func (s *Postgres) GetFXAdjustment(ctx context.Context, itemID string) (domain.FXAdjustment, bool, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return domain.FXAdjustment{}, false, err
	}
	var out domain.FXAdjustment
	found := true
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanFX(tx.QueryRow(ctx, `select `+fxColumns+` from ic_fx_adjustment where tenant_id = $1 and item_id = $2::uuid`, pr.TenantID, itemID))
		if errors.Is(err, pgx.ErrNoRows) {
			found = false
			return nil
		}
		return err
	})
	if err != nil {
		return domain.FXAdjustment{}, false, wrap("get intercompany FX adjustment", err)
	}
	return out, found, nil
}
