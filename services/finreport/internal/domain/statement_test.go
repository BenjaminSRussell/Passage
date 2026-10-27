package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func d(v int64) decimal.Decimal { return decimal.New(v, 0) }

func cellAt(t *testing.T, res Result, row, col string) Cell {
	t.Helper()
	for _, c := range res.Cells {
		if c.Row == row && c.Column == col {
			return c
		}
	}
	t.Fatalf("no cell %s x %s", row, col)
	return Cell{}
}

func quarterOf(no int) int { return (no-1)/3 + 1 }

func calendarMonths(t *testing.T, n int) Calendar {
	t.Helper()
	var ps []Period
	for no := 1; no <= n; no++ {
		ps = append(ps, Period{Year: 2026, No: no, Quarter: quarterOf(no)})
	}
	cal, err := NewCalendar(ps)
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

func TestEvaluate_BalanceSheetTiesAndIsCumulative(t *testing.T) {
	def := Definition{Code: "BS-TEST", Name: "Balance sheet", Kind: KindBalanceSheet,
		Rows: []Row{
			{Code: "A", Kind: RowAccountRange, Label: "Assets", AccountFrom: "100000", AccountTo: "199999", NormalBalance: "debit"},
			{Code: "E", Kind: RowAccountRange, Label: "Equity", AccountFrom: "300000", AccountTo: "399999", NormalBalance: "credit"},
			{Code: "DIFF", Kind: RowCalculation, Label: "Assets less equity", Expression: "A-E", NormalBalance: "debit"},
		},
		Columns: []Column{{Code: "C1", Label: "Period", PeriodExpr: "P", CurrencyType: "rpt"}},
	}
	bals := []Balance{
		{LedgerID: "L1", Period: 0, Account: "100100", Combination: "CC100", Dims: map[string]string{"cost_center": "CC100"}, CurrencyType: "rpt", Net: d(500)},
		{LedgerID: "L1", Period: 0, Account: "300100", Combination: "CC100", Dims: map[string]string{"cost_center": "CC100"}, CurrencyType: "rpt", Net: d(-600)},
		{LedgerID: "L1", Period: 1, Account: "100100", Combination: "CC100", Dims: map[string]string{"cost_center": "CC100"}, CurrencyType: "rpt", Net: d(100)},
		{LedgerID: "L2", Period: 1, Account: "100100", Combination: "CC100", CurrencyType: "rpt", Net: d(999)},
	}
	res, err := Evaluate(Input{Definition: def, LedgerID: "L1", Calendar: calendarMonths(t, 3), At: 1, Balances: bals})
	if err != nil {
		t.Fatal(err)
	}
	assets := cellAt(t, res, "A", "C1")
	equity := cellAt(t, res, "E", "C1")
	if !assets.Value.Equal(d(600)) || !equity.Value.Equal(d(600)) {
		t.Fatalf("assets %s, equity %s; want 600 and 600 (cumulative, other ledger excluded)", assets.Value, equity.Value)
	}
	if diff := cellAt(t, res, "DIFF", "C1"); !diff.Value.IsZero() || diff.Blank {
		t.Fatalf("assets less equity = %+v, want 0", diff)
	}
	if err := assets.CheckDrill(); err != nil {
		t.Fatal(err)
	}
	if len(assets.Keys) != 2 || assets.Keys[0].Period != "2026-01" || assets.Keys[1].Period != "2026-02" {
		t.Fatalf("asset keys = %+v, want periods 2026-01 and 2026-02", assets.Keys)
	}
}

func TestEvaluate_IncomeStatementExcludesClosingAndIsPeriodOnly(t *testing.T) {
	cc := func(c string) map[string]string { return map[string]string{"cost_center": c} }
	def := Definition{Code: "PL-TEST", Name: "Income statement", Kind: KindIncomeStatement,
		Rows: []Row{
			{Code: "REV", Kind: RowAccountRange, Label: "Revenue", AccountFrom: "400000", AccountTo: "499999", NormalBalance: "credit"},
			{Code: "EXP", Kind: RowAccountRange, Label: "Expenses", AccountFrom: "500000", AccountTo: "599999", NormalBalance: "debit"},
			{Code: "CC2", Kind: RowDimensionFilter, Label: "Cost center 200", DimensionFilter: map[string][]string{"cost_center": {"CC200"}}, NormalBalance: "debit"},
			{Code: "RES", Kind: RowCalculation, Label: "Result", Expression: "REV-EXP", NormalBalance: "debit"},
		},
		Columns: []Column{
			{Code: "C1", Label: "Period", PeriodExpr: "P", CurrencyType: "rpt"},
			{Code: "C2", Label: "Year to date", PeriodExpr: "YTD", CurrencyType: "rpt"},
			{Code: "C3", Label: "Prior year", PeriodExpr: "PY", CurrencyType: "rpt"},
			{Code: "C4", Label: "Change", PeriodExpr: "CALC", CurrencyType: "rpt", Expression: "C1-C3"},
		},
	}
	bals := []Balance{
		{LedgerID: "L1", Period: 0, Account: "400100", Combination: "CC100", Dims: cc("CC100"), CurrencyType: "rpt", Net: d(-100)},
		{LedgerID: "L1", Period: 1, Account: "400100", Combination: "CC100", Dims: cc("CC100"), CurrencyType: "rpt", Net: d(-250), ClosingNet: d(-50)},
		{LedgerID: "L1", Period: 1, Account: "500100", Combination: "CC200", Dims: cc("CC200"), CurrencyType: "rpt", Net: d(120)},
	}
	res, err := Evaluate(Input{Definition: def, LedgerID: "L1", Calendar: calendarMonths(t, 2), At: 1, Balances: bals})
	if err != nil {
		t.Fatal(err)
	}
	checks := []struct {
		row, col string
		want     decimal.Decimal
		blank    bool
	}{
		{"REV", "C1", d(200), false},
		{"REV", "C2", d(300), false},
		{"EXP", "C1", d(120), false},
		{"CC2", "C1", d(120), false},
		{"RES", "C1", d(80), false},
		{"REV", "C3", decimal.Zero, true},
		{"RES", "C3", decimal.Zero, true},
		{"RES", "C4", decimal.Zero, true},
	}
	for _, c := range checks {
		got := cellAt(t, res, c.row, c.col)
		if got.Blank != c.blank || (!c.blank && !got.Value.Equal(c.want)) {
			t.Errorf("%s x %s = %+v, want %s blank=%v", c.row, c.col, got, c.want, c.blank)
		}
	}
	if note := cellAt(t, res, "REV", "C3").Note; note != CodePYMissing {
		t.Fatalf("missing prior year note = %q, want %s", note, CodePYMissing)
	}
	if err := cellAt(t, res, "EXP", "C1").CheckDrill(); err != nil {
		t.Fatal(err)
	}
}

func TestEvaluate_DivisionByZeroIsBlankAndHashIsReproducible(t *testing.T) {
	def := Definition{Code: "RATIO", Name: "Ratio", Kind: KindCustom,
		Rows: []Row{
			{Code: "A", Kind: RowAccountRange, Label: "A", AccountFrom: "100000", AccountTo: "199999", NormalBalance: "debit"},
			{Code: "Z", Kind: RowAccountRange, Label: "Z", AccountFrom: "900000", AccountTo: "999999", NormalBalance: "debit"},
			{Code: "Q", Kind: RowCalculation, Label: "Ratio", Expression: "A/Z", NormalBalance: "debit"},
		},
		Columns: []Column{{Code: "C1", Label: "P", PeriodExpr: "P", CurrencyType: "rpt"}},
	}
	bals := []Balance{{LedgerID: "L1", Period: 0, Account: "100100", Combination: "x", CurrencyType: "rpt", Net: d(10)}}
	in := Input{Definition: def, LedgerID: "L1", Calendar: calendarMonths(t, 1), At: 0, Balances: bals}
	first, err := Evaluate(in)
	if err != nil {
		t.Fatal(err)
	}
	if q := cellAt(t, first, "Q", "C1"); !q.Blank {
		t.Fatalf("A/Z with Z = 0 must be blank, got %+v", q)
	}
	second, _ := Evaluate(in)
	if first.Hash != second.Hash {
		t.Fatal("same inputs must give the same result hash")
	}
	in.Balances = append(in.Balances, Balance{LedgerID: "L1", Period: 0, Account: "100200", Combination: "x", CurrencyType: "rpt", Net: d(1)})
	changed, _ := Evaluate(in)
	if changed.Hash == first.Hash {
		t.Fatal("a different balance must change the hash")
	}
}

// AC3 / BR-15.4 property: for every balance-backed cell of a random ledger,
// the balance keys sum exactly to the cell value.
func TestEvaluate_DrillKeysSumToCellValue(t *testing.T) {
	cal := calendarMonths(t, 6)
	def := Definition{Code: "DRILL", Name: "Drill", Kind: KindIncomeStatement,
		Rows: []Row{
			{Code: "R1", Kind: RowAccountRange, Label: "Low accounts", AccountFrom: "100000", AccountTo: "100004", NormalBalance: "debit"},
			{Code: "R2", Kind: RowAccountRange, Label: "High accounts", AccountFrom: "100005", AccountTo: "100009", NormalBalance: "credit"},
			{Code: "R3", Kind: RowDimensionFilter, Label: "Cost center 100", DimensionFilter: map[string][]string{"cost_center": {"CC100"}}, NormalBalance: "debit"},
			{Code: "R4", Kind: RowTotal, Label: "Total", References: []string{"R1", "R2"}, NormalBalance: "debit"},
			{Code: "H", Kind: RowHeader, Label: "Header", NormalBalance: "debit"},
			{Code: "K", Kind: RowCalculation, Label: "Calc", Expression: "R1-R2", NormalBalance: "debit"},
		},
		Columns: []Column{
			{Code: "C1", Label: "P", PeriodExpr: "P", CurrencyType: "rpt"},
			{Code: "C2", Label: "YTD", PeriodExpr: "YTD", CurrencyType: "rpt"},
			{Code: "C3", Label: "P-1", PeriodExpr: "P-1", CurrencyType: "rpt"},
			{Code: "C4", Label: "Change", PeriodExpr: "CALC", CurrencyType: "rpt", Expression: "C1-C3"},
		},
	}
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 60).Draw(t, "n")
		var bals []Balance
		for i := 0; i < n; i++ {
			comb := rapid.SampledFrom([]string{"CC100", "CC200"}).Draw(t, "comb")
			acct := fmt.Sprintf("1000%02d", rapid.IntRange(0, 9).Draw(t, "acct"))
			cents := rapid.Int64Range(-100000, 100000).Draw(t, "cents")
			closing := rapid.Int64Range(-500, 0).Draw(t, "closing")
			bals = append(bals, Balance{
				LedgerID: "L1", Period: rapid.IntRange(0, 5).Draw(t, "period"), Account: acct, Combination: comb,
				Dims: map[string]string{"cost_center": comb}, CurrencyType: "rpt",
				Net: decimal.New(cents, -2), ClosingNet: decimal.New(closing, 0),
			})
		}
		at := rapid.IntRange(0, 5).Draw(t, "at")
		res, err := Evaluate(Input{Definition: def, LedgerID: "L1", Calendar: cal, At: at, Balances: bals})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range res.Cells {
			if c.Row == "K" || c.Column == "C4" {
				continue
			}
			if err := c.CheckDrill(); err != nil {
				t.Fatalf("cell %s x %s: keys %s != value %s: %v", c.Row, c.Column, c.KeySum(), c.Value, err)
			}
		}
	})
}
