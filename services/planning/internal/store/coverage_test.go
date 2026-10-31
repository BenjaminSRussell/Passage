package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/events"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// TestCoverageGroups covers FR-22.2 setup on the database: create with the
// defaults, idempotent replay, duplicate code, If-Match update, the table
// checks behind the domain rules, and tenant isolation under RLS.
func TestCoverageGroups(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.setup.maintain")

	spec := domain.DefaultCoverageGroup("REQ", domain.CoverageRequirement)
	g, replayed, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{IdempotencyKey: "cg-1", InputHash: "h1", Spec: spec})
	if err != nil || replayed || g.Version != 1 || g.Spec != spec {
		t.Fatalf("create = %+v (replayed %v), %v", g, replayed, err)
	}
	again, replayed, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{IdempotencyKey: "cg-1", InputHash: "h1", Spec: spec})
	if err != nil || !replayed || again.ID != g.ID {
		t.Fatalf("replay = %+v (replayed %v), %v; want the first group", again, replayed, err)
	}
	if _, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{IdempotencyKey: "cg-1", InputHash: "h2", Spec: spec}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if _, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: spec}); problemCode(err) != store.CodeCoverageExists {
		t.Fatalf("duplicate code: %v; want %s", err, store.CodeCoverageExists)
	}
	minmax := domain.DefaultCoverageGroup("MIN", domain.CoverageMinMax)
	if _, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: minmax}); err != nil {
		t.Fatal(err)
	}

	list, err := st.ListCoverageGroups(ctx, 10)
	if err != nil || len(list) != 2 || list[0].Spec.Code != "MIN" || list[1].Spec.Code != "REQ" {
		t.Fatalf("list = %+v, %v; want MIN then REQ", list, err)
	}

	changed := spec
	changed.FirmingFenceDays, changed.AutoFirm, changed.CoverageTimeFenceDays = 5, true, 60
	changed.SafetyStockPolicy = domain.SafetyFirstIssue
	updated, err := st.UpdateCoverageGroup(ctx, 1, changed)
	if err != nil || updated.Version != 2 || updated.ID != g.ID {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	read, err := st.GetCoverageGroup(ctx, "REQ")
	if err != nil || read.Version != 2 || read.Spec != changed {
		t.Fatalf("read back = %+v, %v; want %+v", read, err, changed)
	}
	if _, err := st.UpdateCoverageGroup(ctx, 1, changed); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale update: %v; want %s", err, store.CodeVersionConflict)
	}
	missing := changed
	missing.Code = "NOPE"
	if _, err := st.UpdateCoverageGroup(ctx, 1, missing); problemCode(err) != store.CodeCoverageNotFound {
		t.Fatalf("update of unknown group: %v", err)
	}
	if _, err := st.GetCoverageGroup(ctx, "NOPE"); problemCode(err) != store.CodeCoverageNotFound {
		t.Fatalf("get of unknown group: %v", err)
	}

	// The table refuses what the domain refuses, even past the API.
	bad := domain.DefaultCoverageGroup("BAD", domain.CoverageRequirement)
	bad.AutoFirm = true
	if _, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: bad}); err == nil {
		t.Fatal("auto-firm without a firming fence was stored")
	}

	badPolicy := domain.DefaultCoverageGroup("BADP", domain.CoverageRequirement)
	badPolicy.SafetyStockPolicy = "monthly"
	if _, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: badPolicy}); err == nil {
		t.Fatal("an unknown safety-stock policy was stored")
	}

	other := testkit.Ctx(testkit.TenantID(2), "planning.setup.maintain")
	if list, err := st.ListCoverageGroups(other, 10); err != nil || len(list) != 0 {
		t.Fatalf("other tenant sees %+v, %v", list, err)
	}
	if _, err := st.GetCoverageGroup(other, "REQ"); problemCode(err) != store.CodeCoverageNotFound {
		t.Fatalf("other tenant reads REQ: %v", err)
	}
	if _, _, err := st.CreateCoverageGroup(other, store.CoverageCreate{Spec: spec}); err != nil {
		t.Fatalf("codes are unique per tenant only: %v", err)
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from coverage_group`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("rows = %d, %v; want 3", n, err)
	}
}

// TestRunPlan_AutoFirmsInsideTheFirmingFence covers BR-22.4 on the database:
// the run stores its orders inside the firming fence as firmed, in one firming
// batch with the default grouping, and writes one plannedorder.firmed.v1 per
// auto-firmed order in its transaction. The order outside the fence stays open.
func TestRunPlan_AutoFirmsInsideTheFirmingFence(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	item := func(demandDay int) domain.Item {
		return domain.Item{ID: ids.New(), Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2,
			FirmingDays: 5, AutoFirm: true,
			Demand: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: demandDay, Qty: decimal.NewFromInt(10), Priority: domain.PrioritySales}}}
	}
	inside1, inside2, outside := item(4), item(4), item(20)
	res, err := domain.Run(0, []domain.Item{inside1, inside2, outside})
	if err != nil {
		t.Fatal(err)
	}
	req := runRequest(res, "run-af", "in-af")
	if _, _, err := st.RunPlan(ctx, req); err != nil {
		t.Fatal(err)
	}
	orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(orders) != 3 {
		t.Fatalf("orders: %+v, %v", orders, err)
	}
	firmed := map[string]string{}
	for _, o := range orders {
		switch o.ItemID {
		case outside.ID:
			if o.Status != "unprocessed" || o.FirmedTargetID != nil {
				t.Fatalf("order outside the fence = %+v, want open", o)
			}
		default:
			if o.Status != "firmed" || o.FirmedTargetType == nil || *o.FirmedTargetType != "purchase-order" || o.FirmedTargetID == nil {
				t.Fatalf("order inside the fence = %+v, want firmed into a purchase order", o)
			}
			firmed[o.ID] = *o.FirmedTargetID
		}
	}
	targets := map[string]bool{}
	for _, tid := range firmed {
		targets[tid] = true
	}
	if len(firmed) != 2 || len(targets) != 1 {
		t.Fatalf("firmed = %v; want both purchases due the same day in one purchase order", firmed)
	}

	var batches int
	var grouping string
	if err := admin.QueryRow(context.Background(), `select count(*), min(grouping) from firming_batch`).Scan(&batches, &grouping); err != nil ||
		batches != 1 || grouping != domain.DefaultGrouping {
		t.Fatalf("firming batches = %d (%s), %v; want one vendor-date batch", batches, grouping, err)
	}
	rows, err := admin.Query(context.Background(), `select payload from outbox where type = $1`, events.PlannedOrderFirmed)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var evt struct {
			Data events.PlannedOrderFirmedData `json:"data"`
		}
		if err := json.Unmarshal(raw, &evt); err != nil {
			t.Fatal(err)
		}
		if firmed[evt.Data.PlannedOrderID] != evt.Data.TargetID || evt.Data.BatchID == "" || evt.Data.SiteID != req.SiteID || evt.Data.Qty != "10" {
			t.Fatalf("event data = %+v", evt.Data)
		}
		seen++
	}
	rows.Close()
	if seen != 2 {
		t.Fatalf("firmed events = %d, want 2", seen)
	}

	// The replay of the run's key firms nothing again.
	if _, replayed, err := st.RunPlan(ctx, req); err != nil || !replayed {
		t.Fatalf("replay: %v (replayed %v)", err, replayed)
	}
	if err := admin.QueryRow(context.Background(), `select count(*) from firming_batch`).Scan(&batches); err != nil || batches != 1 {
		t.Fatalf("batches after replay = %d, %v", batches, err)
	}
}
