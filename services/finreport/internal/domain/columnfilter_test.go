package domain

import (
	"strings"
	"testing"
)

func filterFixture(t *testing.T) (Calendar, []Balance) {
	t.Helper()
	cal := twoYearCalendar(t)
	at := mustIndex(t, cal, 2026, 3)
	mk := func(acct, pc, combo string, net int64) Balance {
		return Balance{LedgerID: "L1", Period: at, Account: acct, Combination: combo, CurrencyType: "acct",
			Dims: map[string]string{"profit_center": pc}, Net: d(net)}
	}
	return cal, []Balance{
		mk("4000", "PC100", "a", 10),
		mk("4000", "PC200", "b", 20),
		mk("4000", "PC300", "c", 40),
		mk("4000", "", "d", 80),
	}
}

// FR-15.3: a column with a dimension filter reads only the balances whose
// dimension values match; each cell still drills to exactly its balances.
func TestEvaluate_ColumnDimensionFilter(t *testing.T) {
	cal, bals := filterFixture(t)
	def := Definition{Code: "PL-PC", Name: "By profit center", Kind: KindIncomeStatement,
		Rows: []Row{{Code: "REV", Kind: RowAccountRange, Label: "Revenue", AccountFrom: "4000", AccountTo: "4999", NormalBalance: "credit"}},
		Columns: []Column{
			{Code: "ALL", Label: "All", PeriodExpr: "P", CurrencyType: "acct"},
			{Code: "PC100", Label: "PC100", PeriodExpr: "P", CurrencyType: "acct", DimensionFilter: map[string][]string{"profit_center": {"PC100"}}},
			{Code: "PC12", Label: "PC100 and 200", PeriodExpr: "P", CurrencyType: "acct", DimensionFilter: map[string][]string{"profit_center": {"PC100", "PC200"}}},
			{Code: "ANY", Label: "Any value", PeriodExpr: "P", CurrencyType: "acct", DimensionFilter: map[string][]string{"profit_center": {"*"}}},
			{Code: "NONE", Label: "No value", PeriodExpr: "P", CurrencyType: "acct", DimensionFilter: map[string][]string{"profit_center": {""}}},
			{Code: "GONE", Label: "No match", PeriodExpr: "P", CurrencyType: "acct", DimensionFilter: map[string][]string{"region": {"EU"}}},
			{Code: "SHARE", Label: "PC100 share", PeriodExpr: "CALC", Expression: "PC100/ALL*100"},
		},
	}
	res, err := Evaluate(Input{Definition: def, LedgerID: "L1", Calendar: cal, At: mustIndex(t, cal, 2026, 3), Balances: bals})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"ALL": -150, "PC100": -10, "PC12": -30, "ANY": -150, "NONE": -80, "GONE": 0}
	// Credit rows show -net; "*" matches every balance, also one without a value, as in a row filter.
	for col, w := range want {
		got := cellAt(t, res, "REV", col)
		if !got.Value.Equal(d(w)) {
			t.Errorf("%s = %s, want %d", col, got.Value, w)
		}
		if err := got.CheckDrill(); err != nil {
			t.Errorf("%s: %v", col, err)
		}
	}
	if n := len(cellAt(t, res, "REV", "PC12").Keys); n != 2 {
		t.Errorf("PC12 drills to %d balances, want 2", n)
	}
	if got := cellAt(t, res, "REV", "SHARE"); got.Value.Round(2).String() != "6.67" {
		t.Errorf("share = %s, want 6.67", got.Value)
	}
}

func TestColumnDimensionFilter_Validation(t *testing.T) {
	base := func(c Column, kind Kind) Definition {
		return Definition{Code: "X-TEST", Name: "x", Kind: kind,
			Rows:    []Row{{Code: "R", Kind: RowAccountRange, Label: "r", AccountFrom: "1", AccountTo: "9", NormalBalance: "debit"}},
			Columns: []Column{{Code: "A", Label: "a", PeriodExpr: "P"}, c}}
	}
	ok := Column{Code: "B", Label: "b", PeriodExpr: "P", DimensionFilter: map[string][]string{"cost_center": {"C1"}}}
	if issues := base(ok, KindCustom).Validate(); len(issues) != 0 {
		t.Fatalf("valid filter refused: %v", issues)
	}
	cases := map[string]Definition{
		"empty values":  base(Column{Code: "B", Label: "b", PeriodExpr: "P", DimensionFilter: map[string][]string{"cost_center": {}}}, KindCustom),
		"bad attribute": base(Column{Code: "B", Label: "b", PeriodExpr: "P", DimensionFilter: map[string][]string{"cost center": {"1"}}}, KindCustom),
		"calc column":   base(Column{Code: "B", Label: "b", PeriodExpr: "CALC", Expression: "A", DimensionFilter: map[string][]string{"cost_center": {"1"}}}, KindCustom),
		"cash flow":     base(ok, KindCashFlow),
	}
	for name, def := range cases {
		issues := def.Validate()
		found := false
		for _, is := range issues {
			if strings.Contains(is.Field, "dimensionFilter") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no dimensionFilter issue in %v", name, issues)
		}
	}
}

// A saved column set carries its columns' filters into a composed definition,
// is checked like a definition's columns, and cannot bring a filter into a cash
// flow definition.
func TestColumnDimensionFilter_ColumnSets(t *testing.T) {
	cols := ColumnSet{Code: "CS-PC", Name: "Per profit center", Columns: []Column{
		{Code: "C1", Label: "PC100", PeriodExpr: "P", DimensionFilter: map[string][]string{"profit_center": {"PC100"}}},
		{Code: "C2", Label: "Prior year PC100", PeriodExpr: "PY", DimensionFilter: map[string][]string{"profit_center": {"PC100"}}},
		{Code: "C3", Label: "Variance", PeriodExpr: "CALC", Expression: "C1-C2"},
	}}
	if issues := cols.Validate(); len(issues) != 0 {
		t.Fatalf("column set refused: %v", issues)
	}
	rows := RowSet{Code: "RS-REV", Name: "Revenue", Rows: []Row{{Code: "REV", Kind: RowAccountRange, Label: "Revenue", AccountFrom: "4000", AccountTo: "4999", NormalBalance: "credit"}}}
	def, issues := Compose("PL-PC", "P&L PC100", KindIncomeStatement, false, rows, cols)
	if len(issues) != 0 || def.Columns[1].DimensionFilter["profit_center"][0] != "PC100" {
		t.Fatalf("composed = %+v, %v", def.Columns, issues)
	}
	_, issues = Compose("CF-PC", "Cash flow", KindCashFlow, false, rows, cols)
	refused := false
	for _, is := range issues {
		refused = refused || strings.HasSuffix(is.Field, "dimensionFilter")
	}
	if !refused {
		t.Fatalf("a filtered column set composed into a cash flow definition: %v", issues)
	}
	cols.Columns[2].DimensionFilter = map[string][]string{"profit_center": {"PC100"}}
	if issues := cols.Validate(); len(issues) == 0 || !strings.Contains(issues[0].Field, "dimensionFilter") {
		t.Fatalf("a filter on a CALC column in a set: %v", issues)
	}
}
