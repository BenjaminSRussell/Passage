package store_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// TestItemCoverages covers item coverage setup on the database (data model
// 4.22.4, FR-22.18..FR-22.20): create with exact decimals and nullable
// references, idempotent replay, the scope key per site / warehouse /
// subcontractor, the coverage group reference, If-Match update, the table
// checks behind the domain rules, and tenant isolation under RLS.
func TestItemCoverages(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.setup.maintain", "planning.read")

	group, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: domain.DefaultCoverageGroup("REQ", domain.CoverageRequirement)})
	if err != nil {
		t.Fatal(err)
	}
	minmax, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: domain.DefaultCoverageGroup("MIN", domain.CoverageMinMax)})
	if err != nil {
		t.Fatal(err)
	}
	item, site, lead := ids.New(), ids.New(), 5
	spec := domain.DefaultItemCoverage(item, site, group.ID)
	spec.MinQty, spec.MaxQty, spec.LeadTimeDays = decimal.RequireFromString("100.5"), decimal.RequireFromString("400.000001"), &lead
	spec.VendorID = ids.New() // a default vendor does not make a site scope per vendor

	created, replayed, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{IdempotencyKey: "ic-1", InputHash: "h1", Spec: spec})
	if err != nil || replayed || created.Version != 1 || created.CoverageGroupCode != "REQ" {
		t.Fatalf("create = %+v (replayed %v), %v", created, replayed, err)
	}
	got := created.Spec
	if !got.MinQty.Equal(spec.MinQty) || !got.MaxQty.Equal(spec.MaxQty) || got.LeadTimeDays == nil || *got.LeadTimeDays != 5 ||
		got.WarehouseID != "" || got.BufferProfileID != "" || got.VendorID != spec.VendorID {
		t.Fatalf("round trip: %+v", got)
	}
	again, replayed, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{IdempotencyKey: "ic-1", InputHash: "h1", Spec: spec})
	if err != nil || !replayed || again.ID != created.ID {
		t.Fatalf("replay = %+v (replayed %v), %v; want the first coverage", again, replayed, err)
	}
	if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{IdempotencyKey: "ic-1", InputHash: "h2", Spec: spec}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}

	otherVendor := spec
	otherVendor.VendorID = ids.New()
	if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: otherVendor}); problemCode(err) != store.CodeItemCoverageExists {
		t.Fatalf("second site-scope coverage: %v; want %s", err, store.CodeItemCoverageExists)
	}
	unknown := domain.DefaultItemCoverage(ids.New(), site, ids.New())
	if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: unknown}); problemCode(err) != store.CodeCoverageGroupUnknown {
		t.Fatalf("unknown group: %v; want %s", err, store.CodeCoverageGroupUnknown)
	}

	// The same item in two warehouses (MRP areas) and at two subcontractors.
	wh1, wh2 := ids.New(), ids.New()
	for _, wh := range []string{wh1, wh2} {
		c := domain.DefaultItemCoverage(item, site, minmax.ID)
		c.PlanningScope, c.WarehouseID = domain.PlanScopeWarehouse, wh
		if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: c}); err != nil {
			t.Fatalf("warehouse %s: %v", wh, err)
		}
	}
	dupWH := domain.DefaultItemCoverage(item, site, minmax.ID)
	dupWH.PlanningScope, dupWH.WarehouseID = domain.PlanScopeWarehouse, wh1
	if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: dupWH}); problemCode(err) != store.CodeItemCoverageExists {
		t.Fatalf("second coverage of warehouse 1: %v", err)
	}
	for range 2 {
		c := domain.DefaultItemCoverage(item, site, group.ID)
		c.PlanningScope, c.VendorID, c.PlannedOrderType = domain.PlanScopeSubcontractor, ids.New(), domain.OrderTransfer
		if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: c}); err != nil {
			t.Fatalf("subcontractor: %v", err)
		}
	}

	list, err := st.ListItemCoverages(ctx, item, 10)
	if err != nil || len(list) != 5 {
		t.Fatalf("list by item = %d rows, %v; want 5", len(list), err)
	}
	if list[0].Spec.PlanningScope != domain.PlanScopeSite || list[4].Spec.PlanningScope != domain.PlanScopeWarehouse {
		t.Fatalf("order by scope: %s .. %s", list[0].Spec.PlanningScope, list[4].Spec.PlanningScope)
	}
	if page, err := st.ListItemCoverages(ctx, "", 2); err != nil || len(page) != 2 {
		t.Fatalf("limit: %d, %v", len(page), err)
	}

	changed := spec
	changed.CoverageGroupID, changed.LeadTimeDays, changed.MaxQty = minmax.ID, nil, decimal.Zero
	changed.PlanningStrategy, changed.RequiredShelfLifeDays, changed.DecouplingPoint = domain.StrategyMTO, 30, true
	updated, err := st.UpdateItemCoverage(ctx, created.ID, 1, changed)
	if err != nil || updated.Version != 2 || updated.CoverageGroupCode != "MIN" || updated.Spec.LeadTimeDays != nil {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	read, err := st.GetItemCoverage(ctx, created.ID)
	if err != nil || read.Version != 2 || read.Spec.PlanningStrategy != domain.StrategyMTO || !read.Spec.DecouplingPoint ||
		read.Spec.RequiredShelfLifeDays != 30 || !read.Spec.MaxQty.IsZero() {
		t.Fatalf("read back = %+v, %v", read, err)
	}
	if _, err := st.UpdateItemCoverage(ctx, created.ID, 1, changed); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale update: %v", err)
	}
	moved := changed
	moved.PlanningScope, moved.WarehouseID = domain.PlanScopeWarehouse, wh1
	if _, err := st.UpdateItemCoverage(ctx, created.ID, 2, moved); problemCode(err) != store.CodeItemCoverageExists {
		t.Fatalf("update onto a taken scope: %v", err)
	}
	gone := changed
	gone.CoverageGroupID = ids.New()
	if _, err := st.UpdateItemCoverage(ctx, created.ID, 2, gone); problemCode(err) != store.CodeCoverageGroupUnknown {
		t.Fatalf("update to unknown group: %v", err)
	}
	if _, err := st.UpdateItemCoverage(ctx, ids.New(), 1, changed); problemCode(err) != store.CodeItemCoverageNotFound {
		t.Fatalf("update of unknown coverage: %v", err)
	}
	if _, err := st.GetItemCoverage(ctx, ids.New()); problemCode(err) != store.CodeItemCoverageNotFound {
		t.Fatalf("get of unknown coverage: %v", err)
	}

	// The table refuses what the domain refuses, even past the API.
	bad := domain.DefaultItemCoverage(ids.New(), site, group.ID)
	bad.PlanningScope, bad.VendorID = domain.PlanScopeSubcontractor, ids.New() // purchase, not a provision transfer
	if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: bad}); err == nil {
		t.Fatal("subcontractor scope with purchase orders was stored")
	}
	bad = domain.DefaultItemCoverage(ids.New(), site, group.ID)
	bad.MinQty, bad.MaxQty = decimal.NewFromInt(10), decimal.NewFromInt(5)
	if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: bad}); err == nil {
		t.Fatal("max below min was stored")
	}

	other := testkit.Ctx(testkit.TenantID(2), "planning.setup.maintain", "planning.read")
	if list, err := st.ListItemCoverages(other, "", 10); err != nil || len(list) != 0 {
		t.Fatalf("other tenant sees %d rows, %v", len(list), err)
	}
	if _, err := st.GetItemCoverage(other, created.ID); problemCode(err) != store.CodeItemCoverageNotFound {
		t.Fatalf("other tenant reads the coverage: %v", err)
	}
	if _, err := st.UpdateItemCoverage(other, created.ID, 2, changed); problemCode(err) != store.CodeItemCoverageNotFound {
		t.Fatalf("other tenant updates the coverage: %v", err)
	}
	// Tenant 1's coverage group id is unknown to tenant 2.
	if _, _, err := st.CreateItemCoverage(other, store.ItemCoverageCreate{Spec: spec}); problemCode(err) != store.CodeCoverageGroupUnknown {
		t.Fatalf("other tenant references tenant 1's group: %v", err)
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from item_coverage`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("rows = %d, %v; want 5", n, err)
	}
}
