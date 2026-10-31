package domain

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestJaroWinkler_ReferenceValues(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want float64
	}{
		{"identical", "ACME CORP", "acme corp", 1},
		{"martha", "MARTHA", "MARHTA", 0.9611},
		{"dwayne", "DWAYNE", "DUANE", 0.8400},
		{"dixon", "DIXON", "DICKSONX", 0.8133},
		{"punctuation ignored", "Acme, Inc.", "ACME INC", 1},
		{"disjoint", "ABC", "XYZ", 0},
		{"empty vs name", "", "ACME", 0},
		{"both empty", "", "", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := JaroWinkler(tc.a, tc.b)
			if math.Abs(got-tc.want) > 0.01 {
				t.Fatalf("JaroWinkler(%q, %q) = %.4f, want about %.4f", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestJaroWinkler_BoundedAndIdentity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		a := rapid.String().Draw(t, "a")
		b := rapid.String().Draw(t, "b")
		s := JaroWinkler(a, b)
		if s < 0 || s > 1 || math.IsNaN(s) {
			t.Fatalf("JaroWinkler(%q, %q) = %v outside [0,1]", a, b, s)
		}
		if NormalizeName(a) != "" && JaroWinkler(a, a) != 1 {
			t.Fatalf("JaroWinkler(%q, itself) != 1", a)
		}
	})
}

func TestNormalizeName(t *testing.T) {
	tests := []struct{ in, want string }{
		{"  Acme,   Inc. ", "ACME INC"},
		{"O'Brien & Sons", "O BRIEN SONS"},
		{"", ""},
		{"...", ""},
	}
	for _, tc := range tests {
		if got := NormalizeName(tc.in); got != tc.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestPayerRules_R4R5R7(t *testing.T) {
	due := time.Date(2027, 3, 20, 0, 0, 0, 0, time.UTC)
	far := due.AddDate(0, 0, 60) // outside the R6 window so the table isolates the new rules
	item := func(customer, cur string, open string) Item {
		return Item{ID: "i-" + customer, CustomerID: customer, Currency: cur, DocumentNumber: "DOC-" + customer,
			DueDate: due, OpenAmount: decimal.RequireFromString(open)}
	}
	tests := []struct {
		name      string
		payment   Payment
		item      Item
		wantRules []string
		wantScore int
		candidate bool
	}{
		{
			name:      "R4 payer hash bound to customer",
			payment:   Payment{Amount: decimal.RequireFromString("100"), ValueDate: far, PayerCustomers: map[string]bool{"c1": true}},
			item:      item("c1", "USD", "100"),
			wantRules: []string{"R2", "R4"}, wantScore: 30 + 15, candidate: true,
		},
		{
			name: "R5 learned payer name matches",
			payment: Payment{Amount: decimal.RequireFromString("100"), ValueDate: far, PayerName: "ACME, INC.",
				LearnedNames: map[string][]string{"c1": {"Acme Inc"}}},
			item:      item("c1", "USD", "100"),
			wantRules: []string{"R2", "R5"}, wantScore: 30 + 10, candidate: true,
		},
		{
			name: "R5 does not apply to a different name",
			payment: Payment{Amount: decimal.RequireFromString("100"), ValueDate: far, PayerName: "Globex",
				LearnedNames: map[string][]string{"c1": {"Acme Inc"}}},
			item:      item("c1", "USD", "100"),
			wantRules: []string{"R2"}, wantScore: 30, candidate: true,
		},
		{
			name:      "R7 short by less than 1 percent with reason",
			payment:   Payment{Amount: decimal.RequireFromString("995"), ValueDate: far, ShortPayReason: "promo"},
			item:      item("c1", "USD", "1000"),
			wantRules: []string{"R7"}, wantScore: 20, candidate: true,
		},
		{
			name:      "R7 short by less than the USD tolerance with reason",
			payment:   Payment{Amount: decimal.RequireFromString("700"), ValueDate: far, ShortPayReason: "damage"},
			item:      item("c1", "USD", "740"),
			wantRules: []string{"R7"}, wantScore: 20, candidate: true,
		},
		{
			name:      "R7 needs a reason",
			payment:   Payment{Amount: decimal.RequireFromString("995"), ValueDate: far},
			item:      item("c1", "USD", "1000"),
			wantRules: nil, wantScore: 0, candidate: false,
		},
		{
			name:      "R7 does not apply when short by more than tolerance",
			payment:   Payment{Amount: decimal.RequireFromString("500"), ValueDate: far, ShortPayReason: "promo"},
			item:      item("c1", "USD", "1000"),
			wantRules: nil, wantScore: 0, candidate: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res := Match(tc.payment, []Item{tc.item})
			if tc.candidate != (len(res.Top) > 0) {
				t.Fatalf("candidate presence = %v, want %v (result %+v)", len(res.Top) > 0, tc.candidate, res)
			}
			if !tc.candidate {
				return
			}
			c := res.Top[0]
			if c.Score != tc.wantScore {
				t.Errorf("score = %d, want %d (rules %v)", c.Score, tc.wantScore, c.Rules)
			}
			for _, r := range tc.wantRules {
				if !hasRule(c.Rules, r) {
					t.Errorf("rules %v missing %s", c.Rules, r)
				}
			}
		})
	}
}

func TestShortPaid_NeverNegativeOrOverpaid(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		open := decimal.NewFromInt(rapid.Int64Range(1, 100000).Draw(t, "open"))
		paid := decimal.NewFromInt(rapid.Int64Range(1, 200000).Draw(t, "paid"))
		p := Payment{Amount: paid, ShortPayReason: "x"}
		it := Item{Currency: "EUR", OpenAmount: open}
		if shortPaid(p, it) && !paid.LessThan(open) {
			t.Fatalf("shortPaid true for paid %s >= open %s", paid, open)
		}
	})
}

