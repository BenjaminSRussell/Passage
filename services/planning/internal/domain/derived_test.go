package domain

import "testing"

// derivedComp is a component C bought on PO-9 (day 1, 200) that serves the
// dependent demand MO-1/C of the parent's production order MO-1.
func derivedComp(demandDate int, qty string) Item {
	return Item{ID: "C", Coverage: CoverageRequirement, OrderType: OrderPurchase,
		Demand:   []Requirement{{Ref: "MO-1/C", RefType: RefBOM, Date: demandDate, Qty: d(qty), Priority: PriorityDependent}},
		Receipts: []Requirement{{Ref: "PO-9", RefType: "purchase-line", Date: 1, Qty: d("200")}}}
}

// derivedParent is the parent P with its production order MO-1 (day 10, 100)
// and, unless demandQty is empty, one sales line.
func derivedParent(demandQty string, demandDate int) Item {
	it := Item{ID: "P", Coverage: CoverageRequirement, OrderType: OrderProduction, CoverageDays: 5, PositiveDays: 5,
		Components: []BOMLine{{ItemID: "C", Qty: d("2")}},
		Receipts:   []Requirement{{Ref: "MO-1", RefType: "production-order", Date: 10, Qty: d("100")}}}
	if demandQty != "" {
		it.Demand = []Requirement{{Ref: "SO-1", RefType: RefSales, Date: demandDate, Qty: d(demandQty), Priority: PrioritySales}}
	}
	return it
}

// FR-22.7 Derived: a component's existing receipt that serves the dependent
// demand "<parent supply>/<component>" of a parent receipt with a cancel,
// decrease, postpone or advance message gets a Derived message that names the
// parent's action and says what to do with the component receipt.
func TestRun_DerivedFollowsTheParentMessage(t *testing.T) {
	advanced := derivedParent("100", 12)
	advanced.Receipts[0].Date, advanced.NegativeDays = 20, 10
	lateComp := derivedComp(15, "200")
	lateComp.Receipts[0].Date = 14
	tests := []struct {
		name     string
		parent   Item
		comp     Item
		parentOf string
		wantDate int
		wantQty  string
		wantDays int
	}{
		// Nothing of MO-1 is pegged: cancel it, and reduce PO-9 by all it holds for MO-1.
		{"cancel reduces the component receipt by the pegged quantity", derivedParent("", 0), derivedComp(10, "200"), ActionCancel, 1, "200", 0},
		// MO-1 loses 60 of 100: PO-9 loses 60% of the 200 it holds for MO-1.
		{"decrease removes the same share of the component", derivedParent("40", 12), derivedComp(10, "200"), ActionDecrease, 1, "120", 0},
		// MO-1 moves 10 days later (day 10 to 20): PO-9 follows by 10 days, keeping its slack.
		{"postpone moves the component later by the parent's shift", derivedParent("100", 20), derivedComp(10, "200"), ActionPostpone, 11, "200", 10},
		// MO-1 moves 8 days earlier (day 20 to 12): its components are issued on day 7, so PO-9 (day 14) must come by day 7.
		{"advance brings a component receipt that would be late forward", advanced, lateComp, ActionAdvance, 7, "200", 7},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Run(0, []Item{tc.parent, tc.comp})
			if err != nil {
				t.Fatal(err)
			}
			if got := messagesOf(r, tc.parentOf); len(got) != 1 || got[0].SupplyRef != "MO-1" {
				t.Fatalf("%s messages = %+v; want one for MO-1", tc.parentOf, got)
			}
			der := messagesOf(r, ActionDerived)
			if len(der) != 1 {
				t.Fatalf("derived messages = %+v; want one", der)
			}
			m := der[0]
			if m.ItemID != "C" || m.SupplyRef != "PO-9" || m.DemandRef != "MO-1/C" || m.ParentAction != tc.parentOf ||
				m.SuggestedDate != tc.wantDate || !m.Qty.Equal(d(tc.wantQty)) || m.DelayDays != tc.wantDays {
				t.Fatalf("derived = %+v", m)
			}
		})
	}
}

