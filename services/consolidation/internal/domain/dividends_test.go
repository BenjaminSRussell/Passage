package domain

import (
	"errors"
	"testing"
)

func entryOf(entries []Entry, account string) (Entry, bool) {
	for _, e := range entries {
		if e.Account == account {
			return e, true
		}
	}
	return Entry{}, false
}

// TestEliminateDividends_SharesWithNCI: S1 (80 %) declares EUR 50,000.00 at
// 1.10; E6 reverses USD 55,000.00 of dividends declared against the parent's
// 44,000.00 income and the NCI's 11,000.00.
func TestEliminateDividends_SharesWithNCI(t *testing.T) {
	got, err := EliminateDividends(Dividend{Currency: "USD", OwnershipPct: d("80"), Rate: d("1.10"), Amount: d("50000")})
	if err != nil {
		t.Fatal(err)
	}
	want := []Entry{
		{RuleCode: RuleE6, Account: AcctDividendIncome, Debit: d("44000")},
		{RuleCode: RuleE6, Account: AcctNCIEquity, Debit: d("11000")},
		{RuleCode: RuleE6, Account: AcctDividendsDeclared, Credit: d("55000")},
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %+v; want %+v", got, want)
	}
	for i, w := range want {
		g := got[i]
		if g.RuleCode != w.RuleCode || g.Account != w.Account || !g.Debit.Equal(w.Debit) || !g.Credit.Equal(w.Credit) {
			t.Fatalf("entry %d = %+v; want %+v", i, g, w)
		}
	}
}

func TestEliminateDividends_WhollyOwnedHasNoNCILine(t *testing.T) {
	got, err := EliminateDividends(Dividend{Currency: "USD", OwnershipPct: d("100"), Rate: d("1"), Amount: d("1000")})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entryOf(got, AcctNCIEquity); ok || len(got) != 2 {
		t.Fatalf("entries = %+v; want dividend income against dividends declared only", got)
	}
}

// TestEliminateDividends_RoundingLine is BR-40.9: 10.005 splits 5.0025 / 5.0025,
// each rounds to 5.00 while the total rounds to 10.01, so a 0.01 rounding line
// keeps E6 balanced.
func TestEliminateDividends_RoundingLine(t *testing.T) {
	got, err := EliminateDividends(Dividend{Currency: "USD", OwnershipPct: d("50"), Rate: d("1"), Amount: d("10.005")})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := entryOf(got, AcctRounding)
	if !ok || !r.Debit.Equal(d("0.01")) || r.RuleCode != RuleE6 {
		t.Fatalf("entries = %+v; want a 0.01 debit rounding line on E6", got)
	}
	if err := CheckBalanced(got); err != nil {
		t.Fatal(err)
	}
}

func TestEliminateDividends_Rejections(t *testing.T) {
	ok := Dividend{Currency: "USD", OwnershipPct: d("80"), Rate: d("1.1"), Amount: d("100")}
	cases := map[string]func(*Dividend){
		"currency":        func(x *Dividend) { x.Currency = "US" },
		"zero ownership":  func(x *Dividend) { x.OwnershipPct = d("0") },
		"over 100":        func(x *Dividend) { x.OwnershipPct = d("100.01") },
		"zero rate":       func(x *Dividend) { x.Rate = d("0") },
		"zero dividend":   func(x *Dividend) { x.Amount = d("0") },
		"negative amount": func(x *Dividend) { x.Amount = d("-1") },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			in := ok
			edit(&in)
			_, err := EliminateDividends(in)
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeInvalid {
				t.Fatalf("err = %v; want COM-0001", err)
			}
		})
	}
}

// TestBalanceRule_RejectsDefects: a residual beyond half a minor unit per line
// is a defect and fails with CNS-0903 instead of being hidden in rounding.
func TestBalanceRule_RejectsDefects(t *testing.T) {
	entries := []Entry{
		{RuleCode: RuleE6, Account: "A", Debit: d("100.00")},
		{RuleCode: RuleE6, Account: "B", Credit: d("99.00")},
	}
	_, err := BalanceRule(entries, RuleE6, AcctRounding, "USD")
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeUnbalancedRule {
		t.Fatalf("err = %v; want CNS-0903", err)
	}
	balanced := []Entry{{RuleCode: RuleE6, Account: "A", Debit: d("1")}, {RuleCode: RuleE6, Account: "B", Credit: d("1")}, {RuleCode: RuleE1, Account: "C", Debit: d("5")}}
	got, err := BalanceRule(balanced, RuleE6, AcctRounding, "USD")
	if err != nil || len(got) != 3 {
		t.Fatalf("balanced rule changed: %+v, %v", got, err)
	}
}
