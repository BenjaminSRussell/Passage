package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func consolidationFixture(t *testing.T) (Definition, Calendar, []Balance) {
	t.Helper()
	cal := twoYearCalendar(t)
	at := mustIndex(t, cal, 2026, 3)
	mk := func(ledger, acct, combo string, net int64) Balance {
		return Balance{LedgerID: ledger, Period: at, Account: acct, Combination: combo, CurrencyType: "rpt", Net: d(net)}
	}
	bals := []Balance{mk("L1", "4000", "a", 10), mk("L2", "4000", "b", 20), mk("L2", "4100", "c", 5), mk("L3", "4000", "d", 40)}
	def := Definition{Code: "TB-X", Name: "Trial balance", Kind: KindTrialBalance,
		Rows:    []Row{{Code: "REV", Kind: RowAccountRange, Label: "Revenue", AccountFrom: "4000", AccountTo: "4999", NormalBalance: "debit"}},
		Columns: []Column{{Code: "P", Label: "Period", PeriodExpr: "P", CurrencyType: "rpt"}}}
	return def, cal, bals
}

// FR-15.5: one column per ledger plus the consolidated sum; each column drills
// to exactly its own ledger's balances.
func TestConsolidate_ColumnPerEntityAndTotal(t *testing.T) {
	def, cal, bals := consolidationFixture(t)
	ledgers := []LedgerColumn{{"L1", "Plant A"}, {"L2", "Plant B"}, {"L3", "Plant C"}}
	exp, err := Consolidate(def, ledgers)
	if err != nil {
		t.Fatal(err)
	}
	if len(exp.Columns) != 4 || exp.Columns[0].Code != "P" || exp.Columns[1].Code != "P_E1" || exp.Columns[1].Label != "Period - Plant A" {
		t.Fatalf("columns = %+v", exp.Columns)
	}
	res, err := Evaluate(Input{Definition: exp, CurrencyType: "rpt", Consolidated: true, Calendar: cal, At: mustIndex(t, cal, 2026, 3), Balances: bals})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"P": 75, "P_E1": 10, "P_E2": 25, "P_E3": 40}
	for col, w := range want {
		c := cellAt(t, res, "REV", col)
		if !c.Value.Equal(d(w)) {
			t.Errorf("%s = %s, want %d", col, c.Value, w)
		}
		if err := c.CheckDrill(); err != nil {
			t.Errorf("%s drill: %v", col, err)
		}
		for _, k := range c.Keys {
			if col != "P" && k.LedgerID != ledgers[int(col[len(col)-1]-'1')].LedgerID {
				t.Errorf("%s has a key of ledger %s", col, k.LedgerID)
			}
		}
	}
	if n := len(cellAt(t, res, "REV", "P").Keys); n != 4 {
		t.Errorf("consolidated column has %d keys, want 4", n)
	}
	// The entity columns add up to the total, and the base definition is untouched.
	if len(def.Columns) != 1 || def.Columns[0].Ledger != "" {
		t.Errorf("base definition changed: %+v", def.Columns)
	}
}

