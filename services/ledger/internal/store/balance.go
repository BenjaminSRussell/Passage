package store

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/ledger/internal/domain"
	ledgerevents "github.com/BenjaminSRussell/passage/services/ledger/internal/events"
)

// TrialBalance sums the projection per account over the filter. Balanced is
// true when total debits equal total credits and the net is zero (the
// reconciliation check "sum of Balance.net = 0"). Dimension filters and
// group_by attributes must be attributes of the ledger; they reach SQL only as
// bind parameters. An empty filter value keeps the combinations without the
// attribute, the same rows a group_by puts under the empty value.
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
		if err := checkBalanceAttributes(l, f); err != nil {
			return err
		}
		out.Currency = l.Currency
		out.ExcludesYearEndClosing = f.ExcludeYearEndClosing
		if err := tx.QueryRow(ctx, `select quantity_rebuild_pending from ledger where tenant_id = $1 and id = $2`,
			p.TenantID, f.LedgerID).Scan(&out.QuantitiesPending); err != nil {
			return err
		}
		args := []any{p.TenantID, f.LedgerID, f.PeriodFrom, f.PeriodTo, f.AccountFrom, f.AccountTo}
		var closing map[string]closingTotals
		if f.ExcludeYearEndClosing {
			if closing, err = yearEndClosingTotals(ctx, tx, args); err != nil {
				return err
			}
		}
		arg := func(v any) string {
			args = append(args, v)
			return "$" + strconv.Itoa(len(args))
		}
		where := dimensionWhere(f.Dimensions, arg)
		out.Filtered = len(f.Dimensions) > 0
		groupExprs := ""
		for _, attr := range f.GroupBy {
			groupExprs += ", coalesce(d.dimensions->>" + arg(attr) + "::text, '')"
		}
		// The combination join is only paid for when a dimension is asked for: every
		// balance row has a combination (NOT NULL, foreign key), so it never drops rows.
		join, limit := "", ""
		if where != "" || len(f.GroupBy) > 0 {
			join = " join dimension_combination d on d.tenant_id = b.tenant_id and d.id = b.dimension_combination_id"
		}
		argsBeforeLimit := len(args)
		if len(f.GroupBy) > 0 {
			out.GroupBy = f.GroupBy
			limit = " limit " + arg(maxBalanceRows+1)
		}
		rows, err := tx.Query(ctx, `select a.account_number, a.name, sum(b.debit)::text, sum(b.credit)::text, sum(b.net)::text, sum(b.line_count)::bigint`+groupExprs+`
			from balance b join main_account a on a.tenant_id = b.tenant_id and a.id = b.main_account_id`+join+`
			where b.tenant_id = $1 and b.ledger_id = $2 and b.currency_type = 'acct'
				and ($3::text = '' or b.period_key >= $3::text) and ($4::text = '' or b.period_key <= $4::text)
				and ($5::text = '' or a.account_number >= $5::text) and ($6::text = '' or a.account_number <= $6::text)`+where+`
			group by a.account_number, a.name`+groupExprs+` order by a.account_number`+groupExprs+limit,
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		minor := money.MinorUnits(l.Currency)
		debit, credit, net := decimal.Zero, decimal.Zero, decimal.Zero
		for rows.Next() {
			if len(out.Rows) == maxBalanceRows {
				return httpx.NewProblem(http.StatusUnprocessableEntity, CodeBalanceTooManyRows, fmt.Sprintf(
					"the grouping gives more than %d rows; narrow it with an account range, a period range or a dimension filter", maxBalanceRows))
			}
			var r BalanceRow
			var d, c, n string
			groupVals := make([]string, len(f.GroupBy))
			dest := []any{&r.Account, &r.Name, &d, &c, &n, &r.LineCount}
			for i := range groupVals {
				dest = append(dest, &groupVals[i])
			}
			if err := rows.Scan(dest...); err != nil {
				return err
			}
			if len(f.GroupBy) > 0 {
				r.Dimensions = make(map[string]string, len(f.GroupBy))
				for i, attr := range f.GroupBy {
					r.Dimensions[attr] = groupVals[i]
				}
			}
			dv, cv, nv := decimal.RequireFromString(d), decimal.RequireFromString(c), decimal.RequireFromString(n)
			if x, ok := closing[r.Account]; ok {
				dv, cv, nv, r.LineCount = dv.Sub(x.debit), cv.Sub(x.credit), nv.Sub(x.debit).Add(x.credit), r.LineCount-x.lines
			}
			debit, credit, net = debit.Add(dv), credit.Add(cv), net.Add(nv)
			r.Debit, r.Credit, r.Net = dv.StringFixed(minor), cv.StringFixed(minor), nv.StringFixed(minor)
			out.Rows = append(out.Rows, r)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		rows.Close()
		if err := attachQuantities(ctx, tx, &out, groupExprs, join, where, args[:argsBeforeLimit], len(f.GroupBy)); err != nil {
			return err
		}
		out.Debit, out.Credit, out.Net = debit.StringFixed(minor), credit.StringFixed(minor), net.StringFixed(minor)
		out.Balanced = debit.Equal(credit) && net.IsZero()
		return nil
	})
	return out, err
}

