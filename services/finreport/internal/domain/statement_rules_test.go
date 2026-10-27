package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

// twoYearCalendar is fiscal 2025 and 2026, each with an opening period 0 that
// carries the brought-forward balances and twelve monthly periods.
func twoYearCalendar(t *testing.T) Calendar {
	t.Helper()
	var ps []Period
	for _, y := range []int{2025, 2026} {
		ps = append(ps, Period{Year: y, No: 0, Quarter: 1})
		for no := 1; no <= 12; no++ {
			ps = append(ps, Period{Year: y, No: no, Quarter: quarterOf(no)})
		}
	}
	cal, err := NewCalendar(ps)
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

func mustIndex(t *testing.T, cal Calendar, year, no int) int {
	t.Helper()
	i, ok := cal.Index(year, no)
	if !ok {
		t.Fatalf("no period %d-%d", year, no)
	}
	return i
}

// BR-15.2: a balance sheet is cumulative from the opening period of the
// column's fiscal year, so the brought-forward balance in 2026-00 is not added
// on top of the 2025 movements it already contains.
func TestEvaluate_BalanceSheetStartsAtFiscalYearOpening(t *testing.T) {
	cal := twoYearCalendar(t)
	def := Definition{Code: "BS-TEST", Name: "Balance sheet", Kind: KindBalanceSheet,
		Rows: []Row{{Code: "CASH", Kind: RowAccountRange, Label: "Cash", AccountFrom: "1000", AccountTo: "1999", NormalBalance: "debit"}},
		Columns: []Column{
			{Code: "CUR", Label: "Current", PeriodExpr: "P", CurrencyType: "acct"},
			{Code: "PY", Label: "Prior year", PeriodExpr: "PY", CurrencyType: "acct"},
		},
	}
	bal := func(year, no int, net int64) Balance {
		return Balance{LedgerID: "L1", Period: mustIndex(t, cal, year, no), Account: "1000", Combination: "-", CurrencyType: "acct", Net: d(net)}
	}
	bals := []Balance{
		bal(2025, 0, 100), // opening 2025
		bal(2025, 3, 50),  // 2025 movement up to March
		bal(2025, 9, 25),  // 2025 movement after March
		bal(2026, 0, 175), // brought forward = 100 + 50 + 25
		bal(2026, 2, 10),
		bal(2026, 4, 999), // after the run period
	}
	res, err := Evaluate(Input{Definition: def, LedgerID: "L1", Calendar: cal, At: mustIndex(t, cal, 2026, 3), Balances: bals})
	if err != nil {
		t.Fatal(err)
	}
	if got := cellAt(t, res, "CASH", "CUR"); !got.Value.Equal(d(185)) {
		t.Fatalf("current = %s, want 185 (175 opening + 10)", got.Value)
	}
	if got := cellAt(t, res, "CASH", "PY"); !got.Value.Equal(d(150)) {
		t.Fatalf("prior year = %s, want 150 (100 opening + 50 through March 2025)", got.Value)
	}
	for _, c := range res.Cells {
		if err := c.CheckDrill(); err != nil {
			t.Fatalf("%s/%s: %v", c.Row, c.Column, err)
		}
	}
}

// Calculation rows and CALC columns hold derived values with no balance keys;
// the drill invariant must accept them, or every run with a non-zero
// calculation would fail.
func TestEvaluate_ComputedCellsPassDrillCheck(t *testing.T) {
	def := Definition{Code: "PL-TEST", Name: "Margin", Kind: KindIncomeStatement,
		Rows: []Row{
			{Code: "REV", Kind: RowAccountRange, Label: "Revenue", AccountFrom: "4000", AccountTo: "4999", NormalBalance: "credit"},
			{Code: "COGS", Kind: RowAccountRange, Label: "Cost of sales", AccountFrom: "5000", AccountTo: "5999", NormalBalance: "debit"},
			{Code: "GM", Kind: RowCalculation, Label: "Gross margin", Expression: "REV-COGS", NormalBalance: "credit"},
		},
		Columns: []Column{
			{Code: "C1", Label: "This month", PeriodExpr: "P", CurrencyType: "acct"},
			{Code: "C2", Label: "Last month", PeriodExpr: "P-1", CurrencyType: "acct"},
			{Code: "VAR", Label: "Variance", PeriodExpr: "CALC", Expression: "C1-C2"},
		},
	}
	bals := []Balance{
		{LedgerID: "L1", Period: 0, Account: "4000", Combination: "-", CurrencyType: "acct", Net: d(-800)},
		{LedgerID: "L1", Period: 1, Account: "4000", Combination: "-", CurrencyType: "acct", Net: d(-1000)},
		{LedgerID: "L1", Period: 1, Account: "5000", Combination: "-", CurrencyType: "acct", Net: d(600)},
	}
	res, err := Evaluate(Input{Definition: def, LedgerID: "L1", Calendar: calendarMonths(t, 2), At: 1, Balances: bals})
	if err != nil {
		t.Fatal(err)
	}
	gm := cellAt(t, res, "GM", "C1")
	if !gm.Value.Equal(d(400)) || !gm.Computed || len(gm.Keys) != 0 {
		t.Fatalf("gross margin = %+v, want computed 400 without keys", gm)
	}
	variance := cellAt(t, res, "REV", "VAR")
	if !variance.Value.Equal(d(200)) || !variance.Computed {
		t.Fatalf("revenue variance = %+v, want computed 200", variance)
	}
	if rev := cellAt(t, res, "REV", "C1"); rev.Computed {
		t.Fatal("a balance-backed cell must not be marked computed")
	}
	for _, c := range res.Cells {
		if err := c.CheckDrill(); err != nil {
			t.Fatalf("%s/%s: %v", c.Row, c.Column, err)
		}
	}
	bad := gm
	bad.Keys = []Key{{Account: "4000", Amount: d(1)}}
	if bad.CheckDrill() == nil {
		t.Fatal("a computed cell with balance keys must fail the drill check")
	}
}

// BR-15.3: raw: calculations use stored net (debit - credit), not displayed values.
func TestEvaluate_RawCalculationUsesStoredNet(t *testing.T) {
	def := Definition{Code: "RAW-TEST", Name: "Raw", Kind: KindCustom,
		Rows: []Row{
			{Code: "REV", Kind: RowAccountRange, Label: "Revenue", AccountFrom: "4000", AccountTo: "4999", NormalBalance: "credit"},
			{Code: "EXP", Kind: RowAccountRange, Label: "Expenses", AccountFrom: "5000", AccountTo: "5999", NormalBalance: "debit"},
			{Code: "SHOWN", Kind: RowCalculation, Label: "Displayed sum", Expression: "REV+EXP", NormalBalance: "debit"},
			{Code: "NET", Kind: RowCalculation, Label: "Net sum", Expression: "raw: REV+EXP", NormalBalance: "debit"},
		},
		Columns: []Column{{Code: "C1", Label: "P", PeriodExpr: "P"}},
	}
	if issues := def.Validate(); len(issues) != 0 {
		t.Fatalf("raw: expression must validate, got %v", issues)
	}
	bals := []Balance{
		{LedgerID: "L1", Period: 0, Account: "4000", Combination: "-", CurrencyType: "rpt", Net: d(-1000)},
		{LedgerID: "L1", Period: 0, Account: "5000", Combination: "-", CurrencyType: "rpt", Net: d(300)},
	}
	res, err := Evaluate(Input{Definition: def, LedgerID: "L1", CurrencyType: "rpt", Calendar: calendarMonths(t, 1), At: 0, Balances: bals})
	if err != nil {
		t.Fatal(err)
	}
	if got := cellAt(t, res, "SHOWN", "C1").Value; !got.Equal(d(1300)) {
		t.Fatalf("displayed sum = %s, want 1300", got)
	}
	if got := cellAt(t, res, "NET", "C1").Value; !got.Equal(d(-700)) {
		t.Fatalf("raw sum = %s, want -700", got)
	}
}

// A column without a currency type follows the run; a column that names one
// reads that currency type only.
func TestEvaluate_ColumnCurrencyType(t *testing.T) {
	def := Definition{Code: "CUR-TEST", Name: "Currencies", Kind: KindCustom,
		Rows: []Row{{Code: "A", Kind: RowAccountRange, Label: "A", AccountFrom: "1000", AccountTo: "1000", NormalBalance: "debit"}},
		Columns: []Column{
			{Code: "RUN", Label: "Run currency", PeriodExpr: "P"},
			{Code: "TXN", Label: "Transaction", PeriodExpr: "P", CurrencyType: "txn"},
		},
	}
	bals := []Balance{
		{LedgerID: "L1", Period: 0, Account: "1000", Combination: "-", CurrencyType: "acct", Net: decimal.RequireFromString("12.34")},
		{LedgerID: "L1", Period: 0, Account: "1000", Combination: "-", CurrencyType: "txn", Net: decimal.RequireFromString("56.78")},
	}
	res, err := Evaluate(Input{Definition: def, LedgerID: "L1", CurrencyType: "acct", Calendar: calendarMonths(t, 1), At: 0, Balances: bals})
	if err != nil {
		t.Fatal(err)
	}
	if got := cellAt(t, res, "A", "RUN").Value.String(); got != "12.34" {
		t.Fatalf("run-currency column = %s, want 12.34", got)
	}
	if got := cellAt(t, res, "A", "TXN").Value.String(); got != "56.78" {
		t.Fatalf("txn column = %s, want 56.78", got)
	}
	if got := def.CurrencyTypes("acct"); len(got) != 2 || got[0] != "acct" || got[1] != "txn" {
		t.Fatalf("CurrencyTypes = %v, want [acct txn]", got)
	}
}

// BR-15.1: an adjustment period is skipped by YTD unless the definition or the
// run asks for adjustment periods.
func TestEvaluate_RunCanIncludeAdjustmentPeriods(t *testing.T) {
	cal, err := NewCalendar([]Period{
		{Year: 2026, No: 1, Quarter: 1},
		{Year: 2026, No: 2, Quarter: 1, Adjustment: true},
		{Year: 2026, No: 3, Quarter: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	def := Definition{Code: "YTD-TEST", Name: "YTD", Kind: KindIncomeStatement,
		Rows:    []Row{{Code: "EXP", Kind: RowAccountRange, Label: "Expenses", AccountFrom: "5000", AccountTo: "5999", NormalBalance: "debit"}},
		Columns: []Column{{Code: "YTD", Label: "Year to date", PeriodExpr: "YTD", CurrencyType: "acct"}},
	}
	bals := []Balance{
		{LedgerID: "L1", Period: 0, Account: "5000", Combination: "-", CurrencyType: "acct", Net: d(10)},
		{LedgerID: "L1", Period: 1, Account: "5000", Combination: "-", CurrencyType: "acct", Net: d(5)},
		{LedgerID: "L1", Period: 2, Account: "5000", Combination: "-", CurrencyType: "acct", Net: d(20)},
	}
	in := Input{Definition: def, LedgerID: "L1", Calendar: cal, At: 2, Balances: bals}
	res, err := Evaluate(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := cellAt(t, res, "EXP", "YTD").Value; !got.Equal(d(30)) {
		t.Fatalf("YTD without adjustment periods = %s, want 30", got)
	}
	in.IncludeAdjustmentPeriods = true
	if res, err = Evaluate(in); err != nil {
		t.Fatal(err)
	}
	if got := cellAt(t, res, "EXP", "YTD").Value; !got.Equal(d(35)) {
		t.Fatalf("YTD with adjustment periods = %s, want 35", got)
	}
}
