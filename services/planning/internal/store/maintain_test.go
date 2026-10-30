package store_test

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func ptr[T any](v T) *T { return &v }

// TestPlannedOrderMaintenance covers FR-22.11 on the database: change under
// If-Match, stale versions, date and vendor rules, approve, delete and the
// lock-out of orders that are deleted or belong to a superseded version.
func TestPlannedOrderMaintenance(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	res := oneItemPlan(t)
	if _, _, err := st.RunPlan(ctx, runRequest(res, "m-1", "in-1")); err != nil {
		t.Fatal(err)
	}
	orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders: %+v, %v", orders, err)
	}
	o := orders[0]
	if o.Version != 1 || o.Status != "unprocessed" || o.VendorID != nil {
		t.Fatalf("fresh order = %+v", o)
	}

	vendor := ids.New()
	changed, err := st.UpdatePlannedOrder(ctx, o.ID, 1, store.OrderPatch{Qty: ptr(decimal.RequireFromString("55.5")), DeliveryDate: ptr("2026-11-20"), VendorID: &vendor})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Version != 2 || changed.Qty != "55.500000" || changed.DeliveryDate != "2026-11-20" || changed.OrderDate != o.OrderDate ||
		changed.VendorID == nil || *changed.VendorID != vendor || changed.Status != "unprocessed" {
		t.Fatalf("changed order = %+v", changed)
	}
	if _, err := st.UpdatePlannedOrder(ctx, o.ID, 1, store.OrderPatch{Qty: ptr(decimal.NewFromInt(1))}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale version: %v; want %s", err, store.CodeVersionConflict)
	}
	if _, err := st.UpdatePlannedOrder(ctx, o.ID, 2, store.OrderPatch{OrderDate: ptr("2026-11-21")}); problemCode(err) != store.CodeOrderInvalid {
		t.Fatalf("order date after delivery: %v; want %s", err, store.CodeOrderInvalid)
	}
	approved, err := st.UpdatePlannedOrder(ctx, o.ID, 2, store.OrderPatch{Approve: true})
	if err != nil || approved.Status != "approved" || approved.Version != 3 {
		t.Fatalf("approve: %+v, %v", approved, err)
	}
	if got, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{Status: "approved"}, 10); err != nil || len(got) != 1 {
		t.Fatalf("approved filter: %+v, %v", got, err)
	}

	if err := st.DeletePlannedOrder(ctx, o.ID, 2); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("delete with stale version: %v", err)
	}
	if err := st.DeletePlannedOrder(ctx, o.ID, 3); err != nil {
		t.Fatal(err)
	}
	if got, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10); err != nil || len(got) != 0 {
		t.Fatalf("deleted order still listed: %+v, %v", got, err)
	}
	if got, err := st.GetPlannedOrder(ctx, o.ID); err != nil || got.Status != "deleted" || got.Version != 4 {
		t.Fatalf("deleted order read: %+v, %v", got, err)
	}
	if _, err := st.UpdatePlannedOrder(ctx, o.ID, 4, store.OrderPatch{Approve: true}); problemCode(err) != store.CodeOrderNotEditable {
		t.Fatalf("edit after delete: %v; want %s", err, store.CodeOrderNotEditable)
	}
	if _, err := st.UpdatePlannedOrder(ctx, ids.New(), 1, store.OrderPatch{Approve: true}); problemCode(err) != store.CodeOrderNotFound {
		t.Fatalf("unknown order: %v", err)
	}

	// A new run supersedes the version: its orders can no longer be maintained.
	if _, _, err := st.RunPlan(ctx, runRequest(res, "m-2", "in-2")); err != nil {
		t.Fatal(err)
	}
	next, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(next) != 1 {
		t.Fatalf("orders of version 2: %+v, %v", next, err)
	}
	if _, _, err := st.RunPlan(ctx, runRequest(res, "m-3", "in-3")); err != nil {
		t.Fatal(err)
	}
	if err := st.DeletePlannedOrder(ctx, next[0].ID, 1); problemCode(err) != store.CodeOrderNotEditable {
		t.Fatalf("delete in a superseded version: %v; want %s", err, store.CodeOrderNotEditable)
	}

	// Row-level security: another tenant cannot see or change the order.
	two := testkit.Ctx(testkit.TenantID(2), "planning.read")
	if _, err := st.UpdatePlannedOrder(two, next[0].ID, 1, store.OrderPatch{Approve: true}); problemCode(err) != store.CodeOrderNotFound {
		t.Fatalf("tenant 2 edits tenant 1's order: %v", err)
	}
}

