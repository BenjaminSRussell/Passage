package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func bufferItem(id, order string, comps ...BOMLine) Item {
	return Item{ID: id, Coverage: CoverageMinMax, OrderType: order, LeadTimeDays: 5, OnHand: dec("40"), SafetyStock: dec("10"),
		ReorderMin: dec("50"), ReorderMax: dec("200"),
		Demand:   []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 3, Qty: dec("500"), Priority: PrioritySales}},
		Receipts: []Requirement{{Ref: "PO-1", RefType: "purchase-order", Date: 2, Qty: dec("30")}}, Components: comps}
}

// AC-22.12 on the run: the buffer of BR-22.11 orders 115, and the item's own
// demand, on-hand, receipts and min/max play no part.
func TestApplyBuffers_ReplenishesByBufferQuantity(t *testing.T) {
	in := []Item{bufferItem("FG", OrderPurchase)}
	items := ApplyBuffers(in, []RunBuffer{{ItemID: "FG", DLTDays: dec("12"), PlannedQty: dec("115"), Zone: ZoneYellow}})
	if len(in[0].Demand) != 1 || in[0].Demand[0].Ref != "SO-1" {
		t.Fatal("input was modified")
	}
	res, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Orders) != 1 {
		t.Fatalf("orders %+v", res.Orders)
	}
	o := res.Orders[0]
	if !o.Qty.Equal(dec("115")) || o.DeliveryDate != 12 || o.OrderDate != 7 || o.PlanningPriority != nil {
		t.Fatalf("order %+v", o)
	}
	if len(res.Pegging) != 1 || res.Pegging[0].DemandRefType != RefBuffer || !res.Pegging[0].Qty.Equal(dec("115")) {
		t.Fatalf("pegging %+v", res.Pegging)
	}
	if len(res.Messages) != 0 {
		t.Fatalf("yellow zone raises no message: %+v", res.Messages)
	}
}

func TestApplyBuffers_DueNoEarlierThanTheLeadTime(t *testing.T) {
	items := ApplyBuffers([]Item{bufferItem("FG", OrderPurchase)}, []RunBuffer{{ItemID: "FG", DLTDays: dec("2.2"), PlannedQty: dec("10"), Zone: ZoneYellow}})
	res, err := Run(0, items)
	if err != nil || len(res.Orders) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if o := res.Orders[0]; o.OrderDate != 0 || o.DeliveryDate != 5 {
		t.Fatalf("order %+v; the lead time of 5 days is the earliest delivery", o)
	}
}

func TestApplyBuffers_GreenBufferPlansNothing(t *testing.T) {
	items := ApplyBuffers([]Item{bufferItem("FG", OrderPurchase)}, []RunBuffer{{ItemID: "FG", DLTDays: dec("12"), PlannedQty: decimal.Zero, Zone: ZoneGreen}})
	res, err := Run(0, items)
	if err != nil || len(res.Orders) != 0 || len(res.Messages) != 0 {
		t.Fatalf("%+v %v", res, err)
	}
}

// A decoupling point absorbs the demand above it, and its own BOM still explodes.
func TestApplyBuffers_DecouplesParentDemandAndExplodesOwnBOM(t *testing.T) {
	parent := Item{ID: "TOP", Coverage: CoverageRequirement, OrderType: OrderProduction, LeadTimeDays: 2,
		Demand:     []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 10, Qty: dec("20"), Priority: PrioritySales}},
		Components: []BOMLine{{ItemID: "SUB", Qty: dec("1")}}}
	sub := bufferItem("SUB", OrderProduction, BOMLine{ItemID: "RAW", Qty: dec("2")})
	raw := Item{ID: "RAW", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 1}
	items := ApplyBuffers([]Item{parent, sub, raw}, []RunBuffer{{ItemID: "SUB", DLTDays: dec("6"), PlannedQty: dec("30"), Zone: ZoneYellow}})
	res, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	qty := map[string]decimal.Decimal{}
	for _, o := range res.Orders {
		qty[o.ItemID] = qty[o.ItemID].Add(o.Qty)
	}
	if !qty["TOP"].Equal(dec("20")) || !qty["SUB"].Equal(dec("30")) || !qty["RAW"].Equal(dec("60")) {
		t.Fatalf("order quantities %v: TOP 20, SUB 30 (the buffer, not the 20 TOP needs), RAW 60", qty)
	}
}

