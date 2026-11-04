package domain

import (
	"testing"
)

// PLAN-0005 (FR-22.4): a requirement above the maximum order quantity raises one
// warning naming its first order, the number of orders and the maximum.

func splitItem(max string) Item {
	return Item{ID: "P", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 2, MaxOrderQty: d(max),
		Demand: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 10, Qty: d("250"), Priority: PrioritySales}}}
}

func TestRun_LotSplitRaisesOneWarning(t *testing.T) {
	r, err := Run(0, []Item{splitItem("100")})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orders) != 3 {
		t.Fatalf("orders %+v; want 3 (100, 100, 50)", r.Orders)
	}
	got := messagesOf(r, ActionLotSplit)
	if len(got) != 1 {
		t.Fatalf("lot-split messages %+v; want one", got)
	}
	m := got[0]
	if m.ItemID != "P" || m.SupplyRef != r.Orders[0].Number || m.DelayDays != 3 || !m.Qty.Equal(d("100")) || m.SuggestedDate != 10 {
		t.Fatalf("message %+v; want item P, first order %s, 3 orders, max 100, demand day 10", m, r.Orders[0].Number)
	}
	if CatalogCode(ActionLotSplit) != "PLAN-0005" {
		t.Fatalf("code %q", CatalogCode(ActionLotSplit))
	}
	text := CatalogText(MessageView{Action: ActionLotSplit, ItemID: "P", Days: 3, Qty: "100.000000"})
	if text != "Requirement for P splits into 3 orders (max 100)." {
		t.Fatalf("text %q", text)
	}
	if MessageActionable(ActionLotSplit) {
		t.Fatal("a lot-split warning changes no source order")
	}
}

func TestRun_NoLotSplitWarningWithinTheMaximum(t *testing.T) {
	for _, max := range []string{"0", "250", "300"} {
		r, err := Run(0, []Item{splitItem(max)})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Orders) != 1 || len(messagesOf(r, ActionLotSplit)) != 0 {
			t.Fatalf("max %s: orders %d messages %+v; want one order and no warning", max, len(r.Orders), r.Messages)
		}
	}
}
