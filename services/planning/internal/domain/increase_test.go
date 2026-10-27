package domain

import (
	"testing"

	"pgregory.net/rapid"
)

// FR-22.7 Increase: inside the freeze fence no planned order may be created
// (BR-22.4), so a shortage there asks the latest existing receipt dated on or
// before the shortage to increase by the shortage. The shortage message stays.
func TestRun_IncreaseOnFrozenShortage(t *testing.T) {
	frozenItem := func(receipts []Requirement, actionDays int) Item {
		return Item{ID: "F", Coverage: CoverageRequirement, OrderType: OrderPurchase, FreezeDays: 10, ActionDays: actionDays,
			Demand:   []Requirement{{Ref: "SO-7", RefType: RefSales, Date: 3, Qty: d("60"), Priority: PrioritySales}},
			Receipts: receipts}
	}
	tests := []struct {
		name       string
		item       Item
		wantSupply string // "" when no Increase is expected
		wantQty    string
		wantDate   int
	}{
		{"receipt before the shortage is increased by the shortfall",
			frozenItem([]Requirement{{Ref: "PO-7", RefType: "purchase-line", Date: 1, Qty: d("50")}}, 0), "PO-7", "10", 1},
		{"receipt on the shortage day is increased",
			frozenItem([]Requirement{{Ref: "PO-7", RefType: "purchase-line", Date: 3, Qty: d("50")}}, 0), "PO-7", "10", 3},
		{"the latest receipt before the shortage is the one asked",
			frozenItem([]Requirement{{Ref: "PO-1", RefType: "purchase-line", Date: 0, Qty: d("20")}, {Ref: "PO-2", RefType: "purchase-line", Date: 2, Qty: d("30")}}, 0), "PO-2", "10", 2},
		{"a receipt after the shortage is not increased (it would still be late)",
			frozenItem([]Requirement{{Ref: "PO-7", RefType: "purchase-line", Date: 4, Qty: d("50")}}, 0), "", "", 0},
		{"a receipt outside the action fence stays silent",
			frozenItem([]Requirement{{Ref: "PO-7", RefType: "purchase-line", Date: 2, Qty: d("50")}}, 2), "", "", 0},
		{"no existing receipt raises only the shortage",
			frozenItem(nil, 0), "", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Run(0, []Item{tc.item})
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Orders) != 0 {
				t.Fatalf("freeze fence must create no orders: %+v", r.Orders)
			}
			short := messagesOf(r, ActionShortage)
			if len(short) != 1 || short[0].DemandRef != "SO-7" {
				t.Fatalf("shortage messages = %+v; want one for SO-7", short)
			}
			inc := messagesOf(r, ActionIncrease)
			if tc.wantSupply == "" {
				if len(inc) != 0 {
					t.Fatalf("increase messages = %+v; want none", inc)
				}
				return
			}
			if len(inc) != 1 {
				t.Fatalf("increase messages = %+v; want one", inc)
			}
			m := inc[0]
			if m.SupplyRef != tc.wantSupply || !m.Qty.Equal(d(tc.wantQty)) || m.SuggestedDate != tc.wantDate || m.DemandRef != "SO-7" || m.ItemID != "F" {
				t.Fatalf("increase = %+v; want %s by %s on day %d for SO-7", m, tc.wantSupply, tc.wantQty, tc.wantDate)
			}
			if !short[0].Qty.Equal(m.Qty) {
				t.Fatalf("increase %s must equal the shortage %s", m.Qty, short[0].Qty)
			}
		})
	}
}

// Two frozen shortages on different days ask the same receipt once, for their sum.
func TestRun_IncreaseAddsUpShortagesOfOneReceipt(t *testing.T) {
	it := Item{ID: "F", Coverage: CoverageRequirement, OrderType: OrderPurchase, FreezeDays: 10,
		Demand: []Requirement{
			{Ref: "SO-1", RefType: RefSales, Date: 3, Qty: d("15"), Priority: PrioritySales},
			{Ref: "SO-2", RefType: RefSales, Date: 5, Qty: d("7"), Priority: PrioritySales},
		},
		Receipts: []Requirement{{Ref: "PO-1", RefType: "purchase-line", Date: 1, Qty: d("10")}}}
	r, err := Run(0, []Item{it})
	if err != nil {
		t.Fatal(err)
	}
	inc := messagesOf(r, ActionIncrease)
	if len(inc) != 1 || inc[0].SupplyRef != "PO-1" || !inc[0].Qty.Equal(d("12")) || inc[0].DemandRef != "SO-1" {
		t.Fatalf("increase = %+v; want one on PO-1 by 12 (5 + 7) for SO-1", inc)
	}
}

