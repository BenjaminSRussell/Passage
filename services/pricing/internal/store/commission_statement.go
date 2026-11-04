package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// CommissionStatementFilter selects one rep's commission for one invoice month in one legal
// entity (PRD §5.26.15 commission statement).
type CommissionStatementFilter struct {
	LegalEntityID string
	RepID         string
	// PeriodKey is the invoice month such as 2027-03.
	PeriodKey string
}

// CommissionStatementTotal is the footer of a commission statement in one currency. Earned
// sums the positive transactions, Credited the negative ones (credit notes), Net both; they are
// aggregated by the database, so they stay exact however many lines the statement has.
type CommissionStatementTotal struct {
	Currency string
	Lines    int
	Earned   decimal.Decimal
	Credited decimal.Decimal
	Net      decimal.Decimal
}

// commissionStatementQuery reads the lines of the statement in invoice order with the
// per-currency totals attached as arrays. Lines and totals come from ONE statement, so they
// share one snapshot: under READ COMMITTED (the tenant transaction's level) a separate totals
// query could see a commission posted between the two reads and disagree with the lines. The
// totals subquery is uncorrelated, so Postgres evaluates it once.
const commissionStatementQuery = `with totals as (
	select array_agg(currency order by currency) as currencies, array_agg(n order by currency) as counts,
		array_agg(earned order by currency) as earned, array_agg(credited order by currency) as credited,
		array_agg(net order by currency) as net
	from (select currency::text as currency, count(*)::int as n,
			coalesce(sum(amount) filter (where amount >= 0), 0)::text as earned,
			coalesce(sum(amount) filter (where amount < 0), 0)::text as credited, sum(amount)::text as net
		from commission_transaction where legal_entity_id = $1::uuid and rep_id = $2 and period_key = $3
		group by currency) g)
` + commissionSelect + `, totals.currencies, totals.counts, totals.earned, totals.credited, totals.net
	from commission_transaction cross join totals
	where legal_entity_id = $1::uuid and rep_id = $2 and period_key = $3
	order by invoice_date, invoice_line_id, rep_seq, id`

// StreamCommissionStatement reads one rep's commission transactions of an invoice month for
// the commission statement without holding them in memory (a rep can have many thousands of
// lines a month, so the read is not capped at the list limit). It hands the per-currency
// totals to start, which may refuse the read before anything is written, then every
// transaction to line in invoice order; totals and lines are read by one statement, so the
// footer always agrees with the lines. A month without transactions calls start with no
// totals. A callback error stops the read and is returned.
func (s *Postgres) StreamCommissionStatement(ctx context.Context, f CommissionStatementFilter,
	start func([]CommissionStatementTotal) error, line func(CommissionTxn) error) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, commissionStatementQuery, f.LegalEntityID, f.RepID, f.PeriodKey)
		if err != nil {
			return err
		}
		defer rows.Close()
		started := false
		for rows.Next() {
			var cur, earned, credited, net []string
			var counts []int
			t, err := scanTxn(rows, &cur, &counts, &earned, &credited, &net)
			if err != nil {
				return fmt.Errorf("scan commission statement line: %w", err)
			}
			if !started {
				started = true
				totals, err := commissionStatementTotals(cur, counts, earned, credited, net)
				if err != nil {
					return err
				}
				if err := start(totals); err != nil {
					return err
				}
			}
			if err := line(t); err != nil {
				return err
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !started {
			return start([]CommissionStatementTotal{})
		}
		return nil
	})
}

func commissionStatementTotals(cur []string, counts []int, earned, credited, net []string) ([]CommissionStatementTotal, error) {
	if len(counts) != len(cur) || len(earned) != len(cur) || len(credited) != len(cur) || len(net) != len(cur) {
		return nil, fmt.Errorf("commission statement totals: %d currencies, %d counts, %d/%d/%d sums", len(cur), len(counts), len(earned), len(credited), len(net))
	}
	out := make([]CommissionStatementTotal, 0, len(cur))
	for i, c := range cur {
		out = append(out, CommissionStatementTotal{Currency: c, Lines: counts[i], Earned: decimal.RequireFromString(earned[i]),
			Credited: decimal.RequireFromString(credited[i]), Net: decimal.RequireFromString(net[i])})
	}
	return out, nil
}
