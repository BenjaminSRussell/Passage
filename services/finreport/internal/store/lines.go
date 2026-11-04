package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
)

// LineQuery selects one page of drill level 2. Key is a balance key's position
// in the level 1 list, or -1 for every line of the cell. After is the cursor of
// the previous page (nil for the first); Limit is the page size.
type LineQuery struct {
	Key   int
	After *domain.LineCursor
	Limit int
}

// LineItem is a journal line under balance key Key (its position in the
// level 1 list) with its amount as the cell shows it (BR-15.3).
type LineItem struct {
	domain.JournalLine
	Key    int
	Amount decimal.Decimal
}

// LinePage is one page of the journal lines behind a cell (FR-15.7, BR-15.4).
// SumOfLines is the signed sum of this page, CumulativeSum the sum of every
// line up to the end of this page; on the final page (Next nil) it equals
// Reconciliation.LinesTotal. Computed cells have no lines.
type LinePage struct {
	Cell           domain.Cell
	Key            int
	Value          decimal.Decimal
	HWM            int64
	Items          []LineItem
	SumOfLines     decimal.Decimal
	CumulativeSum  decimal.Decimal
	Next           *domain.LineCursor
	Reconciliation domain.Reconciliation
}

// DrillLines returns one page of the journal lines behind a cell of a run, read
// from reporting_journal_line at the run's high-water mark and in BR-15.4 order
// (posting date, posting sequence, line number, key). Every page carries the
// reconciliation of all the cell's lines against its balance keys, so a gap in
// the read model is shown from the first page, never discovered on the last.
// Retired definition versions still resolve, as for outputs (BR-15.8).
func (s *Postgres) DrillLines(ctx context.Context, runID, row, column string, q LineQuery) (LinePage, error) {
	if q.Limit < 1 || q.Limit > domain.MaxLinePage {
		return LinePage{}, fmt.Errorf("line page size %d outside 1..%d", q.Limit, domain.MaxLinePage)
	}
	page := LinePage{Key: q.Key, SumOfLines: decimal.Zero, CumulativeSum: decimal.Zero, Items: []LineItem{}}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		run, err := getRunTx(ctx, tx, runID)
		if err != nil {
			return err
		}
		def, err := runDefinitionTx(ctx, tx, run)
		if err != nil {
			return err
		}
		cell, err := scanCell(tx.QueryRow(ctx, `select row_code, column_code, value::text, blank, computed, note, balance_keys::text
			from statement_cell where run_id=$1 and row_code=$2 and column_code=$3`, runID, row, column))
		if errors.Is(err, pgx.ErrNoRows) {
			return problem(404, CodeCellNotFound, fmt.Sprintf("cell %s/%s does not exist in run %s", row, column, runID))
		}
		if err != nil {
			return err
		}
		page.Cell, page.Value, page.HWM = cell, cell.Value, run.HWMByLedger[run.LedgerID]
		if cell.Computed {
			if q.Key >= 0 {
				return problem(404, CodeCellNotFound, fmt.Sprintf("cell %s/%s is computed and has no balance keys", row, column))
			}
			page.Reconciliation = domain.Reconciliation{LinesTotal: decimal.Zero, Difference: decimal.Zero}
			return nil
		}
		scope, err := domain.NewLineScope(def, cell, page.HWM, q.Key)
		if errors.Is(err, domain.ErrNoSuchKey) {
			return problem(404, CodeCellNotFound, err.Error())
		}
		if err != nil {
			return err
		}
		page.Value = scope.Value
		offset := 0
		if q.Key >= 0 {
			offset = q.Key
		}
		return drillPageTx(ctx, tx, run.LedgerID, scope, offset, q, &page)
	})
	return page, err
}

// lineJoin restricts journal lines to the scope's balances ($2..$7, aligned
// arrays with the key position first), the run's HWM ($8) and, for income
// statements and cash flows, to lines that are not closing entries ($9).
const lineJoin = `from unnest($2::int[], $3::int[], $4::int[], $5::text[], $6::text[], $7::text[]) as k(idx, py, pn, acct, combo, cur)
	join reporting_journal_line l
	  on l.ledger_id = $1::uuid and l.period_year = k.py and l.period_no = k.pn and l.account = k.acct
	 and l.combination = k.combo and l.currency_type = k.cur
	 and l.posting_sequence <= $8 and not ($9::boolean and l.closing_entry)`

