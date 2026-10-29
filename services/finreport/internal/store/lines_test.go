package store

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

// loadLedger stands in for the ledger consumer (a later slice): it writes the
// lines through PutJournalLines and the balances they add up to (lines at or
// below hwm only, closing lines also in closing_net), and sets the HWM.
func loadLedger(t testing.TB, ctx context.Context, pool *pgxpool.Pool, st *Postgres, ledger string, hwm int64, lines []domain.JournalLine) {
	t.Helper()
	if err := st.PutJournalLines(ctx, lines); err != nil {
		t.Fatal(err)
	}
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		p, _ := tenant.FromContext(ctx)
		if _, err := tx.Exec(ctx, `insert into reporting_balance(tenant_id, ledger_id, period_year, period_no, quarter, account, combination, currency_type, net, closing_net, legal_entity_id)
			select tenant_id, ledger_id, period_year, period_no, 1, account, combination, currency_type, sum(net),
			       coalesce(sum(net) filter (where closing_entry), 0), legal_entity_id
			from reporting_journal_line where ledger_id = $1 and posting_sequence <= $2
			group by tenant_id, ledger_id, period_year, period_no, account, combination, currency_type, legal_entity_id`, ledger, hwm); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `insert into reporting_hwm(tenant_id, ledger_id, posting_sequence, legal_entity_id) values($1,$2,$3,nullif($4,'')::uuid)`, p.TenantID, ledger, hwm, p.LegalEntityID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func line(ledger string, seq int64, date string, no int, account, debit, credit string) domain.JournalLine {
	return domain.JournalLine{LedgerID: ledger, PostingSequence: seq, LineNumber: 1, PostingDate: date, PeriodYear: 2026, PeriodNo: no,
		Account: account, Combination: "-", CurrencyType: "acct", Voucher: fmt.Sprintf("GJ-%d", seq),
		Debit: decimal.RequireFromString(debit), Credit: decimal.RequireFromString(credit)}
}

func drillDefinition(code string) domain.Definition {
	return domain.Definition{Code: code, Name: "Drill fixture", Kind: domain.KindIncomeStatement,
		Rows: []domain.Row{
			{Code: "REV", Kind: domain.RowAccountRange, Label: "Revenue", AccountFrom: "4000", AccountTo: "4999", NormalBalance: "credit"},
			{Code: "COST", Kind: domain.RowAccountRange, Label: "Cost", AccountFrom: "5000", AccountTo: "5999", NormalBalance: "debit"},
			{Code: "TOT", Kind: domain.RowTotal, Label: "Revenue and cost", References: []string{"REV", "COST"}, NormalBalance: "credit"},
			{Code: "NET", Kind: domain.RowCalculation, Label: "Net", Expression: "REV-COST", NormalBalance: "credit"},
		},
		Columns: []domain.Column{{Code: "P", Label: "Period", PeriodExpr: "P", CurrencyType: "acct"}, {Code: "Y", Label: "YTD", PeriodExpr: "YTD", CurrencyType: "acct"}},
	}
}

// walk reads every page of a drill and returns the items and the last page.
func walk(t testing.TB, ctx context.Context, st *Postgres, runID, row, col string, key, limit int) ([]LineItem, LinePage) {
	t.Helper()
	var all []LineItem
	q := LineQuery{Key: key, Limit: limit}
	for pages := 0; ; pages++ {
		page, err := st.DrillLines(ctx, runID, row, col, q)
		if err != nil {
			t.Fatal(err)
		}
		if page.Next != nil && len(page.Items) != limit {
			t.Fatalf("a page before the last has %d items, want %d", len(page.Items), limit)
		}
		sum := decimal.Zero
		for _, it := range page.Items {
			sum = sum.Add(it.Amount)
		}
		if !sum.Equal(page.SumOfLines) {
			t.Fatalf("page sum %s, sumOfLines %s", sum, page.SumOfLines)
		}
		all = append(all, page.Items...)
		running := decimal.Zero
		for _, it := range all {
			running = running.Add(it.Amount)
		}
		if !running.Equal(page.CumulativeSum) {
			t.Fatalf("page %d: cumulative %s, sum of the pages so far %s", pages, page.CumulativeSum, running)
		}
		if page.Next == nil {
			return all, page
		}
		if pages > 10000 {
			t.Fatal("cursor does not advance")
		}
		next := *page.Next
		q.After = &next
	}
}

// Drill level 2 against Postgres: BR-15.4 order, keyset pages with page and
// running sums that end on the cell value, signs per key on a total row,
// closing entries and postings after the run's HWM left out, one key at a
// time, computed cells, unknown cells and keys, a gap in the read model shown
// as unreconciled, and a retired definition version still drillable.
func TestDrillLines_PagesReconcileToCell(t *testing.T) {
	pool := testkit.DB(t)
	ctx := testkit.Ctx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	if _, _, err := st.SaveDraft(ctx, drillDefinition("DRILL-L2"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "DRILL-L2", "tester"); err != nil {
		t.Fatal(err)
	}
	ledger := ids.New()
	closing := line(ledger, 6, "2026-03-31", 3, "4000", "50.000000", "0")
	closing.ClosingEntry = true
	src := line(ledger, 2, "2026-03-05", 3, "4000", "0", "100.000000")
	src.SourceURI, src.TraceID = "/payables/invoices/INV-1", "4bf92f3577b34da6a3ce929d0e0e4736"
	second := line(ledger, 2, "2026-03-05", 3, "5000", "60.000000", "0")
	second.LineNumber = 2
	loadLedger(t, ctx, pool, st, ledger, 7, []domain.JournalLine{
		line(ledger, 4, "2026-03-20", 3, "4000", "0", "25.500000"),
		src, second,
		line(ledger, 1, "2026-03-05", 3, "4000", "0", "10.000000"),
		line(ledger, 3, "2026-02-10", 2, "4000", "0", "7.000000"),
		line(ledger, 5, "2026-03-21", 3, "5000", "0", "5.000000"),
		closing,
		line(ledger, 9, "2026-03-22", 3, "4000", "0", "999.000000"), // after the HWM
	})
	run, err := st.CreateRun(ctx, RunSpec{Code: "DRILL-L2", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026, PeriodNo: 3,
		Calendar: storeFixtureCalendar(), RequestedBy: "tester", IdempotencyKey: "drill-l2", RequestHash: "d"})
	if err != nil {
		t.Fatal(err)
	}
	rev, err := st.GetCell(ctx, run.ID, "REV", "P")
	if err != nil {
		t.Fatal(err)
	}
	if !rev.Value.Equal(decimal.RequireFromString("135.5")) {
		t.Fatalf("revenue P = %s, want 135.5 (closing and post-HWM lines excluded)", rev.Value)
	}

	items, last := walk(t, ctx, st, run.ID, "REV", "P", -1, 2)
	var got []string
	for _, it := range items {
		got = append(got, fmt.Sprintf("%s/%d/%d=%s", it.PostingDate, it.PostingSequence, it.LineNumber, it.Amount.StringFixed(2)))
	}
	want := []string{"2026-03-05/1/1=10.00", "2026-03-05/2/1=100.00", "2026-03-20/4/1=25.50"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("revenue lines %v, want %v", got, want)
	}
	if !last.CumulativeSum.Equal(rev.Value) || !last.Reconciliation.Reconciled() || last.HWM != 7 {
		t.Fatalf("final page: cumulative %s, value %s, %+v, hwm %d", last.CumulativeSum, rev.Value, last.Reconciliation, last.HWM)
	}
	if items[1].SourceURI != src.SourceURI || items[1].TraceID != src.TraceID || !items[1].Credit.Equal(decimal.RequireFromString("100")) {
		t.Fatalf("source line %+v", items[1])
	}

	// The total mixes a credit and a debit row: each line keeps its key's sign.
	tot, err := st.GetCell(ctx, run.ID, "TOT", "Y")
	if err != nil {
		t.Fatal(err)
	}
	items, last = walk(t, ctx, st, run.ID, "TOT", "Y", -1, 3)
	if len(items) != 6 || !last.CumulativeSum.Equal(tot.Value) || !last.Reconciliation.Reconciled() {
		t.Fatalf("total YTD: %d lines, cumulative %s, value %s, %+v", len(items), last.CumulativeSum, tot.Value, last.Reconciliation)
	}
	for _, it := range items {
		if it.Account == "5000" && it.PostingSequence == 2 && !it.Amount.Equal(decimal.RequireFromString("60")) {
			t.Fatalf("debit row line under a credit total shows %s, want 60", it.Amount)
		}
	}

	// One key only: its lines, its amount, keys named by their level 1 position.
	for i, k := range tot.Keys {
		items, last = walk(t, ctx, st, run.ID, "TOT", "Y", i, 500)
		if !last.Value.Equal(k.Amount) || !last.CumulativeSum.Equal(k.Amount) || !last.Reconciliation.Reconciled() {
			t.Fatalf("key %d: value %s, cumulative %s, key amount %s", i, last.Value, last.CumulativeSum, k.Amount)
		}
		for _, it := range items {
			if it.Key != i || it.Account != k.Account || it.Period() != k.Period {
				t.Fatalf("key %d returned line %+v", i, it)
			}
		}
	}
	if _, err := st.DrillLines(ctx, run.ID, "TOT", "Y", LineQuery{Key: len(tot.Keys), Limit: 10}); problemCode(err) != CodeCellNotFound {
		t.Fatalf("key past the end: %v", err)
	}

	page, err := st.DrillLines(ctx, run.ID, "NET", "P", LineQuery{Key: -1, Limit: 10})
	if err != nil || !page.Cell.Computed || len(page.Items) != 0 || !page.Reconciliation.Reconciled() {
		t.Fatalf("computed cell: %+v, %v", page, err)
	}
	if _, err := st.DrillLines(ctx, run.ID, "NOPE", "P", LineQuery{Key: -1, Limit: 10}); problemCode(err) != CodeCellNotFound {
		t.Fatalf("unknown cell: %v", err)
	}
	if _, err := st.DrillLines(ctx, ids.New(), "REV", "P", LineQuery{Key: -1, Limit: 10}); problemCode(err) != CodeRunNotFound {
		t.Fatalf("unknown run: %v", err)
	}

	// A line missing from the read model: shown, not hidden or refused.
	err = dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `delete from reporting_journal_line where ledger_id=$1 and posting_sequence=4`, ledger)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RetireDefinition(ctx, "DRILL-L2"); err != nil {
		t.Fatal(err)
	}
	page, err = st.DrillLines(ctx, run.ID, "REV", "P", LineQuery{Key: -1, Limit: 500})
	if err != nil {
		t.Fatalf("retired definition: %v", err)
	}
	rec := page.Reconciliation
	if rec.Reconciled() || rec.UnreconciledKeys != 1 || !rec.Difference.Equal(decimal.RequireFromString("25.5")) {
		t.Fatalf("gap: %+v", rec)
	}
}

// PutJournalLines refuses malformed lines, writes nothing of a refused batch,
// and treats a line already stored as a replay.
func TestPutJournalLines_ValidatesAndIsReplaySafe(t *testing.T) {
	st, ctx := migrated(t)
	ledger := ids.New()
	good := line(ledger, 1, "2026-03-05", 3, "4000", "0", "10")
	bad := line(ledger, 2, "2026-03-05", 3, "4000", "0", "10")
	bad.SourceURI = "javascript:alert(1)"
	if err := st.PutJournalLines(ctx, []domain.JournalLine{good, bad}); err == nil {
		t.Fatal("a script source link must be refused")
	}
	if err := st.PutJournalLines(ctx, []domain.JournalLine{good}); err != nil {
		t.Fatal(err)
	}
	changed := good
	changed.Credit = decimal.RequireFromString("99")
	if err := st.PutJournalLines(ctx, []domain.JournalLine{changed}); err != nil {
		t.Fatal(err)
	}
	var n int
	var credit string
	err := dbx.WithTenantTx(ctx, st.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*), max(credit)::text from reporting_journal_line where ledger_id=$1`, ledger).Scan(&n, &credit)
	})
	if err != nil || n != 1 || credit != "10.000000" {
		t.Fatalf("stored %d lines, credit %s, %v; want the first write kept", n, credit, err)
	}
}

// AC-15.3 (FR-15.7, BR-15.4) through Postgres: for random ledgers (lines in
// several periods, closing entries, postings after the HWM) every
// balance-backed cell drilled across all pages, at a random page size, ends
// on a cumulative sum equal to the cell value, reconciled, with each line
// listed once per key.
func TestDrillLines_PropertyPagesSumToCell(t *testing.T) {
	pool := testkit.DB(t)
	ctx := testkit.Ctx(testkit.TenantID(1))
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(pool)
	if _, _, err := st.SaveDraft(ctx, drillDefinition("DRILL-PROP"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(ctx, "DRILL-PROP", "tester"); err != nil {
		t.Fatal(err)
	}
	iteration := 0
	rapid.Check(t, func(rt *rapid.T) {
		iteration++
		ledger := ids.New()
		hwm := rapid.Int64Range(1, 40).Draw(rt, "hwm")
		n := rapid.IntRange(0, 40).Draw(rt, "n")
		var lines []domain.JournalLine
		for i := 0; i < n; i++ {
			month := rapid.IntRange(1, 3).Draw(rt, "month")
			l := line(ledger, int64(i+1), fmt.Sprintf("2026-%02d-%02d", month, rapid.IntRange(1, 28).Draw(rt, "day")), month,
				rapid.SampledFrom([]string{"4000", "4100", "5000"}).Draw(rt, "account"), "0", "0")
			l.Debit = decimal.New(rapid.Int64Range(0, 99999).Draw(rt, "debit"), -2)
			l.Credit = decimal.New(rapid.Int64Range(0, 99999).Draw(rt, "credit"), -2)
			l.LineNumber = rapid.IntRange(1, 2).Draw(rt, "line")
			l.ClosingEntry = rapid.IntRange(0, 4).Draw(rt, "closing") == 0
			lines = append(lines, l)
		}
		loadLedger(t, ctx, pool, st, ledger, hwm, lines)
		run, err := st.CreateRun(ctx, RunSpec{Code: "DRILL-PROP", LedgerID: ledger, CurrencyType: "acct", PeriodYear: 2026,
			PeriodNo: rapid.IntRange(1, 3).Draw(rt, "at"), Calendar: storeFixtureCalendar(), RequestedBy: "tester",
			IdempotencyKey: fmt.Sprintf("prop-%d", iteration), RequestHash: ledger})
		if err != nil {
			rt.Fatal(err)
		}
		cells, err := st.ListCells(ctx, run.ID)
		if err != nil {
			rt.Fatal(err)
		}
		limit := rapid.IntRange(1, 5).Draw(rt, "limit")
		for _, c := range cells {
			if c.Computed {
				continue
			}
			items, last := walk(t, ctx, st, run.ID, c.Row, c.Column, -1, limit)
			if !last.CumulativeSum.Equal(c.Value) || !last.Reconciliation.Reconciled() {
				rt.Fatalf("cell %s/%s = %s: cumulative %s, %+v", c.Row, c.Column, c.Value, last.CumulativeSum, last.Reconciliation)
			}
			seen := map[string]bool{}
			for _, it := range items {
				id := fmt.Sprintf("%d/%d/%d", it.Key, it.PostingSequence, it.LineNumber)
				if seen[id] {
					rt.Fatalf("cell %s/%s lists line %s twice", c.Row, c.Column, id)
				}
				seen[id] = true
				if it.PostingSequence > hwm {
					rt.Fatalf("cell %s/%s lists line %s posted after the HWM %d", c.Row, c.Column, id, hwm)
				}
			}
		}
	})
}
