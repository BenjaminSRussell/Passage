package store

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/migrations"
)

func consolidationDefinition(code string) domain.Definition {
	def := storeFixtureDefinition(code)
	def.Columns = []domain.Column{{Code: "C01", Label: "Period", PeriodExpr: "P", CurrencyType: "rpt"}}
	return def
}

// FR-15.5: a consolidated run has one column per entity and their sum, reads
// every ledger under the caller's legal-entity scope, and is visible only to a
// caller whose scope covers every entity of the run.
func TestConsolidatedRun_ColumnPerEntityUnderScope(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	all := scopedCtx(testkit.TenantID(1), "")
	if err := dbx.Migrate(all, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	testkit.AssertTenantRLS(t, admin)
	st := New(app)
	ea, eb, ec := ids.New(), ids.New(), ids.New()
	la, lb, lc := ids.New(), ids.New(), ids.New()
	for _, p := range []struct{ ledger, entity, net string }{{la, ea, "10"}, {lb, eb, "20"}, {lc, ec, "40"}} {
		seedBalance(t, scopedCtx(testkit.TenantID(1), p.entity), app, p.ledger, 2026, 3, "1000", "rpt", p.net, 5)
	}
	if _, _, err := st.SaveDraft(all, consolidationDefinition("CONS"), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(all, "CONS", "tester"); err != nil {
		t.Fatal(err)
	}
	spec := func(key string, ledgers ...string) RunSpec {
		s := RunSpec{Code: "CONS", LedgerID: ledgers[0], CurrencyType: "rpt", PeriodYear: 2026, PeriodNo: 3, Calendar: storeFixtureCalendar(),
			RequestedBy: "tester", RequestHash: "h" + key, IdempotencyKey: key}
		for i, l := range ledgers {
			s.Consolidation = append(s.Consolidation, domain.LedgerColumn{LedgerID: l, Label: string(rune('A' + i))})
		}
		return s
	}

	run, err := st.CreateRun(all, spec("k-all", la, lb, lc))
	if err != nil {
		t.Fatalf("all-entity consolidated run: %v", err)
	}
	if len(run.Consolidation) != 3 || run.LedgerID != la || len(run.HWMByLedger) != 3 {
		t.Fatalf("run = %+v", run)
	}
	cells, err := st.ListCells(all, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"C01": "70", "C01_E1": "10", "C01_E2": "20", "C01_E3": "40"}
	got := 0
	for _, c := range cells {
		if c.Row != "R001" {
			continue
		}
		if w, ok := want[c.Column]; !ok || !c.Value.Equal(decimal.RequireFromString(w)) {
			t.Errorf("R001/%s = %s, want %s", c.Column, c.Value, w)
		}
		if err := c.CheckDrill(); err != nil {
			t.Errorf("drill %s: %v", c.Column, err)
		}
		got++
	}
	if got != 4 {
		t.Fatalf("R001 has %d columns, want 4", got)
	}
	def, err := st.RunDefinition(all, run.ID)
	if err != nil || len(def.Columns) != 4 || def.Columns[2].Label != "Period - B" {
		t.Fatalf("run definition = %+v, %v", def.Columns, err)
	}
	if again, err := st.CreateRun(all, spec("k-all", la, lb, lc)); err != nil || !again.Replayed || again.ID != run.ID || len(again.Consolidation) != 3 {
		t.Errorf("replay = %+v, %v", again, err)
	}
	if n := countAs(t, app, all, `select count(*) from audit_trail_entry where object_ref = $1 and kind = 'statement_run'`, "run:"+run.ID); n != 3 {
		t.Errorf("%d audit entries, want one per ledger (3)", n)
	}

	t.Run("a ledger outside the caller's scope is refused, never dropped", func(t *testing.T) {
		_, err := st.CreateRun(scopedCtx(testkit.TenantID(1), ea+","+eb), spec("k-out", la, lb, lc))
		if problemCode(err) != CodeLedgerNotFound {
			t.Errorf("err = %v, want %s", err, CodeLedgerNotFound)
		}
	})
	t.Run("a run is visible only to callers covering all its entities", func(t *testing.T) {
		both := scopedCtx(testkit.TenantID(1), ea+","+eb)
		small, err := st.CreateRun(both, spec("k-ab", la, lb))
		if err != nil {
			t.Fatalf("scope A,B over A,B: %v", err)
		}
		cellCount := func(ctx context.Context, id string) int {
			return countAs(t, app, ctx, `select count(*) from statement_cell where run_id = $1`, id)
		}
		for name, c := range map[string]struct {
			scope string
			runs  int
		}{"A,B": {ea + "," + eb, 1}, "A,B,C": {ea + "," + eb + "," + ec, 1}, "A": {ea, 0}, "B,C": {eb + "," + ec, 0}, "all": {"", 1}} {
			ctx := scopedCtx(testkit.TenantID(1), c.scope)
			if n := countAs(t, app, ctx, `select count(*) from statement_run where id = $1`, small.ID); n != c.runs {
				t.Errorf("scope %s sees %d runs, want %d", name, n, c.runs)
			}
			if n := cellCount(ctx, small.ID); (n > 0) != (c.runs == 1) {
				t.Errorf("scope %s sees %d cells, want some=%v", name, n, c.runs == 1)
			}
		}
		if _, err := st.GetRun(scopedCtx(testkit.TenantID(1), ea), small.ID); problemCode(err) != CodeRunNotFound {
			t.Errorf("entity A reads the A+B consolidation: %v", err)
		}
		// The all-entity run of three entities is hidden from A,B too.
		if n := countAs(t, app, both, `select count(*) from statement_run where id = $1`, run.ID); n != 0 {
			t.Errorf("scope A,B sees the A+B+C run")
		}
	})
	t.Run("refusals", func(t *testing.T) {
		open := consolidationDefinition("CONS-OPEN")
		open.Columns[0].CurrencyType = "" // takes the run's currency type
		if _, _, err := st.SaveDraft(all, open, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Publish(all, "CONS-OPEN", "tester"); err != nil {
			t.Fatal(err)
		}
		acct := spec("k-acct", la, lb)
		acct.Code, acct.CurrencyType = "CONS-OPEN", "acct"
		if _, err := st.CreateRun(all, acct); anyCode(err) != domain.CodeConsolidation {
			t.Errorf("acct currency: %v", err)
		}
		withTree := spec("k-tree", la, lb)
		withTree.Tree = &TreeRun{Code: "T"}
		if _, err := st.CreateRun(all, withTree); anyCode(err) != domain.CodeConsolidation {
			t.Errorf("tree: %v", err)
		}
		if _, err := st.CreateRun(all, spec("k-dup", la, la)); anyCode(err) != domain.CodeConsolidation {
			t.Errorf("duplicate ledger: %v", err)
		}
	})
}

// BR-15.4 for a consolidated run: the lines of an entity column are its
// ledger's lines at that ledger's HWM, and the lines of the consolidated column
// span every ledger, each at its own HWM, in one keyset order whose cumulative
// sum ends on the cell value.
func TestConsolidatedRun_DrillLinesAcrossLedgers(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	all := scopedCtx(testkit.TenantID(1), "")
	if err := dbx.Migrate(all, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app)
	ea, eb := ids.New(), ids.New()
	la, lb := ids.New(), ids.New()
	rpt := func(ledger, entity string, seq int64, date, credit string) domain.JournalLine {
		l := line(ledger, seq, date, 3, "4000", "0", credit)
		l.CurrencyType, l.LegalEntityID = "rpt", entity
		return l
	}
	// Ledger A at HWM 2 (sequence 3 is later), ledger B at HWM 1. A's and B's
	// first lines share date, sequence and line number.
	loadLedger(t, scopedCtx(testkit.TenantID(1), ea), app, st, la, 2, []domain.JournalLine{
		rpt(la, ea, 1, "2026-03-05", "100.000000"), rpt(la, ea, 2, "2026-03-10", "30.000000"), rpt(la, ea, 3, "2026-03-11", "999.000000")})
	loadLedger(t, scopedCtx(testkit.TenantID(1), eb), app, st, lb, 1, []domain.JournalLine{
		rpt(lb, eb, 1, "2026-03-05", "50.000000"), rpt(lb, eb, 2, "2026-03-06", "7.000000")})
	def := domain.Definition{Code: "CONS-DRILL", Name: "Consolidated drill", Kind: domain.KindIncomeStatement,
		Rows:    []domain.Row{{Code: "REV", Kind: domain.RowAccountRange, Label: "Revenue", AccountFrom: "4000", AccountTo: "4999", NormalBalance: "credit"}},
		Columns: []domain.Column{{Code: "P", Label: "Period", PeriodExpr: "P", CurrencyType: "rpt"}}}
	if _, _, err := st.SaveDraft(all, def, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Publish(all, "CONS-DRILL", "tester"); err != nil {
		t.Fatal(err)
	}
	run, err := st.CreateRun(all, RunSpec{Code: "CONS-DRILL", LedgerID: la, CurrencyType: "rpt", PeriodYear: 2026, PeriodNo: 3, Calendar: storeFixtureCalendar(),
		RequestedBy: "tester", RequestHash: "h", IdempotencyKey: "k-drill",
		Consolidation: []domain.LedgerColumn{{LedgerID: la, Label: "A"}, {LedgerID: lb, Label: "B"}}})
	if err != nil {
		t.Fatal(err)
	}

	items, last := walk(t, all, st, run.ID, "REV", "P", -1, 1)
	if len(items) != 3 || !last.CumulativeSum.Equal(decimal.RequireFromString("180")) || !last.Value.Equal(last.CumulativeSum) {
		t.Fatalf("consolidated column: %d lines summing to %s, cell %s; want 3 lines summing to 180", len(items), last.CumulativeSum, last.Value)
	}
	if last.HWM != 0 || last.Reconciliation.UnreconciledKeys != 0 {
		t.Errorf("consolidated column hwm %d, unreconciled %d; want 0 and 0", last.HWM, last.Reconciliation.UnreconciledKeys)
	}
	ledgersSeen := map[string]int{}
	for _, it := range items {
		ledgersSeen[it.LedgerID]++
	}
	if ledgersSeen[la] != 2 || ledgersSeen[lb] != 1 {
		t.Errorf("lines per ledger = %v, want 2 of A and 1 of B", ledgersSeen)
	}

	items, last = walk(t, all, st, run.ID, "REV", "P_E2", -1, 10)
	if len(items) != 1 || items[0].LedgerID != lb || last.HWM != 1 || !last.CumulativeSum.Equal(decimal.RequireFromString("50")) {
		t.Errorf("entity B column: %+v, hwm %d, sum %s; want B's line of 50 at HWM 1", items, last.HWM, last.CumulativeSum)
	}
	items, last = walk(t, all, st, run.ID, "REV", "P_E1", 0, 10)
	if len(items) != 2 || last.HWM != 2 || !last.CumulativeSum.Equal(decimal.RequireFromString("130")) {
		t.Errorf("entity A column key 0: %d lines, hwm %d, sum %s; want 2 lines of 130 at HWM 2", len(items), last.HWM, last.CumulativeSum)
	}
}

// anyCode is the catalog code of a Problem or of a domain error.
func anyCode(err error) string {
	var d *domain.Error
	if errors.As(err, &d) {
		return d.Code
	}
	return problemCode(err)
}