// Outside the freeze fence the shortfall is planned, not raised on a receipt;
// a safety-stock shortfall inside it is not a reason to increase supply.
func TestRun_NoIncreaseOutsideFreezeOrForSafetyStock(t *testing.T) {
	open := Item{ID: "F", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 1,
		Demand:   []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 3, Qty: d("60"), Priority: PrioritySales}},
		Receipts: []Requirement{{Ref: "PO-1", RefType: "purchase-line", Date: 1, Qty: d("50")}}}
	safety := Item{ID: "S", Coverage: CoverageRequirement, OrderType: OrderPurchase, FreezeDays: 10, SafetyStock: d("5"),
		Receipts: []Requirement{{Ref: "PO-2", RefType: "purchase-line", Date: 0, Qty: d("1")}}}
	r, err := Run(0, []Item{open, safety})
	if err != nil {
		t.Fatal(err)
	}
	if inc := messagesOf(r, ActionIncrease); len(inc) != 0 {
		t.Fatalf("increase messages = %+v; want none", inc)
	}
	if len(r.Orders) != 1 || r.Orders[0].ItemID != "F" || !r.Orders[0].Qty.Equal(d("10")) {
		t.Fatalf("orders = %+v; want one planned order of 10 for F", r.Orders)
	}
}

// Property: every Increase names an existing receipt of its item dated on or
// before the demand it is for, and the increases of an item never exceed the
// item's reported shortages.
func TestRun_PropertyIncreaseIsBoundedByShortage(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		it := Item{ID: "P", Coverage: CoverageRequirement, OrderType: OrderPurchase,
			FreezeDays: rapid.IntRange(0, 15).Draw(t, "freeze"), ActionDays: rapid.IntRange(0, 15).Draw(t, "action"),
			OnHand: d(rapid.SampledFrom([]string{"0", "5", "20"}).Draw(t, "onHand"))}
		for i := range rapid.IntRange(0, 5).Draw(t, "demands") {
			it.Demand = append(it.Demand, Requirement{Ref: "SO-" + string(rune('A'+i)), RefType: RefSales,
				Date: rapid.IntRange(0, 20).Draw(t, "dd"), Qty: d(rapid.SampledFrom([]string{"3", "10", "25"}).Draw(t, "dq")), Priority: PrioritySales})
		}
		receiptDay := map[string]int{}
		for i := range rapid.IntRange(0, 3).Draw(t, "receipts") {
			ref := "PO-" + string(rune('A'+i))
			day := rapid.IntRange(0, 20).Draw(t, "rd")
			receiptDay[ref] = day
			it.Receipts = append(it.Receipts, Requirement{Ref: ref, RefType: "purchase-line", Date: day, Qty: d(rapid.SampledFrom([]string{"4", "12"}).Draw(t, "rq"))})
		}
		r, err := Run(0, []Item{it})
		if err != nil {
			t.Fatal(err)
		}
		short, inc := d("0"), d("0")
		for _, m := range messagesOf(r, ActionShortage) {
			short = short.Add(m.Qty)
		}
		for _, m := range messagesOf(r, ActionIncrease) {
			day, ok := receiptDay[m.SupplyRef]
			if !ok || m.SuggestedDate != day || !m.Qty.IsPositive() {
				t.Fatalf("increase %+v does not name an existing receipt on its own date", m)
			}
			inc = inc.Add(m.Qty)
		}
		if inc.GreaterThan(short) {
			t.Fatalf("increases %s exceed shortages %s: %+v", inc, short, r.Messages)
		}
	})
}
