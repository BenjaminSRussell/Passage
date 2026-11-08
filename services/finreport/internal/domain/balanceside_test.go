package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

// FR-15.5: the trial balance shows each account as a debit or a credit balance,
// the side of its net over the column. The two totals agree for a balanced
// ledger (DIFF is 0.00) and every cell drills to exactly its balances.
func TestTrialBalanceStd_DebitCreditColumns(t *testing.T) {
	tb := evaluateStd(t, CodeTBStd, false)
	dr, cr := cellAt(t, tb, "TOTAL", "CLOSE_DR"), cellAt(t, tb, "TOTAL", "CLOSE_CR")
	if dr.Blank || cr.Blank || dr.Value.IsZero() || !dr.Value.Equal(cr.Value) {
		t.Fatalf("closing debit %s and credit %s must be equal and non-zero", dr.Value, cr.Value)
	}
	if diff := cellAt(t, tb, "TOTAL", "DIFF"); !diff.Value.IsZero() || !diff.Computed {
		t.Fatalf("DIFF = %+v, want a computed 0.00", diff)
	}
	for _, row := range []string{"ASSETS", "LIAB", "EQUITY", "REVENUE", "EXPENSE", "TOTAL"} {
		for _, col := range []string{"CLOSE_DR", "CLOSE_CR"} {
			c := cellAt(t, tb, row, col)
			if err := c.CheckDrill(); err != nil {
				t.Errorf("%s/%s: %v", row, col, err)
			}
			if c.Value.IsNegative() {
				t.Errorf("%s/%s = %s, a side column is never negative", row, col, c.Value)
			}
		}
	}
	// Opening and closing columns still show the net.
	if got := cellAt(t, tb, "ASSETS", "CLOSE").Value; !got.Equal(cellAt(t, tb, "ASSETS", "CLOSE_DR").Value) {
		t.Errorf("assets closing %s differs from its debit balances %s", got, cellAt(t, tb, "ASSETS", "CLOSE_DR").Value)
	}
}

// An account with two balances of opposite sign is on one side only: the side
// follows its net, and all of its keys go with it.
func TestEvaluate_BalanceSideFollowsAccountNet(t *testing.T) {
	cal := twoYearCalendar(t)
	at := mustIndex(t, cal, 2026, 3)
	mk := func(acct, combo string, net int64) Balance {
		return Balance{LedgerID: "L1", Period: at, Account: acct, Combination: combo, CurrencyType: "acct", Net: d(net)}
	}
	def := Definition{Code: "TB-SIDE", Name: "Sides", Kind: KindTrialBalance,
		Rows: []Row{{Code: "ALL", Kind: RowAccountRange, Label: "All", AccountFrom: "1000", AccountTo: "9999", NormalBalance: "debit"}},
		Columns: []Column{
			{Code: "DR", Label: "Debit", PeriodExpr: "P", CurrencyType: "acct", BalanceSide: BalanceSideDebit},
			{Code: "CR", Label: "Credit", PeriodExpr: "P", CurrencyType: "acct", BalanceSide: BalanceSideCredit},
		}}
	bals := []Balance{mk("1000", "a", 70), mk("1000", "b", -20), mk("2000", "a", -30), mk("2000", "b", 10), mk("3000", "a", 0)}
	res, err := Evaluate(Input{Definition: def, LedgerID: "L1", Calendar: cal, At: at, Balances: bals})
	if err != nil {
		t.Fatal(err)
	}
	dr, cr := cellAt(t, res, "ALL", "DR"), cellAt(t, res, "ALL", "CR")
	if !dr.Value.Equal(d(50)) || len(dr.Keys) != 2 {
		t.Errorf("debit = %s over %d keys, want 50 over 2 (account 1000: 70 and -20)", dr.Value, len(dr.Keys))
	}
	if !cr.Value.Equal(d(20)) || len(cr.Keys) != 2 {
		t.Errorf("credit = %s over %d keys, want 20 over 2 (account 2000: 30 and -10, shown positive)", cr.Value, len(cr.Keys))
	}
	for _, c := range []Cell{dr, cr} {
		if err := c.CheckDrill(); err != nil {
			t.Error(err)
		}
	}
	for _, k := range cr.Keys {
		if !k.Credit {
			t.Errorf("credit-side key %+v must be marked credit so drill level 2 signs its lines", k)
		}
	}
}

