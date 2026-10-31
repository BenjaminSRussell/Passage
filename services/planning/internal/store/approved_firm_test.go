package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// TestRunPlan_FirmsApprovedOrders covers FR-22.11: two approved purchase orders
// of one vendor and date become firmed by the next run, grouped vendor-date
// into one target order, in one batch with one plannedorder.firmed.v1 each.
// Before that run they are already carried as supply; another tenant's run of
// a plan with the same code firms none of them; unprocessed orders stay open;
// a replay of the run and a third run firm nothing more.
func TestRunPlan_FirmsApprovedOrders(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read", "planning.order.maintain")
	site := ids.New()
	asOf := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	first := runRequest(threePurchasesAndOneProduction(t), "run-1", "in-1")
	first.SiteID = site
	if _, _, err := st.RunPlan(ctx, first); err != nil {
		t.Fatal(err)
	}
	orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(orders) != 4 {
		t.Fatalf("orders: %+v, %v", orders, err)
	}
	vendor := ids.New()
	approved := map[string]bool{}
	for _, o := range orders {
		if o.OrderType != "purchase" || len(approved) == 2 {
			continue
		}
		withVendor, err := st.UpdatePlannedOrder(ctx, o.ID, o.Version, store.OrderPatch{VendorID: &vendor})
		if err != nil {
			t.Fatalf("set vendor: %v", err)
		}
		got, err := st.UpdatePlannedOrder(ctx, o.ID, withVendor.Version, store.OrderPatch{Approve: true})
		if err != nil || got.Status != "approved" {
			t.Fatalf("approve: %+v, %v", got, err)
		}
		approved[o.ID] = true
	}

	// The approved orders are supply of the run that firms them (carryForward).
	supply, err := st.FirmedSupply(ctx, "MAIN", site, asOf)
	if err != nil || len(supply) != 2 || !approved[supply[0].OrderID] || !approved[supply[1].OrderID] {
		t.Fatalf("supply before the run = %+v, %v; want the two approved orders", supply, err)
	}

	// Another tenant's run of its own plan MAIN leaves tenant 1's orders alone.
	other := testkit.Ctx(testkit.TenantID(2), "planning.run", "planning.read")
	otherRun := runRequest(threePurchasesAndOneProduction(t), "run-other", "in-other")
	otherRun.SiteID = site
	if v, _, err := st.RunPlan(other, otherRun); err != nil || v.ApprovedFirmed != 0 {
		t.Fatalf("other tenant run: %+v, %v", v, err)
	}
	if still, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{Status: "approved"}, 10); err != nil || len(still) != 2 {
		t.Fatalf("approved after other tenant's run: %d, %v; want 2", len(still), err)
	}

	countEvents := func() (n int) {
		t.Helper()
		if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = 'dev.passage.planning.plannedorder.firmed.v1'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	eventsBefore := countEvents()
	second := runRequest(threePurchasesAndOneProduction(t), "run-2", "in-2")
	second.SiteID = site
	v2, _, err := st.RunPlan(ctx, second)
	if err != nil {
		t.Fatal(err)
	}
	if v2.ApprovedFirmed != 2 {
		t.Fatalf("approvedFirmed = %d, want 2", v2.ApprovedFirmed)
	}
	var firmed, open, targets int
	if err := admin.QueryRow(context.Background(), `select
		count(*) filter (where status = 'firmed' and firmed_target_type = 'purchase-order' and firmed_batch_id is not null),
		count(*) filter (where status = 'unprocessed'),
		count(distinct firmed_target_id)
		from planned_order where plan_version_id = (select id from plan_version where tenant_id = $1 and number = 1)`,
		testkit.TenantID(1)).Scan(&firmed, &open, &targets); err != nil {
		t.Fatal(err)
	}
	if firmed != 2 || open != 2 || targets != 1 {
		t.Fatalf("version 1: %d firmed, %d unprocessed, %d target orders; want 2, 2 and 1", firmed, open, targets)
	}
	var batches, batchFirmed int
	var grouping string
	if err := admin.QueryRow(context.Background(), `select count(*), coalesce(max(firmed_count), 0), coalesce(max(grouping), '') from firming_batch
		where idempotency_key = 'plan-run-approved:' || $1`, v2.ID).Scan(&batches, &batchFirmed, &grouping); err != nil {
		t.Fatal(err)
	}
	if batches != 1 || batchFirmed != 2 || grouping != "vendor-date" {
		t.Fatalf("batch: %d rows, firmed %d, grouping %q; want 1, 2, vendor-date", batches, batchFirmed, grouping)
	}
	if n := countEvents() - eventsBefore; n != 2 {
		t.Fatalf("firmed events = %d, want 2", n)
	}
	got, err := st.GetVersion(ctx, "MAIN", 2)
	if err != nil || got.ApprovedFirmed != 2 {
		t.Fatalf("stored version: %+v, %v", got, err)
	}

	// A replay returns the stored count and firms nothing again.
	replay, replayed, err := st.RunPlan(ctx, second)
	if err != nil || !replayed || replay.ApprovedFirmed != 2 || countEvents()-eventsBefore != 2 {
		t.Fatalf("replay: %+v, replayed %v, %v", replay, replayed, err)
	}
	// The firmed orders stay supply after their version was demoted.
	if supply, err := st.FirmedSupply(ctx, "MAIN", site, asOf); err != nil || len(supply) != 2 {
		t.Fatalf("supply after the run = %+v, %v; want the two firmed orders", supply, err)
	}
	third := runRequest(threePurchasesAndOneProduction(t), "run-3", "in-3")
	third.SiteID = site
	v3, _, err := st.RunPlan(ctx, third)
	if err != nil || v3.ApprovedFirmed != 0 || countEvents()-eventsBefore != 2 {
		t.Fatalf("third run: %+v, %v", v3, err)
	}
}
