package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func amt(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func item(id, doc, due, open string) Item {
	return Item{ID: id, DocumentNumber: doc, DueDate: day(due), OpenAmount: amt(open)}
}

func TestMatch_Decisions(t *testing.T) {
	tests := []struct {
		name     string
		payment  Payment
		open     []Item
		decision Decision
		topIDs   []string
		topScore int
	}{
		{
			name:     "reference and exact amount with a near value date auto-applies",
			payment:  Payment{Amount: amt("100.00"), ValueDate: day("2027-03-10"), Remittance: []RemittanceLine{{Reference: "INV-20001", Amount: amt("100.00")}}},
			open:     []Item{item("a", "INV-20001", "2027-03-12", "100.00"), item("b", "INV-20002", "2027-03-12", "250.00")},
			decision: DecisionAutoApply,
			topIDs:   []string{"a"},
			topScore: 95,
		},
		{
			name:     "two equally strong sets cannot auto-apply",
			payment:  Payment{Amount: amt("100.00"), ValueDate: day("2027-03-10"), Remittance: []RemittanceLine{{Reference: "INV-20001 INV-20002"}}},
			open:     []Item{item("a", "INV-20001", "2027-03-12", "100.00"), item("b", "INV-20002", "2027-03-12", "100.00")},
			decision: DecisionSuggested,
			topIDs:   []string{"a"},
			topScore: 95,
		},
		{
			name:     "exact amount without a reference is below the suggest threshold",
			payment:  Payment{Amount: amt("100.00"), ValueDate: day("2027-06-01")},
			open:     []Item{item("a", "INV-20001", "2027-03-12", "100.00")},
			decision: DecisionOnAccount,
			topIDs:   []string{"a"},
			topScore: 30,
		},
		{
			name:     "reference alone with a different amount is suggested",
			payment:  Payment{Amount: amt("90.00"), ValueDate: day("2027-06-01"), Remittance: []RemittanceLine{{Reference: "INV-20001"}}},
			open:     []Item{item("a", "INV-20001", "2027-03-12", "100.00")},
			decision: DecisionSuggested,
			topIDs:   []string{"a"},
			topScore: 60,
		},
		{
			name: "known payer quoting the wrong invoice is suggested, not auto-applied",
			payment: Payment{Amount: amt("100.00"), ValueDate: day("2027-06-01"), Remittance: []RemittanceLine{{Reference: "INV-20002"}},
				PayerCustomers: map[string]bool{"c1": true}, PayerName: "ACME INC", LearnedNames: map[string][]string{"c1": {"ACME INC"}}},
			open: []Item{{ID: "a", CustomerID: "c1", DocumentNumber: "INV-20001", DueDate: day("2027-03-12"), OpenAmount: amt("100.00")},
				{ID: "b", CustomerID: "c1", DocumentNumber: "INV-20002", DueDate: day("2027-03-12"), OpenAmount: amt("250.00")}},
			decision: DecisionSuggested,
			topIDs:   []string{"b"},
			topScore: 85,
		},
		{
			name:     "exact subset with one referenced item auto-applies over its single",
			payment:  Payment{Amount: amt("300.00"), ValueDate: day("2027-06-01"), Remittance: []RemittanceLine{{Reference: "INV-A001"}}},
			open:     []Item{item("a", "INV-A001", "2027-03-12", "100.00"), item("b", "INV-B001", "2027-03-12", "200.00")},
			decision: DecisionAutoApply,
			topIDs:   []string{"a", "b"},
			topScore: 85,
		},
		{
			name:     "early payment discount matches with a reference",
			payment:  Payment{Amount: amt("98.00"), ValueDate: day("2027-03-05"), Remittance: []RemittanceLine{{Reference: "INV-30001"}}},
			open:     []Item{{ID: "a", DocumentNumber: "INV-30001", DueDate: day("2027-03-30"), OpenAmount: amt("100.00"), DiscountDate: ptr(day("2027-03-10")), DiscountAmount: amt("2.00")}},
			decision: DecisionAutoApply,
			topIDs:   []string{"a"},
			topScore: 88,
		},
		{
			name:     "no open items puts the payment on account",
			payment:  Payment{Amount: amt("50.00"), ValueDate: day("2027-03-05")},
			open:     nil,
			decision: DecisionOnAccount,
			topIDs:   nil,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Match(tc.payment, tc.open)
			if got.Decision != tc.decision {
				t.Fatalf("decision = %s, want %s (top %+v)", got.Decision, tc.decision, got.Top)
			}
			if tc.topIDs == nil {
				return
			}
			if len(got.Top) == 0 {
				t.Fatalf("no candidates, want %v", tc.topIDs)
			}
			top := got.Top[0]
			if top.Score != tc.topScore || !sameIDs(top.ItemIDs, tc.topIDs) {
				t.Fatalf("top = %v score %d, want %v score %d", top.ItemIDs, top.Score, tc.topIDs, tc.topScore)
			}
		})
	}
}