// TestPlannedOrderMaintenance_VendorOnlyOnPurchase: a planned production
// order takes no vendor.
func TestPlannedOrderMaintenance_VendorOnlyOnPurchase(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	res := oneItemPlan(t)
	res.Orders[0].OrderType = "production"
	if _, _, err := st.RunPlan(ctx, runRequest(res, "p-1", "in")); err != nil {
		t.Fatal(err)
	}
	orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(orders) != 1 {
		t.Fatalf("orders: %+v, %v", orders, err)
	}
	vendor := ids.New()
	if _, err := st.UpdatePlannedOrder(ctx, orders[0].ID, 1, store.OrderPatch{VendorID: &vendor}); problemCode(err) != store.CodeOrderInvalid {
		t.Fatalf("vendor on production order: %v; want %s", err, store.CodeOrderInvalid)
	}
}

func cellIn(item, site, start, end, qty, sales string) store.ForecastCellInput {
	return store.ForecastCellInput{ItemID: item, SiteID: site, PeriodStart: start, PeriodEnd: end, Source: "manual",
		Qty: decimal.RequireFromString(qty), SalesQty: decimal.RequireFromString(sales)}
}

// TestForecastLines covers FR-22.8 on the database: model creation at version
// 0, transactions reduction (AC-22.6), recomputation of every series when the
// principle changes, stale writes, filters, forecast demand for a run and
// tenant isolation.
func TestForecastLines(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.read")
	itemA, itemB, site := ids.New(), ids.New(), ids.New()

	if _, _, err := st.ListForecastLines(ctx, "BASE", "", "", 10); problemCode(err) != store.CodeForecastNotFound {
		t.Fatalf("unknown model: %v", err)
	}
	m, err := st.UpsertForecastLines(ctx, "BASE", "", 0, nil, []store.ForecastCellInput{
		cellIn(itemA, site, "2026-11-02", "2026-11-08", "100", "30"),
		cellIn(itemA, site, "2026-11-09", "2026-11-15", "50", "0"),
		cellIn(itemB, site, "2026-11-02", "2026-11-08", "40", "10"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 1 || m.ReductionPrinciple != "transactions" {
		t.Fatalf("new model = %+v", m)
	}
	reduced := func(item string) []string {
		t.Helper()
		_, lines, err := st.ListForecastLines(ctx, "BASE", item, site, 10)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, l := range lines {
			out = append(out, l.ReducedQty)
		}
		return out
	}
	if got := reduced(itemA); len(got) != 2 || got[0] != "70.000000" || got[1] != "50.000000" {
		t.Fatalf("item A reduced = %v; want 70 then 50 (AC-22.6)", got)
	}

	if _, err := st.UpsertForecastLines(ctx, "BASE", "", 0, nil, []store.ForecastCellInput{cellIn(itemA, site, "2026-11-02", "2026-11-08", "1", "0")}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale write: %v; want %s", err, store.CodeVersionConflict)
	}

	// Switching to percent 20 recomputes item B too, although only A is written.
	m, err = st.UpsertForecastLines(ctx, "BASE", "", 1, &store.ModelSpec{ReductionPrinciple: "percent", ReductionPercent: decimal.NewFromInt(20)},
		[]store.ForecastCellInput{cellIn(itemA, site, "2026-11-02", "2026-11-08", "90", "30")})
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != 2 || m.ReductionPrinciple != "percent" {
		t.Fatalf("model after principle change = %+v", m)
	}
	if got := reduced(itemA); got[0] != "72.000000" || got[1] != "40.000000" {
		t.Fatalf("item A reduced = %v; want 72 then 40", got)
	}
	if got := reduced(itemB); len(got) != 1 || got[0] != "32.000000" {
		t.Fatalf("item B reduced = %v; want 32", got)
	}

	// The run reads periods that have not ended before the plan date.
	demand, err := st.ForecastDemand(ctx, "BASE", site, time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(demand) != 1 || demand[0].ItemID != itemA || demand[0].PeriodStart.Format("2006-01-02") != "2026-11-09" || !demand[0].ReducedQty.Equal(decimal.NewFromInt(40)) {
		t.Fatalf("forecast demand = %+v; want item A 40 from 2026-11-09", demand)
	}
	if _, err := st.ForecastDemand(ctx, "NOPE", site, time.Now()); problemCode(err) != store.CodeForecastNotFound {
		t.Fatalf("unknown model demand: %v", err)
	}

	two := testkit.Ctx(testkit.TenantID(2), "planning.read")
	if _, _, err := st.ListForecastLines(two, "BASE", "", "", 10); problemCode(err) != store.CodeForecastNotFound {
		t.Fatalf("tenant 2 reads tenant 1's model: %v", err)
	}
	// Tenant 2 has its own model BASE, created at version 0.
	if m2, err := st.UpsertForecastLines(two, "BASE", "", 0, nil, []store.ForecastCellInput{cellIn(itemA, site, "2026-11-02", "2026-11-08", "5", "0")}); err != nil || m2.Version != 1 {
		t.Fatalf("tenant 2 first write: %+v, %v", m2, err)
	}
}