// A shortfall that is exactly the available cash discount is R3, not also R7.
func TestShortPay_NotScoredWhenTheDiscountExplainsIt(t *testing.T) {
	due := time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)
	discountDate := due.AddDate(0, 0, -20)
	it := Item{ID: "i-1", CustomerID: "c1", Currency: "USD", DocumentNumber: "INV-1", DueDate: due,
		OpenAmount: decimal.RequireFromString("1000"), DiscountDate: &discountDate, DiscountAmount: decimal.RequireFromString("5")}
	p := Payment{Amount: decimal.RequireFromString("995"), ValueDate: discountDate.AddDate(0, 0, -60), ShortPayReason: "promo"}
	res := Match(p, []Item{it})
	if len(res.Top) != 1 || !hasRule(res.Top[0].Rules, "R3") || hasRule(res.Top[0].Rules, "R7") || res.Top[0].Score != weightR3 {
		t.Fatalf("result = %+v, want R3 alone", res)
	}
}

// Subsets never span customers (apply refuses that), and R4 scores a subset of the bound customer.
func TestSubsets_OneCustomerAndPayerIdentity(t *testing.T) {
	due := time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)
	far := due.AddDate(0, 0, 60)
	open := []Item{
		{ID: "a1", CustomerID: "A", Currency: "USD", DocumentNumber: "INV-A1", DueDate: due, OpenAmount: decimal.RequireFromString("40")},
		{ID: "a2", CustomerID: "A", Currency: "USD", DocumentNumber: "INV-A2", DueDate: due, OpenAmount: decimal.RequireFromString("60")},
		{ID: "b1", CustomerID: "B", Currency: "USD", DocumentNumber: "INV-B1", DueDate: due, OpenAmount: decimal.RequireFromString("60")},
	}
	res := Match(Payment{Amount: decimal.RequireFromString("100"), ValueDate: far, PayerCustomers: map[string]bool{"A": true}}, open)
	if len(res.Top) != 1 {
		t.Fatalf("candidates = %+v, want only the customer-A pair", res.Top)
	}
	c := res.Top[0]
	if strings.Join(c.ItemIDs, ",") != "a1,a2" || !hasRule(c.Rules, "R4") || c.Score != weightR2Subset+weightR4 {
		t.Fatalf("candidate = %+v, want a1+a2 with R2-SUBSET and R4", c)
	}
}

// R1 with R4 and R5 reaches the auto-apply threshold only when the amount agrees:
// a known payer quoting the invoice for its exact amount auto-applies, while a
// different amount stays a suggestion (the payer may have quoted the wrong invoice).
func TestPayerIdentity_ReferenceOnlyStaysSuggestedUnlessAmountAgrees(t *testing.T) {
	due := time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)
	it := Item{ID: "i-1", CustomerID: "c1", Currency: "USD", DocumentNumber: "INV-30001", DueDate: due, OpenAmount: decimal.RequireFromString("1000")}
	p := Payment{Amount: decimal.RequireFromString("400"), ValueDate: due.AddDate(0, 0, 30),
		Remittance: []RemittanceLine{{Reference: "INV-30001"}}, PayerName: "Acme Inc.",
		PayerCustomers: map[string]bool{"c1": true}, LearnedNames: map[string][]string{"c1": {"ACME INC"}}}
	res := Match(p, []Item{it})
	if res.Decision != DecisionSuggested || res.Top[0].Score != weightR1+weightR4+weightR5 {
		t.Fatalf("result = %+v, want SUGGESTED at 85 for a reference-only match", res)
	}
	p.Amount = it.OpenAmount
	if res := Match(p, []Item{it}); res.Decision != DecisionAutoApply || res.Top[0].Score != weightR1+weightR2Single+weightR4+weightR5 {
		t.Fatalf("exact amount: result = %+v, want AUTO_APPLY at 115", res)
	}
	p.Amount = decimal.RequireFromString("400")
	// Without the payer identity the reference alone is a suggestion.
	p.PayerCustomers, p.LearnedNames = nil, nil
	if res := Match(p, []Item{it}); res.Decision != DecisionSuggested {
		t.Fatalf("decision without identity = %s, want SUGGESTED", res.Decision)
	}
}

