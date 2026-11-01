package domain

import "testing"

func TestValidateAlternativeRule_Table(t *testing.T) {
	cases := []struct {
		name string
		r    AlternativeRule
		keys []string
	}{
		{"valid", AlternativeRule{OrderedProductID: "a", AlternativeProductID: "b", Priority: 1}, nil},
		{"valid with validity", AlternativeRule{OrderedProductID: "a", AlternativeProductID: "b", Priority: 99, ValidFrom: sp("2027-03-01"), ValidTo: sp("2027-03-01")}, nil},
		{"same product", AlternativeRule{OrderedProductID: "a", AlternativeProductID: "a", Priority: 1}, []string{"alternativeProductId"}},
		{"missing products and priority", AlternativeRule{}, []string{"orderedProductId", "alternativeProductId", "priority"}},
		{"priority 100", AlternativeRule{OrderedProductID: "a", AlternativeProductID: "b", Priority: 100}, []string{"priority"}},
		{"bad dates", AlternativeRule{OrderedProductID: "a", AlternativeProductID: "b", Priority: 1, ValidFrom: sp("03/01"), ValidTo: sp("x")}, []string{"validFrom", "validTo"}},
		{"to before from", AlternativeRule{OrderedProductID: "a", AlternativeProductID: "b", Priority: 1, ValidFrom: sp("2027-03-02"), ValidTo: sp("2027-03-01")}, []string{"validTo"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ValidateAlternativeRule(tc.r)
			if len(got) != len(tc.keys) {
				t.Fatalf("fields %v, want %v", got, tc.keys)
			}
			for _, k := range tc.keys {
				if _, ok := got[k]; !ok {
					t.Fatalf("missing %q in %v", k, got)
				}
			}
		})
	}
}

func TestApplicableAlternatives(t *testing.T) {
	c1, c2 := "c1", "c2"
	rules := []AlternativeRule{
		{ID: "r1", OrderedProductID: "A", AlternativeProductID: "B", Priority: 2},
		{ID: "r2", OrderedProductID: "A", AlternativeProductID: "B", Priority: 3, CustomerID: &c1},
		{ID: "r3", OrderedProductID: "A", AlternativeProductID: "C", Priority: 1, CustomerID: &c2},
		{ID: "r4", OrderedProductID: "A", AlternativeProductID: "D", Priority: 1, ValidFrom: sp("2027-04-01")},
		{ID: "r5", OrderedProductID: "A", AlternativeProductID: "E", Priority: 1, ValidTo: sp("2027-03-01")},
		{ID: "r6", OrderedProductID: "X", AlternativeProductID: "F", Priority: 1},
		{ID: "r7", OrderedProductID: "A", AlternativeProductID: "G", Priority: 2, CustomerID: &c1},
	}
	got := ApplicableAlternatives(rules, c1, "A", day("2027-03-01"))
	var ids []string
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	// r2 (own rule) replaces r1 for B; r3 is another customer's; r4 not yet valid; r6 other product.
	// Priority 2: r7 (own) before nothing else; E (r5) is valid on its last day with priority 1.
	want := []string{"r5", "r7", "r2"}
	if len(ids) != len(want) {
		t.Fatalf("got %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("got %v, want %v", ids, want)
		}
	}
	if got := ApplicableAlternatives(rules, "", "A", day("2027-03-01")); len(got) != 2 {
		t.Fatalf("without a customer only the general rules apply, got %v", got)
	}
	if got := ApplicableAlternatives(rules, c1, "A", day("2027-03-02")); len(got) != 2 || got[0].ID != "r7" || got[1].ID != "r2" {
		t.Fatalf("on 03-02 the rule ending 03-01 is out, got %v", got)
	}
}

func TestAvailableOnDate_IgnoresLaterSupply(t *testing.T) {
	periods := []Period{{Date: day("2027-03-01"), Supply: dec("30")}, {Date: day("2027-03-10"), Supply: dec("50")}}
	p, err := PromiseDate(periods, day("2027-03-02"), dec("60"), false)
	if err != nil {
		t.Fatal(err)
	}
	if got := AvailableOnDate(p, day("2027-03-02")); !got.Equal(dec("30")) {
		t.Fatalf("available on date %s, want 30", got)
	}
	if got := AvailableOnDate(p, day("2027-03-10")); !got.Equal(dec("60")) {
		t.Fatalf("available by 03-10 %s, want 60", got)
	}
}

func TestOfferAlternatives_CoveringFirstThenPriority(t *testing.T) {
	rules := []AlternativeRule{
		{ID: "r1", AlternativeProductID: "B", Priority: 1},
		{ID: "r2", AlternativeProductID: "C", Priority: 2},
		{ID: "r3", AlternativeProductID: "D", Priority: 3},
		{ID: "r4", AlternativeProductID: "E", Priority: 4},
		{ID: "r5", AlternativeProductID: "F", Priority: 5},
	}
	on := day("2027-03-01")
	supplies := map[string][]Period{
		"B": {{Date: on, Supply: dec("10")}},                    // partial: 10 of 25
		"C": {{Date: on, Supply: dec("25")}},                    // covers
		"D": {{Date: day("2027-03-20"), Supply: dec("100")}},    // only later supply
		"E": {{Date: on, Supply: dec("30"), Demand: dec("30")}}, // nothing free
		// F has no buckets
	}
	offers, unchecked := OfferAlternatives(rules, supplies, on, dec("25"))
	if len(offers) != 2 || offers[0].AlternativeProductID != "C" || !offers[0].CoversQuantity || offers[1].AlternativeProductID != "B" || offers[1].CoversQuantity {
		t.Fatalf("offers %+v", offers)
	}
	if !offers[1].AvailableOnDate.Equal(dec("10")) {
		t.Fatalf("partial offer %s, want 10", offers[1].AvailableOnDate)
	}
	if len(unchecked) != 1 || unchecked[0].ID != "r5" {
		t.Fatalf("unchecked %+v", unchecked)
	}
}
