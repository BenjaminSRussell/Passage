package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	invevents "github.com/BenjaminSRussell/passage/services/inventory/internal/events"
)

// maxReconciliationList caps the run list (newest first).
const maxReconciliationList = 200

// ReconcileInput names the legal entity, currency, inventory account and run
// date of a reconciliation. LedgerBalance, when set, replaces the balance
// derived from the posted journals with the balance read from the ledger
// (wave 11), which also carries manual journals (AC-16.9).
type ReconcileInput struct {
	LegalEntityID string
	Currency      string
	Account       string
	RunDate       time.Time
	LedgerBalance *decimal.Decimal
}

// ReconciliationFilter narrows the run list. Empty fields do not filter.
type ReconciliationFilter struct {
	LegalEntityID string
	Status        string
}

// Reconcile compares the inventory value (sum of on-hand posted value of the
// legal entity in the currency) with the ledger balance of the inventory
// account (FR-16.12) and counts the on-hand rows with negative physical stock
// (FR-16.4). The result is upserted per (legal entity, account, currency, run
// date), so a re-run recomputes fully and overwrites that day's row; a
// non-zero difference writes reconciliation.diff to the outbox in the same
// transaction.
func (s *Postgres) Reconcile(ctx context.Context, in ReconcileInput) (domain.Reconciliation, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.Reconciliation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var invText string
		var negative int
		if err := tx.QueryRow(ctx, `select coalesce(sum(posted_value) filter (where currency = $3), 0)::text,
				count(*) filter (where posted_qty + received + registered - deducted - picked < 0)
			from on_hand where tenant_id = $1 and legal_entity_id = $2`,
			tp.TenantID, in.LegalEntityID, in.Currency).Scan(&invText, &negative); err != nil {
			return err
		}
		inventory, err := decimal.NewFromString(invText)
		if err != nil {
			return err
		}
		ledger, source := decimal.Zero, domain.LedgerSourceJournals
		if in.LedgerBalance != nil {
			ledger, source = *in.LedgerBalance, domain.LedgerSourceOverride
		} else {
			// gl_entry is domain.JournalEntry as JSON: {"currency", "lines": [{"account", "debit", "credit"}]}.
			var glText string
			if err := tx.QueryRow(ctx, `select coalesce(sum((l->>'debit')::numeric - (l->>'credit')::numeric), 0)::text
				from inventory_journal j cross join lateral jsonb_array_elements(j.gl_entry->'lines') as l
				where j.tenant_id = $1 and j.legal_entity_id = $2 and j.status = 'POSTED'
				  and j.currency = $3 and l->>'account' = $4`, tp.TenantID, in.LegalEntityID, in.Currency, in.Account).Scan(&glText); err != nil {
				return err
			}
			if ledger, err = decimal.NewFromString(glText); err != nil {
				return err
			}
		}
		cmp, err := domain.CompareBalances(inventory, ledger, in.Currency)
		if err != nil {
			return domainProblem(err)
		}
		out = domain.Reconciliation{LegalEntityID: in.LegalEntityID, Account: in.Account, Currency: in.Currency, RunDate: in.RunDate,
			InventoryValue: cmp.InventoryValue, LedgerBalance: cmp.LedgerBalance, Diff: cmp.Diff, Status: cmp.Status,
			LedgerSource: source, NegativeRows: negative}
		if err := tx.QueryRow(ctx, `insert into reconciliation_run (tenant_id, legal_entity_id, id, account, currency, run_date,
				inventory_value, ledger_balance, diff, status, ledger_source, negative_rows)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			on conflict (tenant_id, legal_entity_id, account, currency, run_date) do update set
				inventory_value = excluded.inventory_value, ledger_balance = excluded.ledger_balance, diff = excluded.diff,
				status = excluded.status, ledger_source = excluded.ledger_source, negative_rows = excluded.negative_rows, created_at = now()
			returning id::text, created_at`,
			tp.TenantID, in.LegalEntityID, ids.New(), in.Account, in.Currency, in.RunDate.Format(dateLayout),
			cmp.InventoryValue.String(), cmp.LedgerBalance.String(), cmp.Diff.String(), cmp.Status, source, negative).Scan(&out.ID, &out.CreatedAt); err != nil {
			return scopeProblem(err)
		}
		if cmp.Diff.IsZero() {
			return nil
		}
		return enqueue(ctx, tx, "reconciliation", out.ID, invevents.ReconciliationDiff, tp.TenantID, invevents.ReconciliationDiffData{
			RunID: out.ID, LegalEntityID: in.LegalEntityID, Account: in.Account, Currency: in.Currency, RunDate: in.RunDate.Format(dateLayout),
			InventoryValue: cmp.InventoryValue.String(), LedgerBalance: cmp.LedgerBalance.String(), Diff: cmp.Diff.String(),
			LedgerSource: source, NegativeRows: negative,
		})
	})
	return out, err
}

const reconciliationColumns = `id::text, legal_entity_id::text, account, currency::text, run_date, inventory_value::text,
	ledger_balance::text, diff::text, status, ledger_source, negative_rows, created_at`

func scanReconciliation(row pgx.Row) (domain.Reconciliation, error) {
	var r domain.Reconciliation
	var inv, led, diff string
	if err := row.Scan(&r.ID, &r.LegalEntityID, &r.Account, &r.Currency, &r.RunDate, &inv, &led, &diff,
		&r.Status, &r.LedgerSource, &r.NegativeRows, &r.CreatedAt); err != nil {
		return r, err
	}
	var err error
	if r.InventoryValue, err = decimal.NewFromString(inv); err != nil {
		return r, err
	}
	if r.LedgerBalance, err = decimal.NewFromString(led); err != nil {
		return r, err
	}
	r.Diff, err = decimal.NewFromString(diff)
	return r, err
}

// GetReconciliation reads one run visible to the caller (INV-REC-001 otherwise).
func (s *Postgres) GetReconciliation(ctx context.Context, id string) (domain.Reconciliation, error) {
	tp, _ := tenant.FromContext(ctx)
	var out domain.Reconciliation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanReconciliation(tx.QueryRow(ctx, `select `+reconciliationColumns+` from reconciliation_run
			where tenant_id = $1 and id = $2`, tp.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(http.StatusNotFound, domain.CodeReconciliationNotFound, "reconciliation run not found")
		}
		return err
	})
	return out, err
}

// ListReconciliations lists the runs visible to the caller, newest run date
// first, at most maxReconciliationList.
func (s *Postgres) ListReconciliations(ctx context.Context, f ReconciliationFilter) ([]domain.Reconciliation, error) {
	tp, _ := tenant.FromContext(ctx)
	out := []domain.Reconciliation{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+reconciliationColumns+` from reconciliation_run
			where tenant_id = $1 and ($2 = '' or legal_entity_id::text = $2) and ($3 = '' or status = $3)
			order by run_date desc, created_at desc, id desc limit $4`, tp.TenantID, f.LegalEntityID, f.Status, maxReconciliationList)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			r, err := scanReconciliation(rows)
			if err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
