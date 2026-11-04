package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

// MaxStatementLines is how many accrual rows a rebate statement lists. The totals are exact
// whatever the count: they come from the period row and an aggregate, not from the listed rows.
const MaxStatementLines = 5000

// RebateStatementInput reads what the rebate agreement statement (PRD §5.26.15) needs for
// one agreement and one period key, in one transaction so the header, totals and lines agree:
// the agreement, the period's cumulative state, the exact settled total and the accrual rows
// in invoice order (up to MaxStatementLines). A period with no accrual row is an empty input.
func (s *Postgres) RebateStatementInput(ctx context.Context, agreementID, periodKey string) (domain.RebateAgreement, domain.StatementInput, error) {
	var a domain.RebateAgreement
	var in domain.StatementInput
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		a, err = loadAgreement(ctx, tx, `where id = $1`, agreementID)
		if errors.Is(err, pgx.ErrNoRows) {
			return agreementNotFound(agreementID)
		}
		if err != nil {
			return err
		}
		if e := domain.ValidStatementPeriod(a, periodKey); e != nil {
			return problemOf(e)
		}
		var bv, bq, acc string
		err = tx.QueryRow(ctx, `select basis_value::text, basis_qty::text, accrued_total::text from rebate_period
			where agreement_id = $1 and period_key = $2`, a.ID, periodKey).Scan(&bv, &bq, &acc)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			in = domain.StatementInput{Lines: []domain.StatementLine{}}
			return nil
		case err != nil:
			return err
		}
		in.Period = domain.PeriodState{Value: decimal.RequireFromString(bv), Qty: decimal.RequireFromString(bq), Accrued: decimal.RequireFromString(acc)}
		// Totals over every row, whatever the list limit. A retroactive delta row (retroactive, id
		// retro:<run>:<period>) stores the period's cumulative basis, so only the other rows count to the basis.
		var settled, rowsAcc, rowsValue, rowsQty string
		if err := tx.QueryRow(ctx, `select coalesce(sum(accrued_amount) filter (where status = $3), 0)::text, count(*)::int,
			coalesce(sum(accrued_amount), 0)::text,
			coalesce(sum(basis_value) filter (where not (retroactive and invoice_line_id like 'retro:%')), 0)::text,
			coalesce(sum(basis_qty) filter (where not (retroactive and invoice_line_id like 'retro:%')), 0)::text
			from rebate_accrual where agreement_id = $1 and period_key = $2`, a.ID, periodKey, domain.StatusSettled).
			Scan(&settled, &in.LineCount, &rowsAcc, &rowsValue, &rowsQty); err != nil {
			return err
		}
		in.Settled = decimal.RequireFromString(settled)
		in.RowsAccrued, in.RowsBasisValue, in.RowsBasisQty = decimal.RequireFromString(rowsAcc), decimal.RequireFromString(rowsValue), decimal.RequireFromString(rowsQty)
		rows, err := tx.Query(ctx, accrualColumns+` where agreement_id = $1 and period_key = $2
			order by invoice_date, retroactive, created_at, id limit $3`, a.ID, periodKey, MaxStatementLines)
		if err != nil {
			return err
		}
		defer rows.Close()
		in.Lines = []domain.StatementLine{}
		for rows.Next() {
			l, err := scanAccrual(rows)
			if err != nil {
				return fmt.Errorf("scan accrual: %w", err)
			}
			in.Lines = append(in.Lines, StatementLineOf(l))
		}
		return rows.Err()
	})
	return a, in, err
}

// StatementLineOf maps an accrual row to its statement line.
func StatementLineOf(a Accrual) domain.StatementLine {
	return domain.StatementLine{InvoiceLineID: a.InvoiceLineID, InvoiceDate: dayStr(a.InvoiceDate), BasisValue: a.BasisValue,
		BasisQty: a.BasisQty, Accrued: a.Accrued, Status: a.Status, Retroactive: a.Retroactive}
}