func TestBalanceSide_Validation(t *testing.T) {
	base := func(c Column) Definition {
		return Definition{Code: "X-SIDE", Name: "x", Kind: KindTrialBalance,
			Rows:    []Row{{Code: "R", Kind: RowAccountRange, Label: "r", AccountFrom: "1", AccountTo: "9", NormalBalance: "debit"}},
			Columns: []Column{{Code: "A", Label: "a", PeriodExpr: "P"}, c}}
	}
	if issues := base(Column{Code: "B", Label: "b", PeriodExpr: "P", BalanceSide: "credit"}).Validate(); len(issues) != 0 {
		t.Fatalf("a credit side column must validate: %+v", issues)
	}
	for _, c := range []Column{
		{Code: "B", Label: "b", PeriodExpr: "P", BalanceSide: "net"},
		{Code: "B", Label: "b", PeriodExpr: "CALC", Expression: "A", BalanceSide: "debit"},
	} {
		issues := base(c).Validate()
		if len(issues) != 1 || !strings.HasSuffix(issues[0].Field, ".balanceSide") {
			t.Errorf("column %+v: issues %+v, want one on balanceSide", c, issues)
		}
	}
}

// Review (FR-15.5): the side is decided per cell, over the balances the row and
// the column read. Account 1000 is a debit in department A and a credit in B, so
// the department rows put it on opposite sides while the unfiltered row nets
// it; every row balances, a total of rows adds displayed (positive) amounts and
// never nets a debit into a credit, and an account netting to zero is on
// neither side.
func TestEvaluate_BalanceSidePerDimensionRow(t *testing.T) {
	cal := twoYearCalendar(t)
	at := mustIndex(t, cal, 2026, 3)
	mk := func(acct, dept string, net int64) Balance {
		return Balance{LedgerID: "L1", Period: at, Account: acct, Combination: dept, Dims: map[string]string{"dept": dept},
			CurrencyType: "acct", Net: d(net)}
	}
	def := Definition{Code: "TB-DEPT", Name: "By department", Kind: KindTrialBalance,
		Rows: []Row{
			{Code: "DA", Kind: RowDimensionFilter, Label: "A", DimensionFilter: map[string][]string{"dept": {"A"}}, NormalBalance: "debit"},
			{Code: "DB", Kind: RowDimensionFilter, Label: "B", DimensionFilter: map[string][]string{"dept": {"B"}}, NormalBalance: "credit"},
			{Code: "SUM", Kind: RowTotal, Label: "A+B", References: []string{"DA", "DB"}, NormalBalance: "debit"},
			{Code: "ALL", Kind: RowAccountRange, Label: "All", AccountFrom: "1000", AccountTo: "9999", NormalBalance: "debit"},
		},
		Columns: []Column{
			{Code: "DR", Label: "Debit", PeriodExpr: "P", CurrencyType: "acct", BalanceSide: BalanceSideDebit},
			{Code: "CR", Label: "Credit", PeriodExpr: "P", CurrencyType: "acct", BalanceSide: BalanceSideCredit},
			{Code: "DIFF", Label: "Diff", PeriodExpr: "CALC", Expression: "DR-CR"},
		}}
	bals := []Balance{mk("1000", "A", 100), mk("1000", "B", -30), mk("2000", "A", -100), mk("2000", "B", 30),
		mk("3000", "A", 5), mk("3000", "A2", -5)}
	bals[5].Dims = map[string]string{"dept": "A"}
	res, err := Evaluate(Input{Definition: def, LedgerID: "L1", Calendar: cal, At: at, Balances: bals})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][2]int64{"DA": {100, 100}, "DB": {30, 30}, "SUM": {130, 130}, "ALL": {70, 70}}
	for row, w := range want {
		dr, cr, diff := cellAt(t, res, row, "DR"), cellAt(t, res, row, "CR"), cellAt(t, res, row, "DIFF")
		if !dr.Value.Equal(d(w[0])) || !cr.Value.Equal(d(w[1])) || !diff.Value.IsZero() {
			t.Errorf("%s: debit %s credit %s diff %s, want %d / %d / 0", row, dr.Value, cr.Value, diff.Value, w[0], w[1])
		}
		for _, c := range []Cell{dr, cr} {
			if err := c.CheckDrill(); err != nil {
				t.Errorf("%s/%s: %v", row, c.Column, err)
			}
			against := 0
			for _, k := range c.Keys {
				if k.Account == "3000" {
					t.Errorf("%s/%s: account 3000 nets to zero and belongs to neither side, got key %+v", row, c.Column, k)
				}
				if k.Amount.IsNegative() {
					against++
				}
			}
			// Only the unfiltered row nets an account across departments (1000:
			// 100 and -30), so only it has a key against its side.
			if wantAgainst := map[bool]int{true: 1, false: 0}[row == "ALL"]; against != wantAgainst {
				t.Errorf("%s/%s: %d keys against the side, want %d: %+v", row, c.Column, against, wantAgainst, c.Keys)
			}
		}
	}
	// Department B is credit-normal, which changes neither side column.
	if k := cellAt(t, res, "DB", "DR").Keys; len(k) != 1 || k[0].Account != "2000" || k[0].Credit {
		t.Errorf("DB debit keys %+v, want account 2000 unsigned", k)
	}
}