type closingTotals struct {
	debit, credit decimal.Decimal
	lines         int64
}

// yearEndClosingTotals sums per account the lines of the entries the close service posted as year-end closings and of the
// entries that reverse them, within the period and account range args[2:6]. The service is the verified token issuer
// recorded in posted_by_service, so an entry another caller tags year_end_closing is not left out.
func yearEndClosingTotals(ctx context.Context, tx pgx.Tx, args []any) (map[string]closingTotals, error) {
	rows, err := tx.Query(ctx, `select a.account_number,
			coalesce(sum(case when l.debit_credit = 'D' and not l.negative_posting then l.amount_acct
				when l.debit_credit = 'C' and l.negative_posting then -l.amount_acct else 0 end), 0)::text,
			coalesce(sum(case when l.debit_credit = 'C' and not l.negative_posting then l.amount_acct
				when l.debit_credit = 'D' and l.negative_posting then -l.amount_acct else 0 end), 0)::text,
			count(*)
		from journal_line l join journal_entry e on e.tenant_id = l.tenant_id and e.id = l.entry_id
			join main_account a on a.tenant_id = l.tenant_id and a.id = l.main_account_id
		where l.tenant_id = $1 and e.ledger_id = $2
			and ($3::text = '' or e.period_key >= $3::text) and ($4::text = '' or e.period_key <= $4::text)
			and ($5::text = '' or a.account_number >= $5::text) and ($6::text = '' or a.account_number <= $6::text)
			and (e.id in (select c.id from journal_entry c where c.tenant_id = $1 and c.ledger_id = $2 and c.posted_by_service = $7 and c.source_type = $8)
				or e.reversal_of_entry_id in (select c.id from journal_entry c where c.tenant_id = $1 and c.ledger_id = $2 and c.posted_by_service = $7 and c.source_type = $8))
		group by a.account_number`, append(args[:6:6], ClosePostingService, YearEndClosingSourceType)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]closingTotals{}
	for rows.Next() {
		var account, d, c string
		var t closingTotals
		if err := rows.Scan(&account, &d, &c, &t.lines); err != nil {
			return nil, err
		}
		t.debit, t.credit = decimal.RequireFromString(d), decimal.RequireFromString(c)
		out[account] = t
	}
	return out, rows.Err()
}

