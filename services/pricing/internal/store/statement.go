package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
)

// MaxStatementLines is how many accrual rows a rebate statement lists. The totals are exact
// whatever the count: they come from the period row and an aggregate, not from the listed rows.
const MaxStatementLines = 5000

// rebateStatementQuery returns the period state and the exact totals (one row of each, uncorrelated
// so Postgres evaluates them once) joined to the accrual rows in invoice order, up to the limit.
// Totals run over every row, whatever the list limit. A retroactive delta row (retroactive, id
// retro:<run>:<period>) stores the period's cumulative basis, so only the other rows count to the basis.
const rebateStatementQuery = `with p as (
	select basis_value::text as bv, basis_qty::text as bq, accrued_total::text as acc from rebate_period
	where agreement_id = $1 and period_key = $2),
agg as (
	select coalesce(sum(accrued_amount) filter (where status = $3), 0)::text as settled, count(*)::int as n,
		coalesce(sum(accrued_amount), 0)::text as rows_acc,
		coalesce(sum(basis_value) filter (where not (retroactive and invoice_line_id like 'retro:%')), 0)::text as rows_value,
		coalesce(sum(basis_qty) filter (where not (retroactive and invoice_line_id like 'retro:%')), 0)::text as rows_qty
	from rebate_accrual where agreement_id = $1 and period_key = $2)
select p.bv, p.bq, p.acc, agg.settled, agg.n, agg.rows_acc, agg.rows_value, agg.rows_qty,
	l.id::text, l.invoice_line_id, l.invoice_date, l.basis_value::text, l.basis_qty::text, l.accrued_amount::text, l.currency::text, l.status, l.retroactive
from p cross join agg
left join (select * from rebate_accrual where agreement_id = $1 and period_key = $2
	order by invoice_date, retroactive, created_at, id limit $4) l on true
order by l.invoice_date, l.retroactive, l.created_at, l.id`

// RebateStatementInput reads what the rebate agreement statement (PRD §5.26.15) needs for
// one agreement and one period key, in one transaction so the header, totals and lines agree:
// the agreement, the period's cumulative state, the exact settled total and the accrual rows
// in invoice order (up to MaxStatementLines). A period with no accrual row is an empty input.
func (s *Postgres) RebateStatementInput(ctx context.Context, agreementID, periodKey string) (domain.RebateAgreement, domain.StatementInput, error) {
	var a domain.RebateAgreement
	var in domain.StatementInput
	err := s.StreamRebateStatement(ctx, agreementID, periodKey, MaxStatementLines,
		func(ag domain.RebateAgreement, head domain.StatementInput) error { a, in = ag, head; return nil },
		func(l domain.StatementLine) error { in.Lines = append(in.Lines, l); return nil })
	return a, in, err
}

// StreamRebateStatement reads a rebate agreement statement without holding its lines in
// memory. It hands the agreement and the input without lines (period state, exact totals and
// LineCount) to start, which may refuse the read before anything is written, then each
// accrual row in invoice order to line, up to limit rows (limit <= 0 reads every row). Header,
// totals and lines come from ONE statement, so the footer always agrees with the lines. A
// period with no accrual row calls start with an empty input. A callback error stops the read
// and is returned.
func (s *Postgres) StreamRebateStatement(ctx context.Context, agreementID, periodKey string, limit int,
	start func(domain.RebateAgreement, domain.StatementInput) error, line func(domain.StatementLine) error) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		a, err := loadAgreement(ctx, tx, `where id = $1`, agreementID)
		if errors.Is(err, pgx.ErrNoRows) {
			return agreementNotFound(agreementID)
		}
		if err != nil {
			return err
		}
		if e := domain.ValidStatementPeriod(a, periodKey); e != nil {
			return problemOf(e)
		}
		// Period state, totals and lines come from ONE statement, so they share one snapshot: under
		// READ COMMITTED (the tenant transaction's level) separate reads could straddle an accrual
		// posted in between and the footer would disagree with the lines. No period row means no
		// result row, which is the empty input. A NULL limit lists every row.
		var lim *int
		if limit > 0 {
			lim = &limit
		}
		rows, err := tx.Query(ctx, rebateStatementQuery, a.ID, periodKey, domain.StatusSettled, lim)
		if err != nil {
			return err
		}
		defer rows.Close()
		started := false
		for rows.Next() {
			var bv, bq, acc, settled, rowsAcc, rowsValue, rowsQty string
			var count int
			var id, lineID, cur, status, lbv, lbq, lacc *string
			var date *time.Time
			var retro *bool
			if err := rows.Scan(&bv, &bq, &acc, &settled, &count, &rowsAcc, &rowsValue, &rowsQty,
				&id, &lineID, &date, &lbv, &lbq, &lacc, &cur, &status, &retro); err != nil {
				return fmt.Errorf("scan rebate statement: %w", err)
			}
			if !started {
				started = true
				in := domain.StatementInput{Lines: []domain.StatementLine{}, LineCount: count,
					Period:      domain.PeriodState{Value: decimal.RequireFromString(bv), Qty: decimal.RequireFromString(bq), Accrued: decimal.RequireFromString(acc)},
					Settled:     decimal.RequireFromString(settled),
					RowsAccrued: decimal.RequireFromString(rowsAcc), RowsBasisValue: decimal.RequireFromString(rowsValue), RowsBasisQty: decimal.RequireFromString(rowsQty)}
				if err := start(a, in); err != nil {
					return err
				}
			}
			if id == nil {
				continue
			}
			if err := line(StatementLineOf(Accrual{ID: *id, AgreementID: a.ID, PeriodKey: periodKey,
				InvoiceLineID: *lineID, InvoiceDate: *date, BasisValue: decimal.RequireFromString(*lbv),
				BasisQty: decimal.RequireFromString(*lbq), Accrued: decimal.RequireFromString(*lacc), Currency: *cur,
				Status: *status, Retroactive: *retro})); err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !started {
			return start(a, domain.StatementInput{Lines: []domain.StatementLine{}})
		}
		return nil
	})
}

// StatementLineOf maps an accrual row to its statement line.
func StatementLineOf(a Accrual) domain.StatementLine {
	return domain.StatementLine{InvoiceLineID: a.InvoiceLineID, InvoiceDate: dayStr(a.InvoiceDate), BasisValue: a.BasisValue,
		BasisQty: a.BasisQty, Accrued: a.Accrued, Status: a.Status, Retroactive: a.Retroactive}
}
