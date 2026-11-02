package store_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// TestRunCoverages reads the stored coverage a plan run uses (FR-22.2,
// FR-22.5): the site-scope row of an item with its coverage group's settings
// and exact decimals; warehouse and subcontractor scopes, other sites, items
// without coverage and other tenants are absent. Skipped without
// PASSAGE_TEST_DATABASE_URL.
func TestRunCoverages(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.setup.maintain", "planning.read")

	spec := domain.DefaultCoverageGroup("PER", domain.CoveragePeriod)
	spec.PeriodDays, spec.FreezeFenceDays, spec.FirmingFenceDays, spec.AutoFirm = 7, 3, 5, true
	spec.SafetyStockPolicy = domain.SafetyFirstIssue
	period, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: spec})
	if err != nil {
		t.Fatal(err)
	}
	minmax, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: domain.DefaultCoverageGroup("MM", domain.CoverageMinMax)})
	if err != nil {
		t.Fatal(err)
	}
	site, otherSite := ids.New(), ids.New()
	item, whOnly, bare, noLead := ids.New(), ids.New(), ids.New(), ids.New()
	lead := 5

	c := domain.DefaultItemCoverage(item, site, period.ID)
	c.MinQty, c.MaxQty, c.SafetyMarginReceiptDays, c.LeadTimeDays = decimal.RequireFromString("100.5"), decimal.RequireFromString("400.000001"), 2, &lead
	c.PlannedOrderType = domain.OrderProduction
	c.SafetyMarginIssueDays, c.SafetyMarginReorderDays = 3, 4
	vendor := ids.New()
	c.VendorID = vendor
	mustCreate := func(spec domain.ItemCoverageSpec) {
		t.Helper()
		if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: spec}); err != nil {
			t.Fatal(err)
		}
	}
	mustCreate(c)
	// The same item in its own warehouse scope and at a subcontractor: never read at site level.
	wh := domain.DefaultItemCoverage(item, site, minmax.ID)
	wh.PlanningScope, wh.WarehouseID = domain.PlanScopeWarehouse, ids.New()
	mustCreate(wh)
	sub := domain.DefaultItemCoverage(item, site, minmax.ID)
	sub.PlanningScope, sub.VendorID, sub.PlannedOrderType = domain.PlanScopeSubcontractor, ids.New(), domain.OrderTransfer
	mustCreate(sub)
	// An item planned only in a warehouse scope.
	whItem := domain.DefaultItemCoverage(whOnly, site, minmax.ID)
	whItem.PlanningScope, whItem.WarehouseID = domain.PlanScopeWarehouse, ids.New()
	mustCreate(whItem)
	// An item without a lead-time override, and the main item at another site.
	mustCreate(domain.DefaultItemCoverage(noLead, site, minmax.ID))
	mustCreate(domain.DefaultItemCoverage(item, otherSite, minmax.ID))

	got, err := st.RunCoverages(ctx, site, "", []string{item, whOnly, bare, noLead})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("stored coverage for %d items, want 2 (item and noLead): %+v", len(got), got)
	}
	r, ok := got[item]
	if !ok {
		t.Fatal("item has no stored coverage")
	}
	if r.GroupCode != "PER" || r.CoverageCode != domain.CoveragePeriod || r.PeriodDays != 7 || r.FreezeFenceDays != 3 ||
		r.FirmingFenceDays != 5 || !r.AutoFirm || r.CoverageFenceDays != 100 || r.NegativeDays != 1 || r.SafetyStockPolicy != domain.SafetyFirstIssue {
		t.Fatalf("group settings: %+v", r)
	}
	if !r.MinQty.Equal(decimal.RequireFromString("100.5")) || !r.MaxQty.Equal(decimal.RequireFromString("400.000001")) ||
		r.MarginDays != 2 || r.IssueMarginDays != 3 || r.ReorderMarginDays != 4 || r.LeadTimeDays == nil || *r.LeadTimeDays != 5 || r.PlannedOrderType != domain.OrderProduction || r.VendorID != vendor {
		t.Fatalf("item coverage values: %+v", r)
	}
	if n := got[noLead]; n.LeadTimeDays != nil || n.CoverageCode != domain.CoverageMinMax || n.PlannedOrderType != domain.OrderPurchase || n.SafetyStockPolicy != domain.SafetyToday {
		t.Fatalf("coverage without lead-time override: %+v", n)
	}

	at, err := st.RunCoverages(ctx, otherSite, "", []string{item})
	if err != nil || len(at) != 1 || at[item].GroupCode != "MM" {
		t.Fatalf("other site = %+v, %v; want its own MM coverage", at, err)
	}
	if none, err := st.RunCoverages(ctx, site, "", nil); err != nil || len(none) != 0 {
		t.Fatalf("no items = %+v, %v", none, err)
	}
	other, err := st.RunCoverages(testkit.Ctx(testkit.TenantID(2), "planning.read"), site, "", []string{item})
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("another tenant reads %d stored coverages", len(other))
	}
}