// A known payer paying two quoted invoices exactly auto-applies both. Hand-derived:
// the pair scores R2-SUBSET 25 + R1 60 + R4 15 + R5 10 = 110 and each invoice alone
// R1 60 + R4 15 + R5 10 = 85; the singles are part of the pair, not rivals, so the
// pair is the only set at 85 or more. Found by cash-app-500 in the store, where
// payers are learned on apply.
func TestPayerIdentity_MultiInvoiceStillAutoApplies(t *testing.T) {
	due := time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)
	open := []Item{
		{ID: "i-1", CustomerID: "c1", Currency: "EUR", DocumentNumber: "RE-40001", DueDate: due, OpenAmount: decimal.RequireFromString("1200.00")},
		{ID: "i-2", CustomerID: "c1", Currency: "EUR", DocumentNumber: "RE-40002", DueDate: due, OpenAmount: decimal.RequireFromString("380.50")},
	}
	p := Payment{Amount: decimal.RequireFromString("1580.50"), ValueDate: due.AddDate(0, 0, 30),
		Remittance: []RemittanceLine{{Reference: "RE-40001 RE-40002"}}, PayerName: "Kunde GmbH",
		PayerCustomers: map[string]bool{"c1": true}, LearnedNames: map[string][]string{"c1": {"KUNDE GMBH"}}}
	res := Match(p, open)
	if res.Decision != DecisionAutoApply || strings.Join(res.Top[0].ItemIDs, ",") != "i-1,i-2" || res.Top[0].Score != 110 {
		t.Fatalf("result = %+v, want AUTO_APPLY of both at 110", res)
	}
	if len(res.Top) != 3 || res.Top[1].Score != 85 || res.Top[2].Score != 85 {
		t.Fatalf("top = %+v, want both singles kept as candidates at 85", res.Top)
	}
	// A rival set outside the pair still blocks: a third quoted invoice of the same
	// amount as the pair scores R1 60 + R2 30 + R4 15 + R5 10 = 115 on its own.
	rival := Item{ID: "i-3", CustomerID: "c1", Currency: "EUR", DocumentNumber: "RE-40003", DueDate: due, OpenAmount: decimal.RequireFromString("1580.50")}
	p.Remittance = []RemittanceLine{{Reference: "RE-40001 RE-40002 RE-40003"}}
	if res := Match(p, append(open, rival)); res.Decision != DecisionSuggested {
		t.Fatalf("with a rival set: %+v, want SUGGESTED", res)
	}
}

func TestAccountHash_NormalizesAndHidesTheAccount(t *testing.T) {
	a := AccountHash("DE89 3704 0044 0532 0130 00")
	if a == "" || len(a) != 64 || strings.Contains(a, "3704") {
		t.Fatalf("hash = %q, want 64 hex characters", a)
	}
	if b := AccountHash("de89-3704-0044-0532-0130-00"); b != a {
		t.Fatalf("hash of the same account written differently = %q, want %q", b, a)
	}
	if AccountHash("DE89370400440532013001") == a {
		t.Fatal("another account hashes the same")
	}
	if AccountHash(" - ") != "" {
		t.Fatal("an account with no letters or digits must hash to empty")
	}
}

func TestLearnableName_NormalizesAndBounds(t *testing.T) {
	if got := LearnableName("  Acme, Inc. "); got != "ACME INC" {
		t.Fatalf("LearnableName = %q", got)
	}
	if got := LearnableName(strings.Repeat("Ä", 300)); len([]rune(got)) != MaxPayerNameLength {
		t.Fatalf("length = %d, want %d", len([]rune(got)), MaxPayerNameLength)
	}
	if LearnableName("...") != "" {
		t.Fatal("punctuation only must learn nothing")
	}
}

func TestPayerIdentity_LearnedPayerLiftsShortPayWithReasonToAutoApply(t *testing.T) {
	due := time.Date(2027, 3, 31, 0, 0, 0, 0, time.UTC)
	it := Item{ID: "i-1", CustomerID: "c1", Currency: "USD", DocumentNumber: "INV-30001", DueDate: due, OpenAmount: decimal.RequireFromString("1000")}
	p := Payment{Amount: decimal.RequireFromString("980"), ValueDate: due.AddDate(0, 0, 30), ShortPayReason: "DAMAGED GOODS",
		Remittance: []RemittanceLine{{Reference: "INV-30001"}}}
	if res := Match(p, []Item{it}); res.Decision != DecisionSuggested || res.Top[0].Score != weightR1+weightR7 {
		t.Fatalf("unlearned: %+v, want SUGGESTED at 80", res)
	}
	p.PayerCustomers = map[string]bool{"c1": true}
	if res := Match(p, []Item{it}); res.Decision != DecisionAutoApply || res.Top[0].Score != weightR1+weightR7+weightR4 {
		t.Fatalf("learned: %+v, want AUTO_APPLY at 95", res)
	}
}
