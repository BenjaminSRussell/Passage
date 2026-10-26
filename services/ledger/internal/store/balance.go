package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/money"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// TrialBalance sums the projection per account over the filter. Balanced is
// true when total debits equal total credits and the net is zero (the
// reconciliation check "sum of Balance.net = 0").
func (s *Postgres) TrialBalance(ctx context.Context, f BalanceFilter) (TrialBalance, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return TrialBalance{}, err
	}
	out := TrialBalance{LedgerID: f.LedgerID, Rows: []BalanceRow{}}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := loadLedger(ctx, tx, p.TenantID, f.LedgerID, false)
		if err != nil {
			return err
		}
		out.Currency = l.Currency
		rows, err := tx.Query(ctx, `select a.account_number, a.name, sum(b.debit)::text, sum(b.credit)::text, sum(b.net)::text, sum(b.line_count)::bigint
			from balance b join main_account a on a.tenant_id = b.tenant_id and a.id = b.main_account_id
			where b.tenant_id = $1 and b.ledger_id = $2 and b.currency_type = 'acct'
				and ($3::text = '' or b.period_key >= $3::text) and ($4::text = '' or b.period_key <= $4::text)
				and ($5::text = '' or a.account_number >= $5::text) and ($6::text = '' or a.account_number <= $6::text)
			group by a.account_number, a.name order by a.account_number`,
			p.TenantID, f.LedgerID, f.PeriodFrom, f.PeriodTo, f.AccountFrom, f.AccountTo)
		if err != nil {
			return err
		}
		defer rows.Close()
		minor := money.MinorUnits(l.Currency)
		debit, credit, net := decimal.Zero, decimal.Zero, decimal.Zero
		for rows.Next() {
			var r BalanceRow
			var d, c, n string
			if err := rows.Scan(&r.Account, &r.Name, &d, &c, &n, &r.LineCount); err != nil {
				return err
			}
			dv, cv, nv := decimal.RequireFromString(d), decimal.RequireFromString(c), decimal.RequireFromString(n)
			debit, credit, net = debit.Add(dv), credit.Add(cv), net.Add(nv)
			r.Debit, r.Credit, r.Net = dv.StringFixed(minor), cv.StringFixed(minor), nv.StringFixed(minor)
			out.Rows = append(out.Rows, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		out.Debit, out.Credit, out.Net = debit.StringFixed(minor), credit.StringFixed(minor), net.StringFixed(minor)
		out.Balanced = debit.Equal(credit) && net.IsZero()
		return nil
	})
	return out, err
}

// rebuiltCTE recomputes the projection from the journal for one ledger.
const rebuiltCTE = `with built as (
	select l.main_account_id, l.dimension_combination_id, e.period_key,
		sum(case when l.debit_credit = 'D' then l.amount_acct else 0 end) as debit,
		sum(case when l.debit_credit = 'C' then l.amount_acct else 0 end) as credit,
		count(*) as line_count, max(e.posting_sequence) as last_seq
	from journal_line l join journal_entry e on e.tenant_id = l.tenant_id and e.id = l.entry_id
	where l.tenant_id = $1 and e.ledger_id = $2
	group by l.main_account_id, l.dimension_combination_id, e.period_key)`

// RebuildBalances recomputes the balance projection from the journal (BR-11.7,
// simplified: the ledger row lock blocks postings while the rebuild runs, so no
// shadow table or delta catch-up is needed). The journal is the truth; rows
// that differed are reported and replaced.
func (s *Postgres) RebuildBalances(ctx context.Context, ledgerID string) (RebuildResult, error) {
	p, err := principalOf(ctx)
	if err != nil {
		return RebuildResult{}, err
	}
	started := time.Now()
	res := RebuildResult{LedgerID: ledgerID}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		l, err := loadLedger(ctx, tx, p.TenantID, ledgerID, true)
		if err != nil {
			return err
		}
		res.HighWaterSequence = l.NextSequence - 1
		if err := tx.QueryRow(ctx, rebuiltCTE+` select count(*) from built b
			full join (select * from balance where tenant_id = $1 and ledger_id = $2 and currency_type = 'acct') x
				on x.main_account_id = b.main_account_id and x.dimension_combination_id = b.dimension_combination_id and x.period_key = b.period_key
			where b.main_account_id is null or x.main_account_id is null or b.debit <> x.debit or b.credit <> x.credit
				or b.line_count <> x.line_count or b.last_seq <> x.last_posting_sequence`,
			p.TenantID, ledgerID).Scan(&res.RowsDiffering); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from balance where tenant_id = $1 and ledger_id = $2`, p.TenantID, ledgerID); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, rebuiltCTE+` insert into balance (tenant_id, legal_entity_id, ledger_id, main_account_id, dimension_combination_id,
				period_key, currency, debit, credit, net, line_count, last_posting_sequence)
			select $1::uuid, $3::uuid, $2::uuid, b.main_account_id, b.dimension_combination_id, b.period_key, $4::char(3),
				b.debit, b.credit, b.debit - b.credit, b.line_count, b.last_seq
			from built b`,
			p.TenantID, ledgerID, l.LegalEntityID, l.Currency)
		if err != nil {
			return err
		}
		res.RowsWritten = tag.RowsAffected()
		res.DurationMs = time.Since(started).Milliseconds()
		return enqueue(ctx, tx, ledgerID, ledgerevents.BalanceRebuilt, p.TenantID, ledgerevents.BalanceRebuiltData{
			LedgerID: ledgerID, HighWaterSequence: res.HighWaterSequence, RowsWritten: res.RowsWritten,
			RowsDiffering: res.RowsDiffering, DurationMs: res.DurationMs,
		})
	})
	return res, err
}
