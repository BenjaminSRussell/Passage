package domain

import (
	"errors"
	"testing"
)

const (
	whSite = "00000000-0000-0000-0000-0000000000a1"
	whWH2  = "00000000-0000-0000-0000-0000000000b2"
)

func whItems(siteOnHand, whOnHand string) ([]Item, map[string][]WarehouseSpec) {
	site := Item{ID: whSite, Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 3, OnHand: d(siteOnHand),
		Demand: []Requirement{{Ref: "SO-1", RefType: RefSales, Date: 4, Qty: d("5"), Priority: PrioritySales}}}
	wh := Item{Coverage: CoverageRequirement, OrderType: OrderTransfer, LeadTimeDays: 2, OnHand: d(whOnHand),
		Demand: []Requirement{{Ref: "SO-2", RefType: RefSales, Date: 10, Qty: d("8"), Priority: PrioritySales}}}
	return []Item{site}, map[string][]WarehouseSpec{whSite: {{WarehouseID: whWH2, Item: wh}}}
}

// AC-22.16: WH-2 demand is covered by a planned transfer from the site, the
// transfer creates the site demand at delivery less the transfer lead time
// (BR-22.7), and WH-2 on-hand is not netted against site demand.
func TestWarehouseScopeTransfer_AC2216(t *testing.T) {
	base, specs := whItems("100", "3")
	items, err := ExpandWarehouses(base, specs)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	res = SplitWarehouses(res)
	var transfers []PlannedOrder
	for _, o := range res.Orders {
		if o.WarehouseID == whWH2 {
			transfers = append(transfers, o)
		}
	}
	if len(transfers) != 1 {
		t.Fatalf("want one planned transfer at WH-2, got %+v", res.Orders)
	}
	o := transfers[0]
	if o.ItemID != whSite || o.OrderType != OrderTransfer || !o.Qty.Equal(d("5")) || o.DeliveryDate != 10 || o.OrderDate != 8 {
		t.Fatalf("transfer = %+v: want 5 delivered day 10, ordered day 8 (8 demand less 3 on hand)", o)
	}
	// The site is not short: its on-hand covers its own demand and the transfer demand.
	for _, ord := range res.Orders {
		if ord.WarehouseID == "" {
			t.Fatalf("site must plan nothing with 100 on hand, got %+v", ord)
		}
	}
	// The transfer's issue at the site is pegged at its order date.
	found := false
	for _, p := range res.Pegging {
		if p.WarehouseID == "" && p.DemandRef == o.Number+"/"+whSite {
			found = p.DemandDate == 8 && p.DemandRefType == RefBOM && p.Qty.Equal(d("5"))
		}
	}
	if !found {
		t.Fatalf("site on-hand is not pegged to the transfer demand at day 8: %+v", res.Pegging)
	}
}

func TestWarehouseOnHandNotNettedAgainstSite(t *testing.T) {
	base, specs := whItems("0", "50")
	items, _ := ExpandWarehouses(base, specs)
	res, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	res = SplitWarehouses(res)
	if len(res.Orders) != 1 || res.Orders[0].WarehouseID != "" || !res.Orders[0].Qty.Equal(d("5")) {
		t.Fatalf("WH-2 stock covers WH-2 only: want one site order of 5, got %+v", res.Orders)
	}
	for _, p := range res.Pegging {
		if p.WarehouseID == "" && p.SupplyRef == RefOnHand+":"+whSite+WarehouseSep+whWH2 {
			t.Fatalf("WH-2 on-hand pegged to site demand: %+v", p)
		}
	}
}

func TestWarehouseSiteDemandAddsToTransfer(t *testing.T) {
	base, specs := whItems("0", "3")
	items, _ := ExpandWarehouses(base, specs)
	res, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	res = SplitWarehouses(res)
	total := d("0")
	for _, o := range res.Orders {
		if o.WarehouseID == "" {
			total = total.Add(o.Qty)
		}
	}
	// Site demand 5 (own sales) + 5 (transfer to WH-2) = 10 to purchase.
	if !total.Equal(d("10")) {
		t.Fatalf("site purchases = %s, want 10", total)
	}
}

