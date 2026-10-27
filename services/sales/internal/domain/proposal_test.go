package domain

import (
	"reflect"
	"testing"

	"pgregory.net/rapid"
)

func strPtr(s string) *string { return &s }

func TestEvaluateProposals(t *testing.T) {
	rules := []ProposalRule{
		{ID: "P1", OrderedProductID: "FG-100", ProposedProductID: "ACC-1", Reason: ProposalAccessory},
		{ID: "P2", OrderedProductID: "FG-100", ProposedProductID: "UP-9", Reason: ProposalUpSell, CustomerID: strPtr("C9")},
		{ID: "P3", OrderedProductID: "FG-100", ProposedProductID: "ACC-1", Reason: ProposalCrossSell},
		{ID: "P4", OrderedProductID: "FG-120", ProposedProductID: "ACC-2", Reason: ProposalCrossSell},
		{ID: "P6", OrderedProductID: "FG-130", ProposedProductID: "FG-120", Reason: ProposalCrossSell},
		{ID: "P7", OrderedProductID: "FG-120", ProposedProductID: "ACC-1", Reason: ProposalUpSell, CustomerID: strPtr("C7")},
	}
	cases := []struct {
		name      string
		customer  string
		committed []string
		current   []string
		want      []Proposal
	}{
		{name: "accessory proposed once although two rules name it", customer: "C1", committed: []string{"FG-100"},
			want: []Proposal{{RuleID: "P1", OrderedProductID: "FG-100", ProposedProductID: "ACC-1", Reason: ProposalAccessory}}},
		{name: "customer rule only for its customer", customer: "C9", committed: []string{"FG-100"},
			want: []Proposal{
				{RuleID: "P1", OrderedProductID: "FG-100", ProposedProductID: "ACC-1", Reason: ProposalAccessory},
				{RuleID: "P2", OrderedProductID: "FG-100", ProposedProductID: "UP-9", Reason: ProposalUpSell},
			}},
		{name: "no customer sees only the rules for every customer", committed: []string{"FG-100"},
			want: []Proposal{{RuleID: "P1", OrderedProductID: "FG-100", ProposedProductID: "ACC-1", Reason: ProposalAccessory}}},
		{name: "product already on the order is not proposed", customer: "C1", committed: []string{"FG-100"}, current: []string{"ACC-1"},
			want: []Proposal{}},
		{name: "committed product is never proposed back", customer: "C1", committed: []string{"FG-130", "FG-120"},
			want: []Proposal{{RuleID: "P4", OrderedProductID: "FG-120", ProposedProductID: "ACC-2", Reason: ProposalCrossSell}}},
		{name: "customer's own rule wins over the rule for every customer", customer: "C7", committed: []string{"FG-100", "FG-120"},
			want: []Proposal{
				{RuleID: "P4", OrderedProductID: "FG-120", ProposedProductID: "ACC-2", Reason: ProposalCrossSell},
				{RuleID: "P7", OrderedProductID: "FG-120", ProposedProductID: "ACC-1", Reason: ProposalUpSell},
			}},
		{name: "a product committed twice proposes once", customer: "C1", committed: []string{"FG-120", "FG-120"},
			want: []Proposal{{RuleID: "P4", OrderedProductID: "FG-120", ProposedProductID: "ACC-2", Reason: ProposalCrossSell}}},
		{name: "no committing product, no proposals", customer: "C1", want: []Proposal{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := EvaluateProposals(rules, tc.customer, tc.committed, tc.current)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestEvaluateProposals_Property: whatever the rules, a proposal never names a
// product on the order or being committed, never repeats a product, and always
// comes from an applicable rule.
func TestEvaluateProposals_Property(t *testing.T) {
	products := []string{"A", "B", "C", "D", "E", "F"}
	customers := []string{"C1", "C2"}
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 12).Draw(t, "rules")
		rules := make([]ProposalRule, 0, n)
		for i := 0; i < n; i++ {
			r := ProposalRule{ID: string(rune('a' + i)), OrderedProductID: rapid.SampledFrom(products).Draw(t, "ordered"),
				ProposedProductID: rapid.SampledFrom(products).Draw(t, "proposed"), Reason: ProposalCrossSell}
			if rapid.Bool().Draw(t, "own") {
				r.CustomerID = strPtr(rapid.SampledFrom(customers).Draw(t, "ruleCustomer"))
			}
			rules = append(rules, r)
		}
		committing := rapid.SliceOfN(rapid.SampledFrom(products), 1, 3).Draw(t, "committing")
		current := rapid.SliceOfN(rapid.SampledFrom(products), 0, 3).Draw(t, "current")
		customer := rapid.SampledFrom(customers).Draw(t, "customer")
		byID := map[string]ProposalRule{}
		for _, r := range rules {
			byID[r.ID] = r
		}
		onOrder := map[string]bool{}
		for _, p := range append(append([]string{}, committing...), current...) {
			onOrder[p] = true
		}
		seen := map[string]bool{}
		for _, p := range EvaluateProposals(rules, customer, committing, current) {
			if onOrder[p.ProposedProductID] {
				t.Fatalf("proposed %s which is on the order", p.ProposedProductID)
			}
			if seen[p.ProposedProductID] {
				t.Fatalf("proposed %s twice", p.ProposedProductID)
			}
			seen[p.ProposedProductID] = true
			r := byID[p.RuleID]
			if r.OrderedProductID != p.OrderedProductID || r.ProposedProductID != p.ProposedProductID {
				t.Fatalf("proposal %+v does not match its rule %+v", p, r)
			}
			if r.CustomerID != nil && *r.CustomerID != customer {
				t.Fatalf("rule %s of customer %s applied to %s", r.ID, *r.CustomerID, customer)
			}
		}
	})
}

func TestValidateProposalRule(t *testing.T) {
	cases := []struct {
		name string
		rule ProposalRule
		keys []string
	}{
		{name: "valid", rule: ProposalRule{OrderedProductID: "a", ProposedProductID: "b", Reason: ProposalUpSell}},
		{name: "unknown reason", rule: ProposalRule{OrderedProductID: "a", ProposedProductID: "b", Reason: "BUNDLE"}, keys: []string{"reason"}},
		{name: "self proposal", rule: ProposalRule{OrderedProductID: "a", ProposedProductID: "a", Reason: ProposalCrossSell}, keys: []string{"proposedProductId"}},
		{name: "missing products", rule: ProposalRule{Reason: ProposalCrossSell}, keys: []string{"orderedProductId", "proposedProductId"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateProposalRule(tc.rule)
			if len(got) != len(tc.keys) {
				t.Fatalf("fields %v, want keys %v", got, tc.keys)
			}
			for _, k := range tc.keys {
				if _, ok := got[k]; !ok {
					t.Fatalf("missing field error %q in %v", k, got)
				}
			}
		})
	}
}