// A CALC column is expanded like the others: each entity's column calculates
// over that entity's columns, the consolidated one over the consolidated
// columns (so its percent is of the totals, not a sum of percents).
func TestConsolidate_CalcColumnsPerEntity(t *testing.T) {
	def, cal, _ := consolidationFixture(t)
	at := mustIndex(t, cal, 2026, 3)
	prev := mustIndex(t, cal, 2026, 2)
	mk := func(ledger string, period int, net int64) Balance {
		return Balance{LedgerID: ledger, Period: period, Account: "4000", Combination: "a", CurrencyType: "rpt", Net: d(net)}
	}
	bals := []Balance{mk("L1", at, 12), mk("L1", prev, 6), mk("L2", at, 30), mk("L2", prev, 10)}
	def.Kind = KindIncomeStatement // period movements, not cumulative balances
	def.Columns = []Column{
		{Code: "CUR", Label: "Period", PeriodExpr: "P", CurrencyType: "rpt"},
		{Code: "PREV", Label: "Prior", PeriodExpr: "P-1", CurrencyType: "rpt"},
		{Code: "VAR", Label: "Variance", PeriodExpr: "CALC", Expression: "CUR-PREV"},
		{Code: "PCT", Label: "Variance %", PeriodExpr: "CALC", Expression: "(CUR-PREV)/PREV*100"},
	}
	exp, err := Consolidate(def, []LedgerColumn{{"L1", "A"}, {"L2", "B"}})
	if err != nil {
		t.Fatal(err)
	}
	if e := exp.Columns[7].Expression; exp.Columns[7].Code != "VAR_E1" || e != "CUR_E1 - PREV_E1" {
		t.Fatalf("VAR_E1 expression = %q (%s)", e, exp.Columns[7].Code)
	}
	if e := exp.Columns[6].Expression; e != "CUR-PREV" {
		t.Fatalf("consolidated VAR expression = %q", e)
	}
	if errs := exp.Validate(); len(errs) > 0 {
		t.Fatalf("expanded definition is invalid: %v", errs)
	}
	res, err := Evaluate(Input{Definition: exp, CurrencyType: "rpt", Consolidated: true, Calendar: cal, At: at, Balances: bals})
	if err != nil {
		t.Fatal(err)
	}
	for col, w := range map[string]string{"VAR": "26", "PCT": "162.5", "VAR_E1": "6", "PCT_E1": "100", "VAR_E2": "20", "PCT_E2": "200"} {
		c := cellAt(t, res, "REV", col)
		if !c.Computed || !c.Value.Equal(decimal.RequireFromString(w)) {
			t.Errorf("%s = %s (computed %v), want %s", col, c.Value, c.Computed, w)
		}
	}
}

// Every standard definition but the cash flow consolidates into a valid
// definition (TB-STD and BS-STD through their CALC columns).
func TestConsolidate_StandardDefinitions(t *testing.T) {
	for _, def := range StandardDefinitions() {
		exp, err := Consolidate(def, []LedgerColumn{{"L1", "A"}, {"L2", "B"}})
		if def.Kind == KindCashFlow {
			if err == nil {
				t.Errorf("%s consolidated", def.Code)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", def.Code, err)
			continue
		}
		if errs := exp.Validate(); len(errs) > 0 || len(exp.Columns) != 3*len(def.Columns) {
			t.Errorf("%s: %d columns, issues %v", def.Code, len(exp.Columns), errs)
		}
	}
}

func TestConsolidate_Refusals(t *testing.T) {
	def, _, _ := consolidationFixture(t)
	two := []LedgerColumn{{"L1", "A"}, {"L2", "B"}}
	cf := def
	cf.Kind = KindCashFlow
	wide := def
	wide.Columns = nil
	for i := 0; i < 21; i++ {
		wide.Columns = append(wide.Columns, Column{Code: "C" + string(rune('A'+i)), Label: "c", PeriodExpr: "P", CurrencyType: "rpt"})
	}
	clash := def
	clash.Columns = append(append([]Column{}, def.Columns...), Column{Code: "P_E2", Label: "Other", PeriodExpr: "P-1", CurrencyType: "rpt"})
	cases := map[string]struct {
		d  Definition
		l  []LedgerColumn
		in string
	}{
		"one ledger": {def, two[:1], "2 to 12"},
		"duplicate":  {def, []LedgerColumn{{"L1", "A"}, {"L1", "B"}}, "twice"},
		"too many":   {def, make([]LedgerColumn, 13), "2 to 12"},
		"cash flow":  {cf, two, "cash flow"},
		"too wide":   {wide, two, "column limit"},
		"code clash": {clash, two, "collides"},
	}
	for name, c := range cases {
		_, err := Consolidate(c.d, c.l)
		e, ok := err.(*Error)
		if !ok || e.Code != CodeConsolidation || !strings.Contains(e.Detail, c.in) {
			t.Errorf("%s: err = %v, want %s containing %q", name, err, CodeConsolidation, c.in)
		}
	}
}