// attachQuantities adds the statistical quantity totals (BR-11.2) to the trial balance rows and
// the grand total, per unit. It reads balance_quantity with the same filter, join and grouping as
// the amount query, so every row it finds belongs to a row already read.
func attachQuantities(ctx context.Context, tx pgx.Tx, out *TrialBalance, groupExprs, join, where string, args []any, groups int) error {
	rows, err := tx.Query(ctx, `select a.account_number, b.unit, sum(b.debit_quantity)::text, sum(b.credit_quantity)::text, sum(b.net_quantity)::text`+groupExprs+`
		from balance_quantity b join main_account a on a.tenant_id = b.tenant_id and a.id = b.main_account_id`+join+`
		where b.tenant_id = $1 and b.ledger_id = $2
			and ($3::text = '' or b.period_key >= $3::text) and ($4::text = '' or b.period_key <= $4::text)
			and ($5::text = '' or a.account_number >= $5::text) and ($6::text = '' or a.account_number <= $6::text)`+where+`
		group by a.account_number, b.unit`+groupExprs+` order by a.account_number, b.unit`+groupExprs,
		args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	rowKey := func(account string, vals []string) string { return account + "\x1f" + strings.Join(vals, "\x1f") }
	index := make(map[string]int, len(out.Rows))
	for i, r := range out.Rows {
		vals := make([]string, groups)
		for j, attr := range out.GroupBy {
			vals[j] = r.Dimensions[attr]
		}
		index[rowKey(r.Account, vals)] = i
	}
	type sums struct{ debit, credit, net decimal.Decimal }
	totals := map[string]sums{}
	for rows.Next() {
		var account, unit, d, c, n string
		vals := make([]string, groups)
		dest := []any{&account, &unit, &d, &c, &n}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return err
		}
		i, ok := index[rowKey(account, vals)]
		if !ok {
			continue
		}
		dv, cv, nv := decimal.RequireFromString(d), decimal.RequireFromString(c), decimal.RequireFromString(n)
		out.Rows[i].Quantities = append(out.Rows[i].Quantities, QuantityTotal{
			Unit: unit, Debit: dv.StringFixed(domain.QuantityScale), Credit: cv.StringFixed(domain.QuantityScale), Net: nv.StringFixed(domain.QuantityScale)})
		t := totals[unit] // the zero decimal adds like decimal.Zero
		totals[unit] = sums{t.debit.Add(dv), t.credit.Add(cv), t.net.Add(nv)}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	units := make([]string, 0, len(totals))
	for u := range totals {
		units = append(units, u)
	}
	sort.Strings(units)
	for _, u := range units {
		t := totals[u]
		out.Quantities = append(out.Quantities, QuantityTotal{Unit: u, Debit: t.debit.StringFixed(domain.QuantityScale), Credit: t.credit.StringFixed(domain.QuantityScale), Net: t.net.StringFixed(domain.QuantityScale)})
	}
	return nil
}

// checkBalanceAttributes refuses dimension filters and group_by attributes the
// ledger does not define (posting refuses them too, so they could only match nothing).
func checkBalanceAttributes(l Ledger, f BalanceFilter) error {
	known := make(map[string]bool, len(l.DimensionAttributes))
	for _, a := range l.DimensionAttributes {
		known[a] = true
	}
	fields := map[string]string{}
	for attr := range f.Dimensions {
		if !known[attr] {
			fields["dim."+attr] = "is not a dimension attribute of the ledger"
		}
	}
	for _, attr := range f.GroupBy {
		if !known[attr] {
			fields["group_by"] = attr + " is not a dimension attribute of the ledger"
			break
		}
	}
	if len(fields) == 0 {
		return nil
	}
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
	p.Errors = fields
	return p
}

// dimensionWhere renders exact dimension filters on the combination alias d:
// values are matched by containment, an empty value means the attribute is not
// set. Attributes and values are bind parameters only.
func dimensionWhere(dims map[string]string, arg func(any) string) string {
	present := map[string]string{}
	absent := make([]string, 0, len(dims))
	for attr, v := range dims {
		if v == "" {
			absent = append(absent, attr)
		} else {
			present[attr] = v
		}
	}
	where := ""
	if len(present) > 0 {
		where += " and d.dimensions @> " + arg(present) + "::jsonb"
	}
	if len(absent) > 0 {
		sort.Strings(absent)
		where += " and not (d.dimensions ?| " + arg(absent) + "::text[])"
	}
	return where
}

// rebuiltCTE recomputes the projection from the journal for one ledger.
const rebuiltCTE = `with built as (
	select l.main_account_id, l.dimension_combination_id, e.period_key,
		-- A negative posting (BR-11.16) reduces the original side, so its contribution is negated.
		sum(case when l.debit_credit = 'D' and not l.negative_posting then l.amount_acct
			when l.debit_credit = 'C' and l.negative_posting then -l.amount_acct else 0 end) as debit,
		sum(case when l.debit_credit = 'C' and not l.negative_posting then l.amount_acct
			when l.debit_credit = 'D' and l.negative_posting then -l.amount_acct else 0 end) as credit,
		count(*) as line_count, max(e.posting_sequence) as last_seq
	from journal_line l join journal_entry e on e.tenant_id = l.tenant_id and e.id = l.entry_id
	where l.tenant_id = $1 and e.ledger_id = $2
	group by l.main_account_id, l.dimension_combination_id, e.period_key)`

// rebuiltQuantityCTE recomputes the statistical quantity projection (BR-11.2) from the journal for one ledger.
const rebuiltQuantityCTE = `with built as (
	select l.main_account_id, l.dimension_combination_id, e.period_key, l.unit,
		sum(case when l.debit_credit = 'D' and not l.negative_posting then l.quantity
			when l.debit_credit = 'C' and l.negative_posting then -l.quantity else 0 end) as debit,
		sum(case when l.debit_credit = 'C' and not l.negative_posting then l.quantity
			when l.debit_credit = 'D' and l.negative_posting then -l.quantity else 0 end) as credit,
		count(*) as line_count
	from journal_line l join journal_entry e on e.tenant_id = l.tenant_id and e.id = l.entry_id
	where l.tenant_id = $1 and e.ledger_id = $2 and l.quantity is not null
	group by l.main_account_id, l.dimension_combination_id, e.period_key, l.unit)`

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
		if err := tx.QueryRow(ctx, `select quantity_rebuild_pending from ledger where tenant_id = $1 and id = $2`,
			p.TenantID, ledgerID).Scan(&res.QuantityBackfill); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, rebuiltCTE+` select count(*) from built b
			full join (select * from balance where tenant_id = $1 and ledger_id = $2 and currency_type = 'acct') x
				on x.main_account_id = b.main_account_id and x.dimension_combination_id = b.dimension_combination_id and x.period_key = b.period_key
			where b.main_account_id is null or x.main_account_id is null or b.debit <> x.debit or b.credit <> x.credit
				or b.line_count <> x.line_count or b.last_seq <> x.last_posting_sequence`,
			p.TenantID, ledgerID).Scan(&res.RowsDiffering); err != nil {
			return err
		}
		var quantityDiffering int64
		if err := tx.QueryRow(ctx, rebuiltQuantityCTE+` select count(*) from built b
			full join (select * from balance_quantity where tenant_id = $1 and ledger_id = $2) x
				on x.main_account_id = b.main_account_id and x.dimension_combination_id = b.dimension_combination_id
				and x.period_key = b.period_key and x.unit = b.unit
			where b.main_account_id is null or x.main_account_id is null or b.debit <> x.debit_quantity or b.credit <> x.credit_quantity
				or b.line_count <> x.line_count`,
			p.TenantID, ledgerID).Scan(&quantityDiffering); err != nil {
			return err
		}
		// The first rebuild after migration 0027 backfills the quantity lines posted before it:
		// those rows are missing by design, not drift, so they do not count as differing (which
		// raises a P1 alert on balance.rebuilt).
		if !res.QuantityBackfill {
			res.RowsDiffering += quantityDiffering
		}
		if _, err := tx.Exec(ctx, `delete from balance where tenant_id = $1 and ledger_id = $2`, p.TenantID, ledgerID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from balance_quantity where tenant_id = $1 and ledger_id = $2`, p.TenantID, ledgerID); err != nil {
			return err
		}
		qtag, err := tx.Exec(ctx, rebuiltQuantityCTE+` insert into balance_quantity (tenant_id, legal_entity_id, ledger_id, main_account_id,
				dimension_combination_id, period_key, unit, debit_quantity, credit_quantity, net_quantity, line_count)
			select $1::uuid, $3::uuid, $2::uuid, b.main_account_id, b.dimension_combination_id, b.period_key, b.unit,
				b.debit, b.credit, b.debit - b.credit, b.line_count
			from built b`, p.TenantID, ledgerID, l.LegalEntityID)
		if err != nil {
			return err
		}
		res.QuantityRowsWritten = qtag.RowsAffected()
		if res.QuantityBackfill {
			if _, err := tx.Exec(ctx, `update ledger set quantity_rebuild_pending = false where tenant_id = $1 and id = $2`,
				p.TenantID, ledgerID); err != nil {
				return err
			}
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