func TestApplyBuffers_IgnoresItemsWithoutBuffer(t *testing.T) {
	in := []Item{bufferItem("A", OrderPurchase)}
	if got := ApplyBuffers(in, nil); len(got) != 1 || len(got[0].Demand) != 1 {
		t.Fatal("no buffers must leave the snapshot as it is")
	}
	got := ApplyBuffers(in, []RunBuffer{{ItemID: "OTHER", PlannedQty: dec("5"), Zone: ZoneRed}})
	if !got[0].OnHand.Equal(dec("40")) || got[0].Coverage != CoverageMinMax {
		t.Fatalf("item without a buffer changed: %+v", got[0])
	}
}

// AC-22.12: the order that replenishes the BR-22.11 buffer carries priority
// 0.5306 and zone yellow; a yellow buffer raises no message.
func TestAddBufferResults_StampsPriorityAndZone(t *testing.T) {
	buf := []RunBuffer{{ItemID: "FG", DLTDays: dec("10"), PlannedQty: dec("115"), Priority: dec("0.5306"), Zone: ZoneYellow}}
	other := Item{ID: "OTHER", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 1,
		Demand: []Requirement{{Ref: "SO-2", RefType: RefSales, Date: 4, Qty: dec("3"), Priority: PrioritySales}}}
	res, err := Run(0, ApplyBuffers([]Item{bufferItem("FG", OrderPurchase), other}, buf))
	if err != nil {
		t.Fatal(err)
	}
	before := res.Hash
	res = AddBufferResults(res, buf)
	for _, o := range res.Orders {
		switch o.ItemID {
		case "FG":
			if o.PlanningPriority == nil || !o.PlanningPriority.Equal(dec("0.5306")) || o.BufferZone != ZoneYellow || !o.Qty.Equal(dec("115")) {
				t.Fatalf("buffer order %+v", o)
			}
		default:
			if o.PlanningPriority != nil || o.BufferZone != "" {
				t.Fatalf("an order without a buffer got a priority: %+v", o)
			}
		}
	}
	if len(res.Messages) != 0 {
		t.Fatalf("yellow zone raises no message: %+v", res.Messages)
	}
	if res.Hash == before {
		t.Fatal("the plan hash must cover the stamped priority")
	}
	if got := AddBufferResults(res, nil); got.Hash != res.Hash {
		t.Fatal("no buffers must leave the result as it is")
	}
}

// PLAN-0010: a red-zone buffer raises one message on the replenishing order.
func TestAddBufferResults_RedZone(t *testing.T) {
	buf := []RunBuffer{{ItemID: "FG", DLTDays: dec("10"), PlannedQty: dec("80"), Priority: dec("0.2"), Zone: ZoneRed}}
	res, err := Run(0, ApplyBuffers([]Item{bufferItem("FG", OrderPurchase)}, buf))
	if err != nil {
		t.Fatal(err)
	}
	before := res.Hash
	res = AddBufferResults(res, buf)
	if len(res.Messages) != 1 {
		t.Fatalf("messages %+v", res.Messages)
	}
	m := res.Messages[0]
	if m.Action != ActionRedZone || m.ItemID != "FG" || m.SupplyRef != res.Orders[0].Number || !m.Qty.Equal(dec("80")) || m.SuggestedDate != 10 {
		t.Fatalf("message %+v", m)
	}
	if res.Hash == before {
		t.Fatal("the plan hash must cover the message")
	}
}