func TestMatch_TopIsCappedAtThree(t *testing.T) {
	var open []Item
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		open = append(open, item(id, "INV-1000"+id, "2027-03-12", "10.00"))
	}
	got := Match(Payment{Amount: amt("10.00"), ValueDate: day("2027-03-12")}, open)
	if len(got.Top) != 3 {
		t.Fatalf("top has %d candidates, want 3", len(got.Top))
	}
}

func TestReferenced_IgnoresShortNumbers(t *testing.T) {
	p := Payment{Remittance: []RemittanceLine{{Reference: "PAY 12 for 1234 units"}}}
	if referenced(p, "12") {
		t.Fatal("a two-character document number must not match by chance")
	}
	if !referenced(p, "1234") {
		t.Fatal("a four-character document number in the remittance must match")
	}
}

func TestReferenced_MatchesWholeDocumentNumbersOnly(t *testing.T) {
	tests := []struct {
		ref  string
		doc  string
		want bool
	}{
		{"INV-1001", "INV-1001", true},
		{"paying inv-1001, inv-1002", "INV-1001", true},
		{"(INV-1001)", "INV-1001", true},
		{"INV-1001.", "INV-1001", true},
		{"INV-10012", "INV-1001", false},
		{"XINV-1001", "INV-1001", false},
		{"INV-1001-2", "INV-1001", false},
		{"2INV-1001 and INV-1001", "INV-1001", true},
		{"INV-1001", "INV-1001/2", true},
		{"INV-1001/2", "INV-1001/2", true},
		{"INV-1001/3", "INV-1001/2", false},
		{"INV-10012", "INV-1001/2", false},
		{"INV-1001", "INV-1001/x", false},
		{"INV", "INV/1", false},
	}
	for _, tc := range tests {
		p := Payment{Remittance: []RemittanceLine{{Reference: tc.ref}}}
		if got := referenced(p, tc.doc); got != tc.want {
			t.Errorf("referenced(%q, %q) = %v, want %v", tc.ref, tc.doc, got, tc.want)
		}
	}
}

func TestMatch_RemittanceNamingTheInvoiceSettlesAllItsInstallments(t *testing.T) {
	p := Payment{Amount: amt("1000.00"), ValueDate: day("2027-04-30"), Remittance: []RemittanceLine{{Reference: "INV-20001"}}}
	got := Match(p, []Item{item("i1", "INV-20001/1", "2027-04-30", "333.34"), item("i2", "INV-20001/2", "2027-05-30", "333.33"),
		item("i3", "INV-20001/3", "2027-06-30", "333.33"), item("other", "INV-30001", "2027-04-30", "1000.00")})
	if got.Decision != DecisionAutoApply || len(got.Top[0].ItemIDs) != 3 {
		t.Fatalf("got %s top %+v, want auto-apply to the three installments of INV-20001", got.Decision, got.Top)
	}
}