// A parent receipt with both a decrease and a postpone passes both on, one
// Derived message each.
func TestRun_DerivedOneMessagePerParentAction(t *testing.T) {
	r, err := Run(0, []Item{derivedParent("40", 20), derivedComp(10, "200")})
	if err != nil {
		t.Fatal(err)
	}
	der := messagesOf(r, ActionDerived)
	if len(der) != 2 {
		t.Fatalf("derived = %+v; want a decrease and a postpone", der)
	}
	got := map[string]Message{}
	for _, m := range der {
		got[m.ParentAction] = m
	}
	if m := got[ActionDecrease]; !m.Qty.Equal(d("120")) || m.SuggestedDate != 1 || m.DelayDays != 0 {
		t.Fatalf("derived decrease = %+v", m)
	}
	if m := got[ActionPostpone]; !m.Qty.Equal(d("200")) || m.SuggestedDate != 11 || m.DelayDays != 10 {
		t.Fatalf("derived postpone = %+v", m)
	}
}

// A decrease share is rounded half away from zero to the 6 decimals a stored
// quantity keeps: MO-1 loses 2 of 3, so 1 pegged unit of PO-9 loses 0.666667.
func TestRun_DerivedDecreaseShareIsRounded(t *testing.T) {
	parent := derivedParent("1", 12)
	parent.Receipts[0].Qty = d("3")
	r, err := Run(0, []Item{parent, derivedComp(10, "1")})
	if err != nil {
		t.Fatal(err)
	}
	der := messagesOf(r, ActionDerived)
	if len(der) != 1 || !der[0].Qty.Equal(d("0.666667")) {
		t.Fatalf("derived = %+v; want 0.666667", der)
	}
}

// Two cancelled parents pegged to one component receipt raise one message
// each, naming its own dependent demand, so neither is counted twice; a
// parent ref that holds a "/" is still found.
func TestRun_DerivedPerParentOnASharedComponentReceipt(t *testing.T) {
	parent := Item{ID: "P", Coverage: CoverageRequirement, OrderType: OrderProduction, CoverageDays: 5,
		Components: []BOMLine{{ItemID: "C", Qty: d("1")}},
		Receipts: []Requirement{
			{Ref: "MO-1", RefType: "production-order", Date: 10, Qty: d("50")},
			{Ref: "MO-2/10", RefType: "production-order", Date: 12, Qty: d("70")},
		}}
	comp := Item{ID: "C", Coverage: CoverageRequirement, OrderType: OrderPurchase,
		Demand: []Requirement{
			{Ref: "MO-1/C", RefType: RefBOM, Date: 8, Qty: d("50"), Priority: PriorityDependent},
			{Ref: "MO-2/10/C", RefType: RefBOM, Date: 9, Qty: d("70"), Priority: PriorityDependent},
		},
		Receipts: []Requirement{{Ref: "PO-9", RefType: "purchase-line", Date: 1, Qty: d("120")}}}
	r, err := Run(0, []Item{parent, comp})
	if err != nil {
		t.Fatal(err)
	}
	der := messagesOf(r, ActionDerived)
	if len(der) != 2 || der[0].DemandRef != "MO-1/C" || !der[0].Qty.Equal(d("50")) || der[1].DemandRef != "MO-2/10/C" || !der[1].Qty.Equal(d("70")) {
		t.Fatalf("derived = %+v; want 50 for MO-1/C and 70 for MO-2/10/C", der)
	}
	for _, m := range der {
		if m.SupplyRef != "PO-9" || m.ParentAction != ActionCancel {
			t.Fatalf("derived = %+v", m)
		}
	}
}

