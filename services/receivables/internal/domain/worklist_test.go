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

// FR-30.1: credits net against the customer's debits, oldest due date first. The
// worklist lists only customers with a debit to work; the balances keep a
// customer that is net in credit.
func TestWorklist_CreditsNetOldestFirst(t *testing.T) {
	asOf := day("2027-03-31")
	items := []WorkItem{
		{CustomerID: "A", Currency: "USD", DueDate: day("2027-04-10"), OpenAmount: amt("100")},
		{CustomerID: "A", Currency: "USD", DueDate: day("2027-03-01"), OpenAmount: amt("100"), DunningLevel: 2},
		{CustomerID: "A", Currency: "USD", DueDate: day("2027-03-20"), OpenAmount: amt("-150")},
		{CustomerID: "B", Currency: "USD", DueDate: day("2027-01-01"), OpenAmount: amt("200")},
		{CustomerID: "B", Currency: "USD", DueDate: day("2027-03-30"), OpenAmount: amt("-50")},
		{CustomerID: "C", Currency: "USD", DueDate: day("2027-02-01"), OpenAmount: amt("-40")},
	}
	got := Worklist(asOf, items, map[string]int{"C": 1}, nil)
	if len(got) != 2 {
		t.Fatalf("rows = %+v, want A and B only (C holds only a credit)", got)
	}
	byCustomer := map[string]CustomerRisk{}
	for _, r := range got {
		byCustomer[r.CustomerID] = r
	}
	// A: the credit clears the overdue 100 (and its dunning level) and 50 of the current one.
	if a := byCustomer["A"]; !a.TotalOpen.Equal(amt("50")) || !a.Overdue.IsZero() || a.OldestDays != 0 || a.DunningLevel != 0 || a.RiskScore != 0 {
		t.Errorf("row A = %+v, want 50 open, nothing overdue, level 0, score 0", a)
	}
	// B: 150 of the overdue 200 stays open; the ratio is overdue over the net total.
	if b := byCustomer["B"]; !b.TotalOpen.Equal(amt("150")) || !b.Overdue.Equal(amt("150")) || b.OldestDays != 89 || b.RiskScore != 60 {
		t.Errorf("row B = %+v, want 150 open and overdue, 89 days, score 60", b)
	}

	balances := CustomerBalances(asOf, items, map[string]int{"C": 1}, nil)
	if len(balances) != 3 {
		t.Fatalf("balances = %+v, want A, B and C", balances)
	}
	for _, r := range balances {
		if r.CustomerID == "C" && (!r.TotalOpen.Equal(amt("-40")) || !r.Overdue.IsZero() || r.OldestDays != 0 || r.OpenDisputes != 1) {
			t.Errorf("balance C = %+v, want -40 net in credit, nothing overdue, its dispute counted", r)
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
			cents := rapid.Int64Range(-200_000, 500_000).Draw(t, "cents")
			dueOffset := rapid.IntRange(-200, 200).Draw(t, "due")
			cust := fmt.Sprintf("C%d", rapid.IntRange(0, 4).Draw(t, "cust"))
			open := decimal.NewFromInt(cents).Shift(-2)
			want = want.Add(open)
			items = append(items, WorkItem{CustomerID: cust, Currency: "USD",
				DueDate: asOf.AddDate(0, 0, dueOffset), OpenAmount: open, DunningLevel: rapid.IntRange(0, 4).Draw(t, "level")})
		}
		// The net balances conserve every open amount, debit and credit; the
		// worklist is the subset of them with a debit.
		total := decimal.Zero
		for _, r := range CustomerBalances(asOf, items, nil, nil) {
			total = total.Add(r.TotalOpen)
			if r.Overdue.IsNegative() || r.Overdue.GreaterThan(decimal.Max(r.TotalOpen, decimal.Zero)) {
				t.Fatalf("overdue %s outside 0..max(total %s, 0)", r.Overdue, r.TotalOpen)
			}
		}
		rows := Worklist(asOf, items, nil, nil)
		for i, r := range rows {
			if r.RiskScore < 0 || r.RiskScore > 100 {
				t.Fatalf("score %d out of range", r.RiskScore)
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
