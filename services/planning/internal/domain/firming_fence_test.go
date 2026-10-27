package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

// firmingItem is a purchased item with a lead time of 2 days and one sales
// demand of 10 on demandDay, so its single planned order is dated demandDay - 2.
func firmingItem(demandDay, fence int, autoFirm bool) Item {
	return Item{
		ID: "A", Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 2,
		FirmingDays: fence, AutoFirm: autoFirm,
		Demand: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: demandDay, Qty: decimal.NewFromInt(10), Priority: PrioritySales}},
	}
}

// TestRun_AutoFirmInsideFirmingFence covers BR-22.4: a planned order whose
// order date falls inside the firming time fence is auto-firmed, only when the
// coverage enables auto-firming, and the fence counts from today exclusive of
// its last day.
func TestRun_AutoFirmInsideFirmingFence(t *testing.T) {
	cases := []struct {
		name      string
		demandDay int
		fence     int
		autoFirm  bool
		want      bool
	}{
		{name: "order date inside the fence", demandDay: 4, fence: 3, autoFirm: true, want: true},
		{name: "order date on the fence boundary is outside", demandDay: 5, fence: 3, autoFirm: true, want: false},
		{name: "order date beyond the fence", demandDay: 9, fence: 3, autoFirm: true, want: false},
		{name: "past-due order is inside the fence", demandDay: 1, fence: 3, autoFirm: true, want: true},
		{name: "auto-firm off", demandDay: 4, fence: 3, autoFirm: false, want: false},
		{name: "no firming fence", demandDay: 4, fence: 0, autoFirm: true, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Run(0, []Item{firmingItem(tc.demandDay, tc.fence, tc.autoFirm)})
			if err != nil {
				t.Fatal(err)
			}
			if len(res.Orders) != 1 {
				t.Fatalf("orders = %d, want 1", len(res.Orders))
			}
			if got := res.Orders[0].AutoFirmed; got != tc.want {
				t.Fatalf("AutoFirmed = %v, want %v (order date %d)", got, tc.want, res.Orders[0].OrderDate)
			}
		})
	}
}

// TestRun_AutoFirmChangesNothingButTheFlag: the firming fence decides which
// orders are firmed, never what is planned. The orders, pegging and messages
// equal the run without auto-firming; only the plan hash records the firming.
func TestRun_AutoFirmChangesNothingButTheFlag(t *testing.T) {
	off, err := Run(0, []Item{firmingItem(4, 3, false)})
	if err != nil {
		t.Fatal(err)
	}
	on, err := Run(0, []Item{firmingItem(4, 3, true)})
	if err != nil {
		t.Fatal(err)
	}
	a, b := off.Orders[0], on.Orders[0]
	b.AutoFirmed = false
	if a.Number != b.Number || !a.Qty.Equal(b.Qty) || a.OrderDate != b.OrderDate || a.DeliveryDate != b.DeliveryDate {
		t.Fatalf("auto-firm changed the order: %+v vs %+v", a, b)
	}
	if len(off.Pegging) != len(on.Pegging) || len(off.Messages) != len(on.Messages) {
		t.Fatalf("auto-firm changed pegging or messages")
	}
	if off.Hash == on.Hash {
		t.Fatal("plan hash does not record the auto-firmed order")
	}
	again, _ := Run(0, []Item{firmingItem(4, 3, true)})
	if again.Hash != on.Hash {
		t.Fatal("auto-firmed run is not deterministic")
	}
}

func TestRun_NegativeFirmingFenceRejected(t *testing.T) {
	item := firmingItem(4, -1, false)
	if _, err := Run(0, []Item{item}); err == nil {
		t.Fatal("negative firming fence accepted")
	}
}
