package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func vendorResult() Result {
	res := Result{Hash: "h0", Orders: []PlannedOrder{
		{Number: "PLN-1", ItemID: "i1", OrderType: OrderPurchase, Qty: decimal.NewFromInt(10), OrderDate: 5},
		{Number: "PLN-2", ItemID: "i1", OrderType: OrderPurchase, Qty: decimal.NewFromInt(7), OrderDate: 2},
		{Number: "PLN-3", ItemID: "i2", OrderType: OrderPurchase, Qty: decimal.NewFromInt(4), OrderDate: 1},
		{Number: "PLN-4", ItemID: "i3", OrderType: OrderProduction, Qty: decimal.NewFromInt(1)},
		{Number: "PLN-5", ItemID: "i2", OrderType: OrderPurchase, Qty: decimal.NewFromInt(1), VendorID: "quota-vendor"},
	}}
	return res
}

func TestApplyDefaultVendors_GivesAndKeepsVendors(t *testing.T) {
	in := vendorResult()
	out := ApplyDefaultVendors(in, map[string]string{"i1": "v1", "i2": "v2", "i3": "v3"})
	want := []string{"v1", "v1", "v2", "", "quota-vendor"}
	for i, o := range out.Orders {
		if o.VendorID != want[i] {
			t.Errorf("order %s vendor %q; want %q (production takes none, a quota vendor stays)", o.Number, o.VendorID, want[i])
		}
	}
	if in.Orders[0].VendorID != "" {
		t.Error("the input result was modified")
	}
	if len(out.Messages) != 0 {
		t.Errorf("messages %+v; want none when every purchase has a vendor", out.Messages)
	}
	if out.Hash == in.Hash {
		t.Error("the hash must cover the vendors given")
	}
	if again := ApplyDefaultVendors(in, map[string]string{"i1": "v9", "i2": "v2"}); again.Hash == out.Hash {
		t.Error("another vendor must give another hash")
	}
}

func TestApplyDefaultVendors_OneWarningPerItemOnItsEarliestOrder(t *testing.T) {
	out := ApplyDefaultVendors(vendorResult(), map[string]string{"i2": "v2"})
	if len(out.Messages) != 1 {
		t.Fatalf("messages %+v; want one for i1", out.Messages)
	}
	m := out.Messages[0]
	if m.Action != ActionNoVendor || m.ItemID != "i1" || m.SupplyRef != "PLN-2" || m.SuggestedDate != 2 || !m.Qty.Equal(decimal.NewFromInt(17)) {
		t.Fatalf("message %+v; want PLAN-0008 on PLN-2 with 17", m)
	}
	if out.Orders[0].VendorID != "" || out.Orders[1].VendorID != "" {
		t.Error("orders without a vendor stay planned without one")
	}
}

func TestApplyDefaultVendors_NothingToDoKeepsTheResult(t *testing.T) {
	res := Result{Hash: "h", Orders: []PlannedOrder{{Number: "PLN-1", ItemID: "i", OrderType: OrderProduction}}}
	if out := ApplyDefaultVendors(res, nil); out.Hash != "h" || len(out.Messages) != 0 {
		t.Fatalf("result changed: %+v", out)
	}
	if out := ApplyDefaultVendors(Result{Hash: "h"}, map[string]string{"i": "v"}); out.Hash != "h" {
		t.Fatal("empty result changed")
	}
}

func TestCatalogTexts(t *testing.T) {
	cases := []struct {
		m          MessageView
		code, text string
	}{
		{MessageView{Action: ActionPostpone, SupplyRef: "PO-1", Date: "2026-11-09"}, "PLAN-0006", "Supply PO-1 can be postponed to 2026-11-09."},
		{MessageView{Action: ActionDelay, DemandRef: "SO-1", Days: 7, Date: "2026-12-07"}, "PLAN-0007", "Demand SO-1 will be delayed 7 days (expected 2026-12-07)."},
		{MessageView{Action: ActionNoVendor, ItemID: "RM-1"}, "PLAN-0008", "No vendor for planned purchase of RM-1; using none."},
		{MessageView{Action: ActionRedZone, ItemID: "FG-1", Priority: "0.5306"}, "PLAN-0010", "Buffer for FG-1 is in red zone (priority 53.06%)."},
		{MessageView{Action: ActionDelay, SupplyRef: "PLN-4", Days: 2, Date: "2026-12-07"}, "PLAN-0007", "Demand PLN-4 will be delayed 2 days (expected 2026-12-07)."},
		{MessageView{Action: ActionSalesOnTop, DemandRef: "SO-9", Qty: "30.500000"}, "PLAN-0015", "Sales demand SO-9 exceeds consumable forecast by 30.5; added on top."},
		{MessageView{Action: ActionShortage}, "", ""},
	}
	for _, c := range cases {
		if got := CatalogCode(c.m.Action); got != c.code {
			t.Errorf("%s: code %q; want %q", c.m.Action, got, c.code)
		}
		if got := CatalogText(c.m); got != c.text {
			t.Errorf("%s: text %q; want %q", c.m.Action, got, c.text)
		}
	}
}
