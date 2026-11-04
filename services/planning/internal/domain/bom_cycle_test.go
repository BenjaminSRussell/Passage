package domain

import (
	"slices"
	"testing"
)

// PLAN-0004 (BR-22.1 step 2): a cyclic BOM is planned, the line that closes the
// cycle is not exploded, and each such line raises a warning on its parent.

func cycleItems() []Item {
	return []Item{
		{ID: "A", Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 1,
			Demand:     []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 10, Qty: d("5"), Priority: PrioritySales}},
			Components: []BOMLine{{ItemID: "B", Qty: d("2")}}},
		{ID: "B", Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 1, Components: []BOMLine{{ItemID: "A", Qty: d("1")}}},
	}
}

func TestRun_CyclicBOMWarnsAndPlansTheRest(t *testing.T) {
	r, err := Run(0, cycleItems())
	if err != nil {
		t.Fatal(err)
	}
	got := messagesOf(r, ActionBOMCycle)
	if len(got) != 1 || got[0].ItemID != "B" || got[0].DemandRef != "A" || !got[0].Qty.Equal(d("1")) {
		t.Fatalf("bom-cycle messages %+v; want one on parent B naming component A", got)
	}
	if len(r.Orders) != 2 {
		t.Fatalf("orders %+v; want one for A and one for its component B", r.Orders)
	}
	again, _ := Run(0, cycleItems())
	if again.Hash != r.Hash {
		t.Fatal("a cyclic run must be deterministic")
	}
	if CatalogCode(ActionBOMCycle) != "PLAN-0004" {
		t.Fatalf("code %q", CatalogCode(ActionBOMCycle))
	}
	if text := CatalogText(MessageView{Action: ActionBOMCycle, ItemID: "B"}); text != "BOM cycle detected for B; low-level codes not updated." {
		t.Fatalf("text %q", text)
	}
	if MessageActionable(ActionBOMCycle) {
		t.Fatal("a bom-cycle warning changes no source order")
	}
}

func TestRun_SelfReferencingComponentIsCut(t *testing.T) {
	items := []Item{{ID: "A", Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 1,
		Demand:     []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 10, Qty: d("5"), Priority: PrioritySales}},
		Components: []BOMLine{{ItemID: "A", Qty: d("1")}}}}
	r, err := Run(0, items)
	if err != nil || len(messagesOf(r, ActionBOMCycle)) != 1 || len(r.Orders) != 1 {
		t.Fatalf("result %+v err %v; want one order and one warning", r, err)
	}
}

func TestRun_AcyclicBOMRaisesNoCycleWarning(t *testing.T) {
	items := cycleItems()
	items[1].Components = nil
	r, err := Run(0, items)
	if err != nil || len(messagesOf(r, ActionBOMCycle)) != 0 {
		t.Fatalf("messages %+v err %v; want none", r.Messages, err)
	}
}

func TestLowLevelCodes_CyclicBOMGetsAcyclicCodes(t *testing.T) {
	items := cycleItems()
	llc, err := LowLevelCodes(items)
	if err != nil || llc["A"] != 0 || llc["B"] != 1 {
		t.Fatalf("llc %v err %v; want A 0, B 1", llc, err)
	}
	if len(items[1].Components) != 1 {
		t.Fatal("LowLevelCodes must not change the caller's items")
	}
}

// rootedCycleItems: FG (top level, sales demand) uses A; A uses B and C; B uses A
// (a two-item cycle under FG); C uses itself. Ids sort B < A would start a walk in
// the middle of the cycle if the snapshot order were followed.
func rootedCycleItems() []Item {
	return []Item{
		{ID: "FG", Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 1,
			Demand:     []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 20, Qty: d("4"), Priority: PrioritySales}},
			Components: []BOMLine{{ItemID: "A", Qty: d("1")}}},
		{ID: "A", Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 1,
			Components: []BOMLine{{ItemID: "B", Qty: d("2")}, {ItemID: "C", Qty: d("1")}}},
		{ID: "B", Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 1, Components: []BOMLine{{ItemID: "A", Qty: d("3")}}},
		{ID: "C", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 1, Components: []BOMLine{{ItemID: "C", Qty: d("1")}}},
	}
}

// The cut must not depend on the order the API happens to send or load the
// snapshot and its lines in: every permutation cuts the same lines and gives the
// same plan hash.
func TestRun_BOMCycleCutIsIndependentOfSnapshotOrder(t *testing.T) {
	base, err := Run(0, rootedCycleItems())
	if err != nil {
		t.Fatal(err)
	}
	reversed := rootedCycleItems()
	slices.Reverse(reversed)
	for i := range reversed {
		slices.Reverse(reversed[i].Components)
	}
	rotated := rootedCycleItems()
	rotated = append(rotated[2:], rotated[:2]...)
	for name, items := range map[string][]Item{"reversed": reversed, "rotated": rotated} {
		r, err := Run(0, items)
		if err != nil {
			t.Fatal(err)
		}
		if r.Hash != base.Hash {
			t.Fatalf("%s: hash %s, want %s; messages %+v vs %+v", name, r.Hash, base.Hash, r.Messages, base.Messages)
		}
	}
}

// A cycle under a top-level item is cut on the line that leads back up (B uses A),
// so FG's demand still reaches A, B and C; the self-loop of C is cut too, and each
// cut line raises its own warning.
func TestRun_CycleUnderATopLevelItemKeepsTheDemandPath(t *testing.T) {
	r, err := Run(0, rootedCycleItems())
	if err != nil {
		t.Fatal(err)
	}
	got := messagesOf(r, ActionBOMCycle)
	if len(got) != 2 || got[0].ItemID != "B" || got[0].DemandRef != "A" || got[1].ItemID != "C" || got[1].DemandRef != "C" {
		t.Fatalf("bom-cycle messages %+v; want B->A and C->C", got)
	}
	planned := map[string]bool{}
	for _, o := range r.Orders {
		planned[o.ItemID] = true
	}
	for _, id := range []string{"FG", "A", "B", "C"} {
		if !planned[id] {
			t.Fatalf("no planned order for %s: %+v", id, r.Orders)
		}
	}
	llc, err := LowLevelCodes(rootedCycleItems())
	if err != nil || llc["FG"] != 0 || llc["A"] != 1 || llc["B"] != 2 || llc["C"] != 2 {
		t.Fatalf("llc %v err %v; want FG 0, A 1, B 2, C 2", llc, err)
	}
}

// Two disjoint cycles with no top-level item, one of three items: the walk starts
// at the smallest id of each, ends, and cuts exactly one line per cycle.
func TestRun_EveryCycleIsCutOnce(t *testing.T) {
	line := func(id string) []BOMLine { return []BOMLine{{ItemID: id, Qty: d("1")}} }
	item := func(id, comp string) Item {
		return Item{ID: id, Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 1, Components: line(comp)}
	}
	items := []Item{item("Z", "X"), item("Y", "Z"), item("X", "Y"), item("Q", "P"), item("P", "Q")}
	r, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	got := messagesOf(r, ActionBOMCycle)
	if len(got) != 2 || got[0].ItemID != "Q" || got[0].DemandRef != "P" || got[1].ItemID != "Z" || got[1].DemandRef != "X" {
		t.Fatalf("bom-cycle messages %+v; want Q->P and Z->X", got)
	}
}