// When receipts of two items share the parent ref, the item whose BOM lists
// the component is the parent: X's postpone of its own R-1 is not passed on.
func TestRun_DerivedParentByBOMWhenRefsCollide(t *testing.T) {
	parent := derivedParent("", 0)
	parent.Receipts[0].Ref = "R-1"
	other := Item{ID: "X", Coverage: CoverageRequirement, OrderType: OrderPurchase, PositiveDays: 5,
		Demand:   []Requirement{{Ref: "SO-7", RefType: RefSales, Date: 30, Qty: d("5"), Priority: PrioritySales}},
		Receipts: []Requirement{{Ref: "R-1", RefType: "purchase-line", Date: 10, Qty: d("5")}}}
	comp := derivedComp(10, "200")
	comp.Demand[0].Ref = "R-1/C"
	r, err := Run(0, []Item{parent, other, comp})
	if err != nil {
		t.Fatal(err)
	}
	if got := messagesOf(r, ActionPostpone); len(got) != 1 || got[0].ItemID != "X" {
		t.Fatalf("postpone = %+v; want X's", got)
	}
	der := messagesOf(r, ActionDerived)
	if len(der) != 1 || der[0].ParentAction != ActionCancel {
		t.Fatalf("derived = %+v; want only P's cancel", der)
	}
}

func TestRun_DerivedNeedsAParentMessageAndAnExistingReceipt(t *testing.T) {
	// The parent receipt is pegged and on time: no parent message, so no Derived.
	parent := Item{ID: "P", Coverage: CoverageRequirement, OrderType: OrderProduction,
		Demand:   []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 10, Qty: d("100"), Priority: PrioritySales}},
		Receipts: []Requirement{{Ref: "MO-1", RefType: "production-order", Date: 10, Qty: d("100")}}}
	comp := Item{ID: "C", Coverage: CoverageRequirement, OrderType: OrderPurchase,
		Demand:   []Requirement{{Ref: "MO-1/C", RefType: RefBOM, Date: 10, Qty: d("200"), Priority: PriorityDependent}},
		Receipts: []Requirement{{Ref: "PO-9", RefType: "purchase-line", Date: 1, Qty: d("200")}}}
	r, err := Run(0, []Item{parent, comp})
	if err != nil {
		t.Fatal(err)
	}
	if got := messagesOf(r, ActionDerived); len(got) != 0 {
		t.Fatalf("derived = %+v; want none", got)
	}
	// A cancelled parent whose component has no receipt (the run plans an order for it) gets none.
	parent.Demand = nil
	parent.CoverageDays = 5
	noReceipt := comp
	noReceipt.Receipts = nil
	if r, err = Run(0, []Item{parent, noReceipt}); err != nil {
		t.Fatal(err)
	}
	if got := messagesOf(r, ActionCancel); len(got) != 1 {
		t.Fatalf("cancel = %+v", got)
	}
	if got := messagesOf(r, ActionDerived); len(got) != 0 {
		t.Fatalf("derived for a planned component order = %+v; want none", got)
	}
	// BR-22.4: a component receipt outside its action fence gets none.
	fenced := comp
	fenced.ActionDays = 1
	if r, err = Run(0, []Item{parent, fenced}); err != nil {
		t.Fatal(err)
	}
	if got := messagesOf(r, ActionDerived); len(got) != 0 {
		t.Fatalf("derived outside the action fence = %+v; want none", got)
	}
	// Demand that is not "<parent ref>/<this component>" is not linked.
	other := comp
	other.Demand = []Requirement{{Ref: "MO-1/D", RefType: RefBOM, Date: 10, Qty: d("200"), Priority: PriorityDependent}}
	if r, err = Run(0, []Item{parent, other}); err != nil {
		t.Fatal(err)
	}
	if got := messagesOf(r, ActionDerived); len(got) != 0 {
		t.Fatalf("derived for another component's demand = %+v; want none", got)
	}
}

// An advanced parent leaves a component receipt that is still on time alone.
func TestRun_DerivedAdvanceOnlyWhenTheComponentWouldBeLate(t *testing.T) {
	parent := derivedParent("100", 12)
	parent.Receipts[0].Date, parent.NegativeDays = 20, 10
	r, err := Run(0, []Item{parent, derivedComp(15, "200")})
	if err != nil {
		t.Fatal(err)
	}
	if got := messagesOf(r, ActionAdvance); len(got) != 1 {
		t.Fatalf("advance = %+v", got)
	}
	if got := messagesOf(r, ActionDerived); len(got) != 0 {
		t.Fatalf("derived = %+v; PO-9 on day 1 is not late for day 7", got)
	}
}
