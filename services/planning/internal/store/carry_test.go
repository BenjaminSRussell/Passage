package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// TestFirmedSupply covers the carry-forward read (FR-22.1, FR-22.10): orders
// firmed in version 1 are still listed after version 2 replaced it, only for
// their own site and while their delivery date has not passed, and another
// tenant sees none.
func TestFirmedSupply(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	site := ids.New()
	first := runRequest(threePurchasesAndOneProduction(t), "run-1", "in-1")
	first.SiteID = site
	if _, _, err := st.RunPlan(ctx, first); err != nil {
		t.Fatal(err)
	}
	orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(orders) != 4 {
		t.Fatalf("orders: %+v, %v", orders, err)
	}
	var firm []string
	var latest string
	for _, o := range orders {
		if o.OrderType == "purchase" {
			continue // two of the four stay unprocessed and are not supply
		}
		firm = append(firm, o.ID)
		latest = o.DeliveryDate
	}
	if len(firm) != 1 {
		t.Fatalf("firm %v", firm)
	}
	if _, _, err := st.FirmPlannedOrders(ctx, store.FirmRequest{IdempotencyKey: "firm-1", InputHash: "h", Grouping: "none", OrderIDs: firm}); err != nil {
		t.Fatal(err)
	}
	// Version 2 replaces version 1 as the current one.
	second := runRequest(threePurchasesAndOneProduction(t), "run-2", "in-2")
	second.SiteID = site
	if _, _, err := st.RunPlan(ctx, second); err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	got, err := st.FirmedSupply(ctx, "MAIN", site, asOf)
	if err != nil || len(got) != 1 || got[0].OrderID != firm[0] || got[0].OrderType != "production" || !got[0].Qty.IsPositive() {
		t.Fatalf("firmed supply = %+v, %v; want the one order firmed in version 1", got, err)
	}
	if got[0].DeliveryDate.Format("2006-01-02") != latest {
		t.Fatalf("delivery %v, want %s", got[0].DeliveryDate, latest)
	}
	if other, err := st.FirmedSupply(ctx, "MAIN", ids.New(), asOf); err != nil || len(other) != 0 {
		t.Fatalf("other site: %+v, %v", other, err)
	}
	if other, err := st.FirmedSupply(ctx, "OTHER", site, asOf); err != nil || len(other) != 0 {
		t.Fatalf("other plan: %+v, %v", other, err)
	}
	if late, err := st.FirmedSupply(ctx, "MAIN", site, got[0].DeliveryDate.AddDate(0, 0, 1)); err != nil || len(late) != 0 {
		t.Fatalf("delivered order still listed: %+v, %v", late, err)
	}
	if on, err := st.FirmedSupply(ctx, "MAIN", site, got[0].DeliveryDate); err != nil || len(on) != 1 {
		t.Fatalf("order delivering on the plan date: %+v, %v", on, err)
	}
	otherTenant := testkit.Ctx(testkit.TenantID(2), "planning.run", "planning.read")
	if leaked, err := st.FirmedSupply(otherTenant, "MAIN", site, asOf); err != nil || len(leaked) != 0 {
		t.Fatalf("tenant leak: %+v, %v", leaked, err)
	}
}

// TestFirmedSupply_LegalEntityAndSimulations: a caller scoped to another legal
// entity reads no firmed supply of the plan (RLS on plan_version), and orders
// of a simulation version are never carried, even when marked firmed.
func TestFirmedSupply_LegalEntityAndSimulations(t *testing.T) {
	st, admin, _ := setup(t)
	entityA, entityB := ids.New(), ids.New()
	ctxA := scopedCtx(testkit.TenantID(1), entityA)
	ctxB := scopedCtx(testkit.TenantID(1), entityB)
	site := ids.New()
	req := runRequest(threePurchasesAndOneProduction(t), "run-a", "in-a")
	req.SiteID = site
	if _, _, err := st.RunPlan(ctxA, req); err != nil {
		t.Fatal(err)
	}
	orders, err := st.ListPlannedOrders(ctxA, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(orders) != 4 {
		t.Fatalf("orders: %d, %v", len(orders), err)
	}
	var all []string
	for _, o := range orders {
		all = append(all, o.ID)
	}
	if _, _, err := st.FirmPlannedOrders(ctxA, store.FirmRequest{IdempotencyKey: "firm-a", InputHash: "h", Grouping: "none", OrderIDs: all}); err != nil {
		t.Fatal(err)
	}
	asOf := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	if got, err := st.FirmedSupply(ctxA, "MAIN", site, asOf); err != nil || len(got) != 4 {
		t.Fatalf("entity A: %d orders, %v; want 4", len(got), err)
	}
	if got, err := st.FirmedSupply(ctxB, "MAIN", site, asOf); err != nil || len(got) != 0 {
		t.Fatalf("entity B sees entity A's firmed supply: %+v, %v", got, err)
	}
	// Turn the version into a simulation of a stored scenario (the check
	// constraint wants both); the firmed orders then never count as supply.
	sc, _, err := st.CreateScenario(ctxA, store.ScenarioCreate{Spec: scenarioSpec("UP20")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `update plan_version set kind = 'simulation', status = 'complete', scenario_id = $1`, sc.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := st.FirmedSupply(ctxA, "MAIN", site, asOf); err != nil || len(got) != 0 {
		t.Fatalf("simulation orders carried: %+v, %v", got, err)
	}
}
