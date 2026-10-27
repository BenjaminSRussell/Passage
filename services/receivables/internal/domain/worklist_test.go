package domain

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestWorklist_RollsUpAndRanks(t *testing.T) {
	asOf := day("2027-03-31")
	items := []WorkItem{
		{CustomerID: "A", Currency: "USD", DueDate: day("2027-03-01"), OpenAmount: amt("100"), DunningLevel: 2},
		{CustomerID: "A", Currency: "USD", DueDate: day("2027-04-10"), OpenAmount: amt("100")},
		{CustomerID: "B", Currency: "USD", DueDate: day("2027-04-10"), OpenAmount: amt("50")},
		{CustomerID: "C", Currency: "USD", DueDate: day("2027-01-01"), OpenAmount: amt("0")},
	}
	got := Worklist(asOf, items, map[string]int{"A": 1}, nil)
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2 (zero-balance customer C excluded): %+v", len(got), got)
	}
	a := got[0]
	if a.CustomerID != "A" || a.RiskScore != 45 || a.OpenDisputes != 1 || a.AvgDaysLate != 30 || a.OldestDays != 30 {
		t.Errorf("row A = %+v, want score 45 (overdue 20 + days 10 + level 10 + dispute 5)", a)
	}
	if !a.TotalOpen.Equal(amt("200")) || !a.Overdue.Equal(amt("100")) {
		t.Errorf("row A totals = %s / %s", a.TotalOpen, a.Overdue)
	}
	if got[1].CustomerID != "B" || got[1].RiskScore != 0 {
		t.Errorf("row B = %+v, want score 0 ranked last", got[1])
	}
}

func TestWorklist_SeparatesCurrencies(t *testing.T) {
	asOf := day("2027-03-31")
	items := []WorkItem{
		{CustomerID: "A", Currency: "EUR", DueDate: day("2027-03-01"), OpenAmount: amt("10")},
		{CustomerID: "A", Currency: "USD", DueDate: day("2027-04-10"), OpenAmount: amt("10")},
	}
	got := Worklist(asOf, items, map[string]int{"A": 2}, map[string]int{"A": 1})
	if len(got) != 2 {
		t.Fatalf("rows = %+v, want one per currency", got)
	}
	// Disputes and promises attach once, to the first currency row (EUR).
	if got[0].Currency != "EUR" && got[1].Currency != "EUR" {
		t.Fatalf("missing EUR row: %+v", got)
	}
	for _, r := range got {
		if r.Currency == "EUR" && (r.OpenDisputes != 2 || r.BrokenPromises != 1) {
			t.Errorf("EUR row = %+v, want disputes 2 and broken promise 1", r)
		}
		if r.Currency == "USD" && (r.OpenDisputes != 0 || r.BrokenPromises != 0) {
			t.Errorf("USD row repeats customer counts: %+v", r)
		}
	}
}

func TestBrokenPromise(t *testing.T) {
	asOf := day("2027-03-31")
	tests := []struct {
		name    string
		promise string
		open    string
		want    bool
	}{
		{"three days past promise", "2027-03-28", "10", true},
		{"within grace", "2027-03-29", "10", false},
		{"kept, nothing open", "2027-03-01", "0", false},
		{"future promise", "2027-04-02", "10", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := BrokenPromise(asOf, day(tc.promise), amt(tc.open)); got != tc.want {
				t.Errorf("BrokenPromise = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWorklist_ConservesBalancesAndRanks(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		asOf := day("2027-03-31")
		n := rapid.IntRange(0, 12).Draw(t, "n")
		items := make([]WorkItem, 0, n)
		want := decimal.Zero
		for i := 0; i < n; i++ {
			cents := rapid.Int64Range(0, 500_000).Draw(t, "cents")
			dueOffset := rapid.IntRange(-200, 200).Draw(t, "due")
			cust := fmt.Sprintf("C%d", rapid.IntRange(0, 4).Draw(t, "cust"))
			open := decimal.NewFromInt(cents).Shift(-2)
			if open.IsPositive() {
				want = want.Add(open)
			}
			items = append(items, WorkItem{CustomerID: cust, Currency: "USD",
				DueDate: asOf.AddDate(0, 0, dueOffset), OpenAmount: open, DunningLevel: rapid.IntRange(0, 4).Draw(t, "level")})
		}
		rows := Worklist(asOf, items, nil, nil)
		total := decimal.Zero
		for i, r := range rows {
			total = total.Add(r.TotalOpen)
			if r.RiskScore < 0 || r.RiskScore > 100 {
				t.Fatalf("score %d out of range", r.RiskScore)
			}
			if r.Overdue.GreaterThan(r.TotalOpen) {
				t.Fatalf("overdue %s exceeds total %s", r.Overdue, r.TotalOpen)
			}
			if i > 0 && rows[i-1].RiskScore < r.RiskScore {
				t.Fatalf("rows not ranked by score: %d before %d", rows[i-1].RiskScore, r.RiskScore)
			}
		}
		if !total.Equal(want) {
			t.Fatalf("totals %s != open balances %s", total, want)
		}
	})
}
