package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// rtCalendar is two fiscal years of twelve periods each, index 0 = 2025-01.
func rtCalendar(t *testing.T) Calendar {
	t.Helper()
	var periods []Period
	for y := 2025; y <= 2026; y++ {
		for no := 1; no <= 12; no++ {
			periods = append(periods, Period{Year: y, No: no, Quarter: (no-1)/3 + 1})
		}
	}
	cal, err := NewCalendar(periods)
	if err != nil {
		t.Fatal(err)
	}
	return cal
}

// rtDefinition builds a trial-balance style definition: one account_range row
// per account 1000.., and cols period columns cycling through the expressions.
func rtDefinition(rows, cols int) Definition {
	d := Definition{SchemaVersion: SchemaVersion, Code: "ROUNDTRIP", Name: "Round trip", Kind: KindTrialBalance}
	for i := 0; i < rows; i++ {
		acct := fmt.Sprintf("%04d", 1000+i)
		d.Rows = append(d.Rows, Row{Code: fmt.Sprintf("R%03d", i), Kind: RowAccountRange, Label: "Account " + acct,
			AccountFrom: acct, AccountTo: acct, NormalBalance: "debit"})
	}
	exprs := []string{"P", "P-1", "YTD", "PY", "PYTD", "Q", "FY"}
	for j := 0; j < cols; j++ {
		d.Columns = append(d.Columns, Column{Code: fmt.Sprintf("C%02d", j), Label: fmt.Sprintf("Column %d", j),
			PeriodExpr: exprs[j%len(exprs)], CurrencyType: "acct"})
	}
	return d
}

func TestDesignerRoundTrip_200x12_IdenticalCells(t *testing.T) {
	cal := rtCalendar(t)
	at, ok := cal.Index(2026, 3)
	if !ok {
		t.Fatal("2026-03 missing from calendar")
	}
	def := rtDefinition(200, 12)
	if issues := def.Validate(); len(issues) > 0 {
		t.Fatalf("fixture invalid: %+v", issues[0])
	}
	var bals []Balance
	for i := 0; i < 200; i++ {
		for p := 0; p <= at; p++ {
			bals = append(bals, Balance{LedgerID: "L1", Period: p, Account: fmt.Sprintf("%04d", 1000+i),
				CurrencyType: "acct", Net: decimal.New(int64(1000+i*7+p*13), -2), ClosingNet: decimal.New(int64(1000+i*7+p*13), -2)})
		}
	}
	raw, err := def.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatalf("re-import failed: %v", err)
	}
	back.Code = "ROUNDTRIP2"
	back.Name = "Round trip copy"

	in := func(d Definition) Input {
		return Input{Definition: d, LedgerID: "L1", Calendar: cal, At: at, Balances: bals}
	}
	a, err := Evaluate(in(def))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Evaluate(in(back))
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Cells) != 200*12 || len(a.Cells) != len(b.Cells) {
		t.Fatalf("cell counts differ: %d vs %d", len(a.Cells), len(b.Cells))
	}
	for i := range a.Cells {
		if a.Cells[i].Row != b.Cells[i].Row || a.Cells[i].Column != b.Cells[i].Column || !a.Cells[i].Value.Equal(b.Cells[i].Value) {
			t.Fatalf("cell %d differs: %s/%s %s vs %s/%s %s", i,
				a.Cells[i].Row, a.Cells[i].Column, a.Cells[i].Value, b.Cells[i].Row, b.Cells[i].Column, b.Cells[i].Value)
		}
		if err := a.Cells[i].CheckDrill(); err != nil {
			t.Fatalf("cell %s/%s: %v", a.Cells[i].Row, a.Cells[i].Column, err)
		}
	}
}

// TestDrillSumsToCell_Property is AC3: for any balances, the keys behind every
// cell sum exactly to the cell value.
func TestDrillSumsToCell_Property(t *testing.T) {
	cal := rtCalendar(t)
	at, _ := cal.Index(2026, 3)
	def := rtDefinition(20, 12)
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 300).Draw(t, "n")
		bals := make([]Balance, 0, n)
		for i := 0; i < n; i++ {
			acct := fmt.Sprintf("%04d", rapid.IntRange(1000, 1019).Draw(t, "acct"))
			period := rapid.IntRange(0, at).Draw(t, "period")
			amt := rapid.Int64Range(-100000000, 100000000).Draw(t, "amount")
			bals = append(bals, Balance{LedgerID: "L1", Period: period, Account: acct, CurrencyType: "acct",
				Net: decimal.New(amt, -2), ClosingNet: decimal.New(amt, -2)})
		}
		res, err := Evaluate(Input{Definition: def, LedgerID: "L1", Calendar: cal, At: at, Balances: bals})
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range res.Cells {
			if err := c.CheckDrill(); err != nil {
				t.Fatalf("cell %s/%s: %v", c.Row, c.Column, err)
			}
		}
	})
}