// drillPageTx reads one page for scope; offset is the level 1 position of
// scope.Keys[0], so items and cursors name keys as the cell lists them.
func drillPageTx(ctx context.Context, tx pgx.Tx, ledgerID string, scope domain.LineScope, offset int, q LineQuery, page *LinePage) error {
	n := len(scope.Keys)
	idx, years, nos := make([]int32, n), make([]int32, n), make([]int32, n)
	accts, combos, curs := make([]string, n), make([]string, n), make([]string, n)
	for i, k := range scope.Keys {
		var year, no int
		if _, err := fmt.Sscanf(k.Period, "%d-%d", &year, &no); err != nil {
			return fmt.Errorf("balance key period %q: %w", k.Period, err)
		}
		idx[i], years[i], nos[i] = int32(i), int32(year), int32(no)
		accts[i], combos[i], curs[i] = k.Account, k.Combination, k.CurrencyType
	}
	args := []any{ledgerID, idx, years, nos, accts, combos, curs, scope.HWM, scope.ExcludeClosing}
	var cDate *string
	var cSeq int64
	var cLine, cKey int
	if q.After != nil {
		cDate, cSeq, cLine, cKey = &q.After.PostingDate, q.After.PostingSequence, q.After.LineNumber, q.After.Key-offset
	}

	// Per key: the net of every line in scope, and of the lines at or before
	// the cursor (the pages already served).
	rows, err := tx.Query(ctx, `select k.idx, sum(l.net)::text,
			coalesce(sum(l.net) filter (where (l.posting_date, l.posting_sequence, l.line_number, k.idx) <= ($10::date, $11::bigint, $12::int, $13::int)), 0)::text
		`+lineJoin+` group by k.idx`, append(args, cDate, cSeq, cLine, cKey)...)
	if err != nil {
		return err
	}
	netByKey := make([]decimal.Decimal, n)
	before := decimal.Zero // signed
	for rows.Next() {
		var i int
		var total, prior string
		if err := rows.Scan(&i, &total, &prior); err != nil {
			rows.Close()
			return err
		}
		t, err1 := decimal.NewFromString(total)
		p, err2 := decimal.NewFromString(prior)
		if err := errors.Join(err1, err2); err != nil {
			rows.Close()
			return err
		}
		netByKey[i] = t
		before = before.Add(scope.Signed(i, p))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	page.Reconciliation = scope.Reconcile(netByKey)
	page.CumulativeSum = before

	rows, err = tx.Query(ctx, `select k.idx, l.posting_sequence, l.line_number, to_char(l.posting_date, 'YYYY-MM-DD'), l.period_year, l.period_no,
			l.account, l.combination, l.currency_type, l.voucher, l.debit::text, l.credit::text, l.closing_entry, l.source_uri, l.trace_id
		`+lineJoin+`
		where $10::date is null or (l.posting_date, l.posting_sequence, l.line_number, k.idx) > ($10::date, $11::bigint, $12::int, $13::int)
		order by l.posting_date, l.posting_sequence, l.line_number, k.idx
		limit $14`, append(args, cDate, cSeq, cLine, cKey, q.Limit+1)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var l domain.JournalLine
		var i int
		var debit, credit string
		if err := rows.Scan(&i, &l.PostingSequence, &l.LineNumber, &l.PostingDate, &l.PeriodYear, &l.PeriodNo, &l.Account, &l.Combination,
			&l.CurrencyType, &l.Voucher, &debit, &credit, &l.ClosingEntry, &l.SourceURI, &l.TraceID); err != nil {
			return err
		}
		l.LedgerID = ledgerID
		var err1, err2 error
		l.Debit, err1 = decimal.NewFromString(debit)
		l.Credit, err2 = decimal.NewFromString(credit)
		if err := errors.Join(err1, err2); err != nil {
			return err
		}
		page.Items = append(page.Items, LineItem{JournalLine: l, Key: offset + i, Amount: scope.Signed(i, l.Net())})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(page.Items) > q.Limit {
		page.Items = page.Items[:q.Limit]
		last := page.Items[q.Limit-1]
		page.Next = &domain.LineCursor{PostingDate: last.PostingDate, PostingSequence: last.PostingSequence, LineNumber: last.LineNumber, Key: last.Key}
	}
	for _, it := range page.Items {
		page.SumOfLines = page.SumOfLines.Add(it.Amount)
	}
	page.CumulativeSum = page.CumulativeSum.Add(page.SumOfLines)
	return nil
}

// PutJournalLines writes ledger lines to the read model in one transaction.
// The journal is append-only, so a line already stored under the same (ledger,
// posting sequence, line number) is a replay and is left as it is. The ledger
// consumer (ApplyEntry) writes lines together with balances and the high-water
// mark; this call loads lines alone, for tests and fixtures.
func (s *Postgres) PutJournalLines(ctx context.Context, lines []domain.JournalLine) error {
	for _, l := range lines {
		if err := l.Validate(); err != nil {
			return err
		}
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		owners := map[[2]string]string{}
		for _, l := range lines {
			key := [2]string{l.LedgerID, l.LegalEntityID}
			entity, seen := owners[key]
			if !seen {
				var err error
				if entity, err = requireLedgerEntityTx(ctx, tx, l.LedgerID, l.LegalEntityID); err != nil {
					return err
				}
				owners[key] = entity
			}
			if err := insertLineTx(ctx, tx, l, entity); err != nil {
				return err
			}
		}
		return nil
	})
}

// insertLineTx stores one line; a line already stored under the same key is a
// replay and is left as it is.
func insertLineTx(ctx context.Context, tx pgx.Tx, l domain.JournalLine, entity string) error {
	_, err := tx.Exec(ctx, `insert into reporting_journal_line(tenant_id, ledger_id, posting_sequence, line_number, posting_date,
		period_year, period_no, account, combination, currency_type, voucher, debit, credit, closing_entry, source_uri, trace_id, legal_entity_id)
		values($1,$2::uuid,$3,$4,$5::date,$6,$7,$8,$9,$10,$11,$12::numeric,$13::numeric,$14,$15,$16,nullif($17,'')::uuid)
		on conflict (tenant_id, ledger_id, posting_sequence, line_number) do nothing`,
		tenantID(ctx), l.LedgerID, l.PostingSequence, l.LineNumber, l.PostingDate, l.PeriodYear, l.PeriodNo,
		l.Account, l.Combination, l.CurrencyType, l.Voucher, l.Debit.StringFixed(6), l.Credit.StringFixed(6),
		l.ClosingEntry, l.SourceURI, l.TraceID, entity)
	return err
}