// Review (BR-15.3, BR-15.4): on a credit-normal row the debit side keeps the
// debit sign. Drill level 2 must sign the lines by the side, not the row, and a
// raw: calculation must turn each side back into the stored net.
func TestBalanceSide_CreditNormalRowDrillAndRaw(t *testing.T) {
	cal := twoYearCalendar(t)
	at := mustIndex(t, cal, 2026, 3)
	mk := func(acct string, net int64) Balance {
		return Balance{LedgerID: "L1", Period: at, Account: acct, Combination: "c", CurrencyType: "acct", Net: d(net)}
	}
	def := Definition{Code: "TB-LIAB", Name: "Liabilities", Kind: KindTrialBalance,
		Rows: []Row{
			{Code: "LIAB", Kind: RowAccountRange, Label: "Liabilities", AccountFrom: "2000", AccountTo: "2999", NormalBalance: "credit"},
			{Code: "NET", Kind: RowCalculation, Label: "Stored net", Expression: "raw: LIAB", NormalBalance: "debit"},
		},
		Columns: []Column{
			{Code: "DR", Label: "Debit", PeriodExpr: "P", CurrencyType: "acct", BalanceSide: BalanceSideDebit},
			{Code: "CR", Label: "Credit", PeriodExpr: "P", CurrencyType: "acct", BalanceSide: BalanceSideCredit},
		}}
	res, err := Evaluate(Input{Definition: def, LedgerID: "L1", Calendar: cal, At: at, Balances: []Balance{mk("2000", -90), mk("2100", 15)}})
	if err != nil {
		t.Fatal(err)
	}
	dr, cr := cellAt(t, res, "LIAB", "DR"), cellAt(t, res, "LIAB", "CR")
	if !dr.Value.Equal(d(15)) || !cr.Value.Equal(d(90)) {
		t.Fatalf("debit %s credit %s, want 15 and 90", dr.Value, cr.Value)
	}
	if got := cellAt(t, res, "NET", "DR").Value; !got.Equal(d(15)) {
		t.Errorf("raw: of the debit side = %s, want the stored net 15", got)
	}
	if got := cellAt(t, res, "NET", "CR").Value; !got.Equal(d(-90)) {
		t.Errorf("raw: of the credit side = %s, want the stored net -90", got)
	}
	// The lines under each key net to the stored balance; the scope must sign
	// them so they reconcile to the displayed cell.
	for _, tc := range []struct {
		cell Cell
		net  int64
	}{{dr, 15}, {cr, -90}} {
		s, err := NewLineScope(def, tc.cell, 1, -1)
		if err != nil {
			t.Fatal(err)
		}
		if rec := s.Reconcile([]decimal.Decimal{d(tc.net)}); !rec.Reconciled() {
			t.Errorf("%s drill level 2 does not reconcile: %+v", tc.cell.Column, rec)
		}
	}
}

func TestBalanceSide_RefusedOnCashFlow(t *testing.T) {
	def := Definition{Code: "CF-SIDE", Name: "x", Kind: KindCashFlow,
		Rows:    []Row{{Code: "R", Kind: RowAccountRange, Label: "r", AccountFrom: "1", AccountTo: "9", NormalBalance: "debit"}},
		Columns: []Column{{Code: "A", Label: "a", PeriodExpr: "P", BalanceSide: BalanceSideDebit}}}
	issues := def.Validate()
	if len(issues) != 1 || issues[0].Field != "columns[0].balanceSide" {
		t.Fatalf("issues %+v, want one on columns[0].balanceSide", issues)
	}
}
