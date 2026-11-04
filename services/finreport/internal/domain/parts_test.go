package domain

import (
	"strings"
	"testing"
)

func TestColumnTemplates_AreValidAndComparative(t *testing.T) {
	templates := ColumnTemplates()
	if len(templates) != 4 {
		t.Fatalf("templates = %d, want 4", len(templates))
	}
	for _, tpl := range templates {
		if issues := tpl.Validate(); len(issues) > 0 {
			t.Errorf("%s is invalid: %+v", tpl.Code, issues)
		}
	}
	full := templates[0]
	if full.Code != "CMP-FULL" || len(full.Columns) != 8 {
		t.Fatalf("first template %s has %d columns, want CMP-FULL with 8 (sorted by code)", full.Code, len(full.Columns))
	}
	if got := full.Columns[6].Expression; got != "C3-C4" {
		t.Fatalf("YTD variance = %q, want C3-C4", got)
	}
}

func TestComparativeTemplate_EvaluatesVarianceAndBlankPercentOnZero(t *testing.T) {
	var py ColumnSet
	for _, tpl := range ColumnTemplates() {
		if tpl.Code == "CMP-PY" {
			py = tpl
		}
	}
	rows := RowSet{Code: "REV", Name: "Revenue", Rows: []Row{
		{Code: "R", Kind: RowAccountRange, Label: "Revenue", AccountFrom: "400000", AccountTo: "499999", NormalBalance: "credit"},
		{Code: "N", Kind: RowAccountRange, Label: "New line", AccountFrom: "500000", AccountTo: "599999", NormalBalance: "credit"},
	}}
	def, issues := Compose("REV-CMP", "Revenue comparison", KindIncomeStatement, false, rows, py)
	if len(issues) > 0 {
		t.Fatalf("composed definition invalid: %+v", issues)
	}
	var ps []Period
	for _, y := range []int{2025, 2026} {
		for no := 1; no <= 3; no++ {
			ps = append(ps, Period{Year: y, No: no, Quarter: 1})
		}
	}
	cal, err := NewCalendar(ps)
	if err != nil {
		t.Fatal(err)
	}
	bal := func(p int, acct string, net int64) Balance {
		return Balance{LedgerID: "L1", Period: p, Account: acct, Combination: "X", CurrencyType: "acct", Net: d(net)}
	}
	// Indexes 0-2 are 2025-01..03, 3-5 are 2026-01..03: credit balances are negative net.
	bals := []Balance{bal(1, "400100", -100), bal(4, "400100", -150), bal(4, "500100", -30)}
	res, err := Evaluate(Input{Definition: def, LedgerID: "L1", Calendar: cal, At: 4, Balances: bals, CurrencyType: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	if v := cellAt(t, res, "R", "C3"); !v.Value.Equal(d(50)) {
		t.Fatalf("variance = %s, want 50", v.Value)
	}
	if p := cellAt(t, res, "R", "C4"); !p.Value.Equal(d(50)) {
		t.Fatalf("variance %% = %s, want 50", p.Value)
	}
	if p := cellAt(t, res, "N", "C4"); !p.Blank {
		t.Fatalf("percent over a zero prior year = %+v, want blank", p)
	}
}

func TestRowSetValidate_ChecksRowsOnly(t *testing.T) {
	ok := RowSet{Code: "ROWS-1", Name: "Rows", Rows: []Row{
		{Code: "A", Kind: RowAccountRange, Label: "A", AccountFrom: "1", AccountTo: "2", NormalBalance: "debit"},
		{Code: "T", Kind: RowTotal, Label: "T", References: []string{"A"}, NormalBalance: "debit"},
	}}
	if issues := ok.Validate(); len(issues) > 0 {
		t.Fatalf("valid row set rejected: %+v", issues)
	}
	bad := RowSet{Code: "bad", Name: "", Rows: []Row{{Code: "T", Kind: RowTotal, Label: "T", References: []string{"MISSING"}, NormalBalance: "debit"}}}
	fields := map[string]bool{}
	for _, is := range bad.Validate() {
		fields[is.Field] = true
	}
	if !fields["code"] || !fields["name"] || !fields["rows.T"] {
		t.Fatalf("issues on %v, want code, name and rows.T", fields)
	}
	if issues := (RowSet{Code: "EMPTY", Name: "Empty"}).Validate(); len(issues) != 1 || issues[0].Field != "rows" || issues[0].Code != CodeTooLarge {
		t.Fatalf("empty row set issues = %+v, want one rows issue", issues)
	}
}

func TestColumnSetValidate_ChecksColumnsOnly(t *testing.T) {
	bad := ColumnSet{Code: "COLS", Name: "Cols", Columns: []Column{
		{Code: "C1", Label: "P", PeriodExpr: "P"},
		{Code: "C2", Label: "Var", PeriodExpr: "CALC", Expression: "C1-C9"},
		{Code: "C3", Label: "Bad", PeriodExpr: "X9"},
	}}
	issues := bad.Validate()
	if len(issues) != 2 {
		t.Fatalf("issues = %+v, want the unknown reference and the unsupported period", issues)
	}
	long := ColumnSet{Code: "COLS", Name: "Cols", Description: strings.Repeat("x", 501), Columns: []Column{{Code: "C1", Label: "P", PeriodExpr: "P"}}}
	if is := long.Validate(); len(is) != 1 || is[0].Field != "description" || is[0].Code != CodePartInvalid {
		t.Fatalf("long description issues = %+v, want one description issue", is)
	}
	for _, is := range issues {
		if !strings.HasPrefix(is.Field, "columns[") {
			t.Fatalf("issue on %q, want a column field", is.Field)
		}
	}
}

func TestCompose_CopiesPartsAndReportsDefinitionIssues(t *testing.T) {
	rows := RowSet{Code: "R", Name: "R", Rows: []Row{{Code: "A", Kind: RowAccountRange, Label: "A", AccountFrom: "1", AccountTo: "2", NormalBalance: "debit"}}}
	cols := ColumnTemplates()[0]
	def, issues := Compose("NEW-DEF", "New", KindCustom, true, rows, cols)
	if len(issues) > 0 || def.Status != "draft" || !def.IncludeAdjustmentPeriods || def.SchemaVersion != SchemaVersion {
		t.Fatalf("def %+v issues %+v", def, issues)
	}
	def.Rows[0].Label = "changed"
	if rows.Rows[0].Label != "A" {
		t.Fatal("Compose must copy the rows, not share them with the set")
	}
	if _, issues := Compose("bad code", "New", KindCustom, false, rows, cols); len(issues) == 0 {
		t.Fatal("a bad definition code must be reported")
	}
}

func shippedTemplate(t *testing.T, code string) ColumnSet {
	t.Helper()
	for _, tpl := range ColumnTemplates() {
		if tpl.Code == code {
			return tpl
		}
	}
	t.Fatalf("no template %s", code)
	return ColumnSet{}
}

// The variance and percent work on displayed values (BR-15.3): a debit-normal
// expense row that grows shows a positive variance and percent (higher, not
// favourable). The percent divides by the comparative as shown, so a comparative
// below zero turns the percent's sign around; the expression language has no
// ABS, and the templates say so in their description.
func TestComparativeTemplate_PercentFollowsTheSignOfTheComparative(t *testing.T) {
	rows := RowSet{Code: "PL", Name: "P&L", Rows: []Row{
		{Code: "REV", Kind: RowAccountRange, Label: "Revenue", AccountFrom: "400000", AccountTo: "499999", NormalBalance: "credit"},
		{Code: "EXP", Kind: RowAccountRange, Label: "Expenses", AccountFrom: "600000", AccountTo: "699999", NormalBalance: "debit"},
	}}
	def, issues := Compose("PL-CMP", "P&L comparison", KindIncomeStatement, false, rows, shippedTemplate(t, "CMP-PY"))
	if len(issues) > 0 {
		t.Fatalf("composed definition invalid: %+v", issues)
	}
	cal, err := NewCalendar([]Period{{Year: 2025, No: 1, Quarter: 1}, {Year: 2026, No: 1, Quarter: 1}})
	if err != nil {
		t.Fatal(err)
	}
	bal := func(p int, acct string, net int64) Balance {
		return Balance{LedgerID: "L1", Period: p, Account: acct, Combination: "X", CurrencyType: "acct", Net: d(net)}
	}
	// Revenue: a debit (negative) prior year of 50 shown as -50, then 100 of revenue.
	// Expenses: 100 then 120.
	bals := []Balance{bal(0, "400100", 50), bal(1, "400100", -100), bal(0, "600100", 100), bal(1, "600100", 120)}
	res, err := Evaluate(Input{Definition: def, LedgerID: "L1", Calendar: cal, At: 1, Balances: bals, CurrencyType: "acct"})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		row, col string
		want     int64
	}{
		{"EXP", "C3", 20}, {"EXP", "C4", 20},
		{"REV", "C3", 150}, {"REV", "C4", -300},
	} {
		if v := cellAt(t, res, c.row, c.col); v.Blank || !v.Value.Equal(d(c.want)) {
			t.Errorf("%s %s = %+v, want %d", c.row, c.col, v, c.want)
		}
	}
}

// CMP-PRIOR-PERIOD uses P-1 (BR-15.1): from the first period of a fiscal year it
// reaches back into the year before, past the adjustment period 13 unless the
// definition counts adjustment periods.
func TestPriorPeriodTemplate_CrossesTheYearAndSkipsAdjustmentPeriod(t *testing.T) {
	rows := RowSet{Code: "REV", Name: "Revenue", Rows: []Row{
		{Code: "R", Kind: RowAccountRange, Label: "Revenue", AccountFrom: "400000", AccountTo: "499999", NormalBalance: "credit"},
	}}
	cal, err := NewCalendar([]Period{
		{Year: 2025, No: 12, Quarter: 4}, {Year: 2025, No: 13, Quarter: 4, Adjustment: true}, {Year: 2026, No: 1, Quarter: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	bal := func(p int, net int64) Balance {
		return Balance{LedgerID: "L1", Period: p, Account: "400100", Combination: "X", CurrencyType: "acct", Net: d(net)}
	}
	bals := []Balance{bal(0, -80), bal(1, -5), bal(2, -100)}
	for _, c := range []struct {
		adjustment bool
		prior      int64
	}{{false, 80}, {true, 5}} {
		def, issues := Compose("REV-PP", "Revenue against prior period", KindIncomeStatement, c.adjustment, rows, shippedTemplate(t, "CMP-PRIOR-PERIOD"))
		if len(issues) > 0 {
			t.Fatalf("composed definition invalid: %+v", issues)
		}
		res, err := Evaluate(Input{Definition: def, LedgerID: "L1", Calendar: cal, At: 2, Balances: bals, CurrencyType: "acct"})
		if err != nil {
			t.Fatal(err)
		}
		if v := cellAt(t, res, "R", "C2"); !v.Value.Equal(d(c.prior)) {
			t.Errorf("adjustment periods %v: prior period = %s, want %d", c.adjustment, v.Value, c.prior)
		}
		if v := cellAt(t, res, "R", "C3"); !v.Value.Equal(d(100 - c.prior)) {
			t.Errorf("adjustment periods %v: variance = %s, want %d", c.adjustment, v.Value, 100-c.prior)
		}
	}
}
