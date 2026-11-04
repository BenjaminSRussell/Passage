package domain

import "testing"

// The supply_ref of a message about an open purchase order is the ref the caller
// sent on the receipt, unchanged. Purchasing matches it to the PO number, so the
// run must never rewrite or prefix it (wave 17 handoff).
func TestRun_PurchaseReceiptRefIsTheSupplyRefOfItsMessages(t *testing.T) {
	const po = "PO-4500012345"
	item := Item{ID: "P", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 2, PositiveDays: 3,
		Demand:   []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 20, Qty: d("10"), Priority: PrioritySales}},
		Receipts: []Requirement{{Ref: po, RefType: "purchase-line", Date: 5, Qty: d("10")}}}
	r, err := Run(0, []Item{item})
	if err != nil {
		t.Fatal(err)
	}
	post := messagesOf(r, ActionPostpone)
	if len(post) != 1 || post[0].SupplyRef != po {
		t.Fatalf("postpone messages %+v; want one with supply ref %s", post, po)
	}
	pegged := false
	for _, p := range r.Pegging {
		if p.SupplyRef == po {
			pegged = true
		}
	}
	if !pegged {
		t.Fatalf("pegging %+v; want a line with supply ref %s", r.Pegging, po)
	}
}
