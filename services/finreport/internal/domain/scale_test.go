package domain

import (
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// scaleInput builds a synthetic ledger: one account range row per account, one
// column per trailing period, and accounts x periods x combos balances. The
// shape matches the reference statement of FR-15.6 (2,000 rows x 12 columns).
func scaleInput(accounts, periods, combos int) Input {
	ps := make([]Period, periods)
	for i := range ps {
		ps[i] = Period{Year: 2026, No: i + 1, Quarter: i/3 + 1}
	}
	cal, err := NewCalendar(ps)
	if err != nil {
		panic(err)
	}
	balances := make([]Balance, 0, accounts*periods*combos)
	for a := 0; a < accounts; a++ {
		acct := fmt.Sprintf("%05d", a)
		for p := 0; p < periods; p++ {
			for c := 0; c < combos; c++ {
				balances = append(balances, Balance{
					LedgerID: "L1", Period: p, Account: acct, Combination: fmt.Sprintf("C%d", c),
					CurrencyType: "acct", Net: decimal.NewFromInt(int64(a*7 + c*3 + p - 11)),
				})
			}
		}
	}
	rows := make([]Row, accounts)
	for a := range rows {
		acct := fmt.Sprintf("%05d", a)
		rows[a] = Row{Code: fmt.Sprintf("R%04d", a), Kind: RowAccountRange, Label: "Account " + acct,
			AccountFrom: acct, AccountTo: acct, NormalBalance: "debit"}
	}
	cols := make([]Column, periods)
	for j := range cols {
		expr := "P"
		if j > 0 {
			expr = fmt.Sprintf("P-%d", j)
		}
		cols[j] = Column{Code: fmt.Sprintf("C%02d", j), Label: "Period -" + fmt.Sprint(j), PeriodExpr: expr, CurrencyType: "acct"}
	}
	def := Definition{
		Code: "SCALE-2000X12", Name: "Scale statement", Kind: KindBalanceSheet,
		Rows: rows, Columns: cols,
	}
	return Input{Definition: def, LedgerID: "L1", Calendar: cal, At: periods - 1, Balances: balances}
}

// TestEvaluate_Scale2000x12Under20s is the FR-15.6 / AC-15.10 budget: a 2,000 x
// 12 statement over about 200k balances completes in under 20 seconds, and every
// balance-backed cell passes the drill contract (BR-15.4).
func TestEvaluate_Scale2000x12Under20s(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test skipped in -short mode")
	}
	in := scaleInput(2000, 12, 8)
	if len(in.Balances) < 190000 {
		t.Fatalf("fixture has %d balances, want about 200k", len(in.Balances))
	}
	start := time.Now()
	res, err := Evaluate(in)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 20*time.Second {
		t.Fatalf("statement took %s, budget is 20s", elapsed)
	}
	if len(res.Cells) != 2000*12 {
		t.Fatalf("cells = %d, want %d", len(res.Cells), 2000*12)
	}
	for _, c := range res.Cells {
		if err := c.CheckDrill(); err != nil {
			t.Fatalf("cell %s/%s: %v", c.Row, c.Column, err)
		}
	}
}

// TestEvaluate_ResultHashIsReproducible covers AC-15.7 and BR-15.6: the same
// balances and definition give the same hash regardless of input order, and a
// changed balance changes it.
func TestEvaluate_ResultHashIsReproducible(t *testing.T) {
	in := scaleInput(20, 3, 2)
	a, err := Evaluate(in)
	if err != nil {
		t.Fatal(err)
	}
	reversed := append([]Balance(nil), in.Balances...)
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	in.Balances = reversed
	b, err := Evaluate(in)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash == "" || a.Hash != b.Hash {
		t.Fatalf("hash differs for reordered input: %q vs %q", a.Hash, b.Hash)
	}
	in.Balances[0].Net = in.Balances[0].Net.Add(decimal.NewFromInt(1))
	c, err := Evaluate(in)
	if err != nil {
		t.Fatal(err)
	}
	if c.Hash == a.Hash {
		t.Fatal("hash did not change when a balance changed")
	}
}