func TestWarehouseDirectPurchaseCreatesNoSiteDemand(t *testing.T) {
	base, specs := whItems("100", "0")
	specs[whSite][0].Item.OrderType = OrderPurchase
	items, _ := ExpandWarehouses(base, specs)
	res, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	res = SplitWarehouses(res)
	if len(res.Orders) != 1 || res.Orders[0].WarehouseID != whWH2 || res.Orders[0].OrderType != OrderPurchase {
		t.Fatalf("want one direct purchase at WH-2, got %+v", res.Orders)
	}
}

func TestWarehouseNetRowsSeparateScopes(t *testing.T) {
	base, specs := whItems("100", "3")
	items, _ := ExpandWarehouses(base, specs)
	res, _ := Run(0, items)
	res = SplitWarehouses(res)
	seen := map[string]bool{}
	for _, n := range res.Net {
		if n.ItemID != whSite {
			t.Fatalf("net row item = %q", n.ItemID)
		}
		seen[n.WarehouseID] = true
	}
	if !seen[""] || !seen[whWH2] {
		t.Fatalf("want net rows for the site and WH-2, got %v", seen)
	}
}

func TestExpandWarehousesRejects(t *testing.T) {
	base, specs := whItems("0", "0")
	dup := map[string][]WarehouseSpec{whSite: {specs[whSite][0], specs[whSite][0]}}
	if _, err := ExpandWarehouses(base, dup); !errors.Is(err, ErrInvalidItem) {
		t.Fatalf("duplicate scope: %v", err)
	}
	if _, err := ExpandWarehouses(base, map[string][]WarehouseSpec{"other": specs[whSite]}); !errors.Is(err, ErrInvalidItem) {
		t.Fatalf("unknown item: %v", err)
	}
	if _, err := ExpandWarehouses(base, map[string][]WarehouseSpec{whSite: {{Item: specs[whSite][0].Item}}}); !errors.Is(err, ErrInvalidItem) {
		t.Fatalf("empty warehouse id: %v", err)
	}
}

func TestApplyScopeJudgesWarehouseLikeItsItem(t *testing.T) {
	base, specs := whItems("100", "3")
	items, _ := ExpandWarehouses(base, specs)
	res, _ := Run(0, items)
	out, n, err := ApplyScope(res, items, nil, Scope{ItemIDs: []string{whSite}, LLCMax: ptrInt(0)})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 || len(out.Orders) != len(res.Orders) {
		t.Fatalf("item scope must keep both scopes at llc 0: n=%d orders=%d/%d", n, len(out.Orders), len(res.Orders))
	}
}

func ptrInt(n int) *int { return &n }

// A production scope explodes the item's BOM: the warehouse's production order
// demands the component in its site scope, and creates no demand for the item at the site.
func TestWarehouseProductionExplodesTheBOM(t *testing.T) {
	const rm = "00000000-0000-0000-0000-0000000000c3"
	base, specs := whItems("100", "0")
	base[0].OrderType = OrderProduction
	base[0].Components = []BOMLine{{ItemID: rm, Qty: d("2")}}
	base = append(base, Item{ID: rm, Coverage: CoverageRequirement, OrderType: OrderPurchase, LeadTimeDays: 1})
	specs[whSite][0].Item.OrderType = OrderProduction
	items, err := ExpandWarehouses(base, specs)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	res = SplitWarehouses(res)
	var production, purchase *PlannedOrder
	for i, o := range res.Orders {
		switch {
		case o.ItemID == whSite && o.WarehouseID == whWH2:
			production = &res.Orders[i]
		case o.ItemID == rm && o.WarehouseID == "":
			purchase = &res.Orders[i]
		case o.ItemID == whSite:
			t.Fatalf("the site plans nothing for the item with 100 on hand: %+v", o)
		}
	}
	if production == nil || production.OrderType != OrderProduction || !production.Qty.Equal(d("8")) {
		t.Fatalf("want a production order of 8 at WH-2, got %+v", res.Orders)
	}
	if purchase == nil || !purchase.Qty.Equal(d("16")) {
		t.Fatalf("want the component purchased for 2 x 8 at the site, got %+v", res.Orders)
	}
}