func TestMatch_LongerDocumentNumberIsNoFalseMatch(t *testing.T) {
	// The remittance names INV-10012; INV-1001 has the same amount but must not
	// pick up the +60 reference score and auto-apply to the wrong item.
	p := Payment{Amount: amt("100.00"), ValueDate: day("2027-03-10"), Remittance: []RemittanceLine{{Reference: "INV-10012"}}}
	got := Match(p, []Item{item("wrong", "INV-1001", "2027-03-12", "100.00"), item("right", "INV-10012", "2027-03-12", "100.00")})
	if got.Decision != DecisionAutoApply || got.Top[0].ItemIDs[0] != "right" {
		t.Fatalf("got %s top %+v, want auto-apply to INV-10012 only", got.Decision, got.Top)
	}
}

func TestMatch_SubsetSearchReachesReferencedItemsBeyondThePool(t *testing.T) {
	// An unidentified payer pays INV-20101 (300.00) and INV-20102 (450.25), both
	// due in May, while twelve other items fall due earlier and would fill the
	// subset pool by due date. Hand-derived: the pair scores R2-SUBSET 25 + R1 60 =
	// 85 (no R6: value date 2027-03-15 is far from the May due dates), each item
	// alone R1 60, so the pair auto-applies with a margin of 25.
	open := []Item{item("x", "INV-20101", "2027-05-10", "300.00"), item("y", "INV-20102", "2027-05-12", "450.25")}
	for i := 0; i < MaxSubsetPool; i++ {
		open = append(open, item(string(rune('a'+i)), "INV-3000"+string(rune('a'+i)), "2027-03-01", "1000.00"))
	}
	p := Payment{Amount: amt("750.25"), ValueDate: day("2027-03-15"), Remittance: []RemittanceLine{{Reference: "INV-20101 INV-20102"}}}
	got := Match(p, open)
	if got.Decision != DecisionAutoApply || !sameIDs(got.Top[0].ItemIDs, []string{"x", "y"}) || got.Top[0].Score != 85 {
		t.Fatalf("got %s top %+v, want auto-apply of x and y at 85", got.Decision, got.Top)
	}
}

func TestMatch_PropertySubsetBoundAndAutoRule(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 15).Draw(t, "n")
		open := make([]Item, 0, n)
		for i := 0; i < n; i++ {
			cents := rapid.Int64Range(1, 50000).Draw(t, "cents")
			open = append(open, Item{
				ID:         string(rune('a' + i)),
				DueDate:    day("2027-03-12"),
				OpenAmount: decimal.New(cents, -2),
			})
		}
		amount := decimal.New(rapid.Int64Range(1, 200000).Draw(t, "amount"), -2)
		res := Match(Payment{Amount: amount, ValueDate: day("2027-03-12")}, open)
		if len(res.Top) > 3 {
			t.Fatalf("top has %d candidates", len(res.Top))
		}
		byID := map[string]Item{}
		for _, it := range open {
			byID[it.ID] = it
		}
		for _, c := range res.Top {
			if len(c.ItemIDs) > MaxSubsetSize {
				t.Fatalf("candidate settles %d items, limit %d", len(c.ItemIDs), MaxSubsetSize)
			}
			if len(c.ItemIDs) > 1 {
				sum := decimal.Zero
				for _, id := range c.ItemIDs {
					sum = sum.Add(byID[id].OpenAmount)
				}
				if !sum.Equal(amount) {
					t.Fatalf("subset %v sums to %s, payment is %s", c.ItemIDs, sum, amount)
				}
			}
		}
		if res.Decision == DecisionAutoApply {
			top := res.Top[0]
			if top.Score < AutoThreshold {
				t.Fatalf("auto-applied at score %d", top.Score)
			}
			// A runner-up is any listed set with an item outside the top set.
			for _, c := range res.Top[1:] {
				if !within(c.ItemIDs, top.ItemIDs) && c.Score > top.Score-AutoMargin {
					t.Fatalf("auto-applied with runner-up %d against %d", c.Score, top.Score)
				}
			}
		}
	})
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := map[string]int{}
	for _, x := range a {
		seen[x]++
	}
	for _, x := range b {
		seen[x]--
	}
	for _, v := range seen {
		if v != 0 {
			return false
		}
	}
	return true
}

func ptr[T any](v T) *T { return &v }
