package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	events "github.com/BenjaminSRussell/passage/services/planning/internal/events"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// warehousePlan plans a site item with 100 on hand and a warehouse scope that needs a
// transfer of 5 (AC-22.16), then splits the result as the run endpoint does.
func warehousePlan(t *testing.T) (domain.Result, string, string) {
	t.Helper()
	item, wh := ids.New(), ids.New()
	base := []domain.Item{{ID: item, Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 3,
		OnHand: decimal.NewFromInt(100),
		Demand: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 4, Qty: decimal.NewFromInt(5), Priority: domain.PrioritySales}}}}
	specs := map[string][]domain.WarehouseSpec{item: {{WarehouseID: wh, Item: domain.Item{Coverage: domain.CoverageRequirement,
		OrderType: domain.OrderTransfer, LeadTimeDays: 2, OnHand: decimal.NewFromInt(3),
		Demand: []domain.Requirement{{Ref: "SO-2", RefType: domain.RefSales, Date: 4, Qty: decimal.NewFromInt(8), Priority: domain.PrioritySales}}}}}}
	items, err := domain.ExpandWarehouses(base, specs)
	if err != nil {
		t.Fatal(err)
	}
	res, err := domain.Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	return domain.SplitWarehouses(res), item, wh
}

// TestWarehouseScope_PersistsAndFirms: the site row and the warehouse row of one item
// on one day are both stored, every read path tells the scopes apart, firming a
// planned transfer of a warehouse scope writes its warehouse into the event, and
// carry-forward reports it as supply of that warehouse.
func TestWarehouseScope_PersistsAndFirms(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	res, item, wh := warehousePlan(t)
	req := runRequest(res, "wh-1", "in-wh")
	if _, _, err := st.RunPlan(ctx, req); err != nil {
		t.Fatal(err)
	}

	all, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(all) != 1 || all[0].WarehouseID == nil || *all[0].WarehouseID != wh || all[0].OrderType != domain.OrderTransfer {
		t.Fatalf("orders = %+v, %v; want one planned transfer for the warehouse", all, err)
	}
	if got, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{WarehouseID: ids.New()}, 10); err != nil || len(got) != 0 {
		t.Fatalf("another warehouse: %+v, %v", got, err)
	}
	if got, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{WarehouseID: wh}, 10); err != nil || len(got) != 1 {
		t.Fatalf("filter by warehouse: %+v, %v", got, err)
	}

	site, err := st.ListNetRequirements(ctx, "MAIN", item, "", 100)
	if err != nil || len(site) == 0 {
		t.Fatalf("site rows: %+v, %v", site, err)
	}
	whRows, err := st.ListNetRequirements(ctx, "MAIN", item, wh, 100)
	if err != nil || len(whRows) == 0 {
		t.Fatalf("warehouse rows: %+v, %v", whRows, err)
	}
	for _, n := range site {
		if n.WarehouseID != nil {
			t.Fatalf("site read returned a warehouse row: %+v", n)
		}
	}
	for _, n := range whRows {
		if n.WarehouseID == nil || *n.WarehouseID != wh {
			t.Fatalf("warehouse read returned %+v", n)
		}
	}
	// One day holds a row of each scope: the 0004 key (version, item, day) is gone.
	shared := false
	for _, a := range site {
		for _, b := range whRows {
			shared = shared || a.Date == b.Date
		}
	}
	if !shared {
		t.Fatalf("no day with both a site and a warehouse row: %+v / %+v", site, whRows)
	}
	if len(site)+len(whRows) != len(res.Net) {
		t.Fatalf("rows %d + %d, engine %d", len(site), len(whRows), len(res.Net))
	}

	// The supply schedule grid reads the site scope only, day for day as the net requirements read.
	src, err := st.ReadProjectionSource(ctx, "MAIN", []string{item}, 90)
	if err != nil || len(src.Rows[item]) != len(site) {
		t.Fatalf("projection rows = %+v, %v; want the %d site rows", src.Rows[item], err, len(site))
	}
	for i, n := range src.Rows[item] {
		if n.Date != site[i].Date || n.PAB != site[i].PAB {
			t.Fatalf("projection row %d = %+v, site row %+v", i, n, site[i])
		}
	}

	// Firming the transfer: the event names the to-warehouse.
	order := all[0]
	batch, _, err := st.FirmPlannedOrders(ctx, store.FirmRequest{IdempotencyKey: "firm-wh", InputHash: "h", Grouping: domain.GroupVendorDate, OrderIDs: []string{order.ID}})
	if err != nil || batch.Firmed != 1 || batch.Groups[0].TargetType != "transfer-order" {
		t.Fatalf("batch = %+v, %v", batch, err)
	}
	var raw []byte
	if err := admin.QueryRow(context.Background(), `select payload from outbox where type = $1`, events.PlannedOrderFirmed).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data events.PlannedOrderFirmedData `json:"data"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil || evt.Data.WarehouseID == nil || *evt.Data.WarehouseID != wh || evt.Data.ItemID != item {
		t.Fatalf("firmed event = %+v, %v", evt.Data, err)
	}

	supply, err := st.FirmedSupply(ctx, "MAIN", req.SiteID, req.AsOf)
	if err != nil || len(supply) != 1 || supply[0].WarehouseID != wh || supply[0].ItemID != item || supply[0].OrderDate.Format("2006-01-02") != order.OrderDate {
		t.Fatalf("firmed supply = %+v, %v", supply, err)
	}
}

// TestRunWarehouseCoverages: the warehouse-scope coverage is keyed by item and
// warehouse; the site-scope read never returns it.
func TestRunWarehouseCoverages(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.setup.maintain", "planning.read")
	group, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: domain.DefaultCoverageGroup("REQ", domain.CoverageRequirement)})
	if err != nil {
		t.Fatal(err)
	}
	site, item, wh := ids.New(), ids.New(), ids.New()
	lead := 2
	c := domain.DefaultItemCoverage(item, site, group.ID)
	c.PlanningScope, c.WarehouseID, c.PlannedOrderType, c.LeadTimeDays = domain.PlanScopeWarehouse, wh, domain.OrderTransfer, &lead
	if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: c}); err != nil {
		t.Fatal(err)
	}
	got, err := st.RunWarehouseCoverages(ctx, site, "", []string{item})
	if err != nil || len(got) != 1 {
		t.Fatalf("warehouse coverage = %+v, %v", got, err)
	}
	r := got[domain.WarehouseKey(item, wh)]
	if r.PlannedOrderType != domain.OrderTransfer || r.LeadTimeDays == nil || *r.LeadTimeDays != 2 || r.CoverageCode != domain.CoverageRequirement {
		t.Fatalf("coverage = %+v", r)
	}
	if site, err := st.RunCoverages(ctx, site, "", []string{item}); err != nil || len(site) != 0 {
		t.Fatalf("site read returned the warehouse scope: %+v, %v", site, err)
	}
	if other, err := st.RunWarehouseCoverages(testkit.Ctx(testkit.TenantID(2), "planning.read"), site, "", []string{item}); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 = %+v, %v", other, err)
	}
}
