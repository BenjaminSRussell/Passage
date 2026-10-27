package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/events"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// threePurchasesAndOneProduction is AC-22.8's three planned purchases due the
// same day (three purchased items, the lines of one purchase order), plus a
// produced item with its own planned order.
func threePurchasesAndOneProduction(t *testing.T) domain.Result {
	t.Helper()
	item := func(orderType string, lead int, qty int64) domain.Item {
		return domain.Item{ID: ids.New(), Coverage: domain.CoverageRequirement, OrderType: orderType, LeadTimeDays: lead,
			Demand: []domain.Requirement{{Ref: fmt.Sprintf("SO-%d", qty), RefType: domain.RefSales, Date: 10, Qty: decimal.NewFromInt(qty), Priority: domain.PrioritySales}}}
	}
	res, err := domain.Run(0, []domain.Item{
		item(domain.OrderPurchase, 2, 40), item(domain.OrderPurchase, 2, 50), item(domain.OrderPurchase, 2, 60), item(domain.OrderProduction, 3, 7),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Orders) != 4 {
		t.Fatalf("want 4 planned orders, got %+v", res.Orders)
	}
	return res
}

// TestFirmPlannedOrders covers FR-22.10, BR-22.9 and AC-22.8 on the database:
// three approved purchases for one vendor and date become one purchase order,
// the production order its own target; each order is firmed with its target
// id and a new ETag, one event per order is in the outbox, the key replays,
// and a second firming of a firmed order is 409 PLAN-0002.
func TestFirmPlannedOrders(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	if _, _, err := st.RunPlan(ctx, runRequest(threePurchasesAndOneProduction(t), "run-1", "in-1")); err != nil {
		t.Fatal(err)
	}
	orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(orders) != 4 {
		t.Fatalf("orders: %+v, %v", orders, err)
	}
	vendor := ids.New()
	var all, purchases []string
	var delivery string
	versions := map[string]int{}
	for _, o := range orders {
		all = append(all, o.ID)
		if o.OrderType != domain.OrderPurchase {
			continue
		}
		purchases = append(purchases, o.ID)
		delivery = o.DeliveryDate
		approved, err := st.UpdatePlannedOrder(ctx, o.ID, o.Version, store.OrderPatch{VendorID: &vendor, Approve: true})
		if err != nil {
			t.Fatal(err)
		}
		versions[o.ID] = approved.Version
	}
	slices.Sort(all)
	slices.Sort(purchases)

	req := store.FirmRequest{IdempotencyKey: "firm-1", InputHash: "h1", Grouping: domain.GroupVendorDate, OrderIDs: all}
	batch, replayed, err := st.FirmPlannedOrders(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if replayed || batch.Firmed != 4 || len(batch.Groups) != 2 {
		t.Fatalf("batch = %+v (replayed %v), want 4 orders in 2 targets", batch, replayed)
	}
	po, prod := batch.Groups[1], batch.Groups[0]
	if po.TargetType != "purchase-order" || !slices.Equal(po.PlannedOrderIDs, purchases) || po.Qty != "150" ||
		po.VendorID == nil || *po.VendorID != vendor || po.DeliveryDate != delivery {
		t.Fatalf("purchase group = %+v, want one purchase order of 150 with the three purchases", po)
	}
	if prod.TargetType != "production-order" || len(prod.PlannedOrderIDs) != 1 || prod.TargetID == po.TargetID {
		t.Fatalf("production group = %+v", prod)
	}
	for _, id := range purchases {
		o, err := st.GetPlannedOrder(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if o.Status != "firmed" || o.FirmedTargetID == nil || *o.FirmedTargetID != po.TargetID ||
			o.FirmedTargetType == nil || *o.FirmedTargetType != "purchase-order" || o.Version != versions[id]+1 {
			t.Fatalf("firmed order = %+v, want firmed into %s with version %d", o, po.TargetID, versions[id]+1)
		}
	}

	rows, err := admin.Query(context.Background(), `select payload from outbox where type = $1`, events.PlannedOrderFirmed)
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]string{}
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
		if evt.Data.BatchID != batch.ID || evt.Data.ItemID == "" || evt.Data.SiteID == "" || evt.Data.DeliveryDate == "" {
			t.Fatalf("event data = %+v", evt.Data)
		}
		targets[evt.Data.PlannedOrderID] = evt.Data.TargetID
	}
	rows.Close()
	if len(targets) != 4 || targets[purchases[0]] != po.TargetID || targets[prod.PlannedOrderIDs[0]] != prod.TargetID {
		t.Fatalf("firmed events by order = %v, want one per order with its target", targets)
	}

	again, replayed, err := st.FirmPlannedOrders(ctx, req)
	if err != nil || !replayed || again.ID != batch.ID || again.Groups[1].TargetID != po.TargetID {
		t.Fatalf("replay = %+v (replayed %v), %v; want the stored batch", again, replayed, err)
	}
	if _, _, err := st.FirmPlannedOrders(ctx, store.FirmRequest{IdempotencyKey: "firm-1", InputHash: "h2", Grouping: domain.GroupNone, OrderIDs: all}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v; want %s", err, store.CodeIdempotencyReuse)
	}
	if _, _, err := st.FirmPlannedOrders(ctx, store.FirmRequest{IdempotencyKey: "firm-2", InputHash: "h3", Grouping: domain.GroupNone, OrderIDs: purchases[:1]}); problemCode(err) != store.CodeAlreadyFirmed {
		t.Fatalf("firming a firmed order: %v; want %s", err, store.CodeAlreadyFirmed)
	}
	if _, err := st.UpdatePlannedOrder(ctx, purchases[0], versions[purchases[0]]+1, store.OrderPatch{Approve: true}); problemCode(err) != store.CodeOrderNotEditable {
		t.Fatalf("maintaining a firmed order: %v; want %s", err, store.CodeOrderNotEditable)
	}
}

// TestFirmPlannedOrders_AllOrNothing: one bad id (unknown, deleted, of a
// superseded version, or of another tenant) refuses the whole batch and firms
// nothing.
func TestFirmPlannedOrders_AllOrNothing(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	other := testkit.Ctx(testkit.TenantID(2), "planning.run", "planning.read")
	if _, _, err := st.RunPlan(ctx, runRequest(threePurchasesAndOneProduction(t), "run-1", "in-1")); err != nil {
		t.Fatal(err)
	}
	old, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RunPlan(ctx, runRequest(threePurchasesAndOneProduction(t), "run-2", "in-2")); err != nil {
		t.Fatal(err)
	}
	cur, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeletePlannedOrder(ctx, cur[1].ID, cur[1].Version); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RunPlan(other, runRequest(oneItemPlan(t), "run-x", "in-x")); err != nil {
		t.Fatal(err)
	}
	foreign, err := st.ListPlannedOrders(other, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(foreign) != 1 {
		t.Fatalf("other tenant's orders: %+v, %v", foreign, err)
	}
	tests := []struct {
		name, bad, code string
	}{
		{"unknown id", ids.New(), store.CodeOrderNotFound},
		{"another tenant's order", foreign[0].ID, store.CodeOrderNotFound},
		{"deleted order", cur[1].ID, store.CodeOrderNotEditable},
		{"order of a superseded version", old[0].ID, store.CodeOrderNotEditable},
	}
	for i, tc := range tests {
		batch := []string{cur[0].ID, tc.bad}
		_, _, err := st.FirmPlannedOrders(ctx, store.FirmRequest{IdempotencyKey: fmt.Sprintf("bad-%d", i), InputHash: "h", Grouping: domain.GroupNone, OrderIDs: batch})
		if problemCode(err) != tc.code {
			t.Fatalf("%s: %v; want %s", tc.name, err, tc.code)
		}
	}
	if o, err := st.GetPlannedOrder(ctx, cur[0].ID); err != nil || o.Status != "unprocessed" {
		t.Fatalf("a refused batch firmed %+v, %v", o, err)
	}
	var batches int
	if err := admin.QueryRow(context.Background(), `select count(*) from firming_batch`).Scan(&batches); err != nil || batches != 0 {
		t.Fatalf("firming batches after refusals = %d, %v; want 0", batches, err)
	}
}

// TestRunPlan_PurgesOldVersions keeps the newest versions of a plan and every
// version that holds a firmed planned order (FR-22.1, PLANNING_MAX_VERSIONS).
func TestRunPlan_PurgesOldVersions(t *testing.T) {
	st, admin, _ := setup(t)
	st.WithMaxVersions(store.MinMaxVersions)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	res := oneItemPlan(t)
	if _, _, err := st.RunPlan(ctx, runRequest(res, "p-1", "in-1")); err != nil {
		t.Fatal(err)
	}
	first, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(first) != 1 {
		t.Fatalf("orders: %+v, %v", first, err)
	}
	if _, _, err := st.FirmPlannedOrders(ctx, store.FirmRequest{IdempotencyKey: "f-1", InputHash: "h", Grouping: domain.GroupNone, OrderIDs: []string{first[0].ID}}); err != nil {
		t.Fatal(err)
	}
	for n := 2; n <= 5; n++ {
		if _, _, err := st.RunPlan(ctx, runRequest(res, fmt.Sprintf("p-%d", n), fmt.Sprintf("in-%d", n))); err != nil {
			t.Fatal(err)
		}
	}
	versions, err := st.ListVersions(ctx, "MAIN", 50)
	if err != nil {
		t.Fatal(err)
	}
	var kept []int
	for _, v := range versions {
		kept = append(kept, v.Number)
	}
	if !slices.Equal(kept, []int{5, 4, 1}) || versions[0].Status != "current" {
		t.Fatalf("versions kept = %v (newest %s), want 5, 4 and the firmed 1", kept, versions[0].Status)
	}
	var orphans int
	if err := admin.QueryRow(context.Background(), `select (select count(*) from planned_order where plan_version_id not in (select id from plan_version))
		+ (select count(*) from pegging where plan_version_id not in (select id from plan_version))
		+ (select count(*) from action_message where plan_version_id not in (select id from plan_version))`).Scan(&orphans); err != nil || orphans != 0 {
		t.Fatalf("rows left of purged versions = %d, %v", orphans, err)
	}
	if o, err := st.GetPlannedOrder(ctx, first[0].ID); err != nil || o.Status != "firmed" {
		t.Fatalf("firmed order after purge = %+v, %v", o, err)
	}
}

// TestRunPlan_StoresCancelAndDecrease: the FR-22.7 surplus messages pass the
// action_message check and filter by action.
func TestRunPlan_StoresCancelAndDecrease(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	res, err := domain.Run(0, []domain.Item{{
		ID: ids.New(), Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, CoverageDays: 30,
		Demand: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 40, Qty: decimal.NewFromInt(100), Priority: domain.PrioritySales}},
		Receipts: []domain.Requirement{
			{Ref: "PO-1", RefType: "purchase-line", Date: 40, Qty: decimal.NewFromInt(300)},
			{Ref: "PO-2", RefType: "purchase-line", Date: 50, Qty: decimal.NewFromInt(80)},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RunPlan(ctx, runRequest(res, "run-1", "in-1")); err != nil {
		t.Fatal(err)
	}
	for action, want := range map[string]string{domain.ActionDecrease: "PO-1 200.000000", domain.ActionCancel: "PO-2 80.000000"} {
		msgs, err := st.ListMessages(ctx, "MAIN", action, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(msgs) != 1 || msgs[0].SupplyRef+" "+msgs[0].Qty != want {
			t.Fatalf("%s messages = %+v, want %s", action, msgs, want)
		}
	}
}

// TestFirmPlannedOrders_ConcurrentRetryReplays: two requests with the same key
// at once firm once; the one that waited on the row locks replays the batch.
func TestFirmPlannedOrders_ConcurrentRetryReplays(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	if _, _, err := st.RunPlan(ctx, runRequest(threePurchasesAndOneProduction(t), "run-1", "in-1")); err != nil {
		t.Fatal(err)
	}
	orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, o := range orders {
		all = append(all, o.ID)
	}
	slices.Sort(all)
	req := store.FirmRequest{IdempotencyKey: "same", InputHash: "h", Grouping: domain.GroupVendorDate, OrderIDs: all}
	type outcome struct {
		batch    store.FirmBatch
		replayed bool
		err      error
	}
	results := make(chan outcome, 2)
	for range 2 {
		go func() {
			b, r, err := st.FirmPlannedOrders(ctx, req)
			results <- outcome{b, r, err}
		}()
	}
	a, b := <-results, <-results
	if a.err != nil || b.err != nil {
		t.Fatalf("errors: %v, %v", a.err, b.err)
	}
	if a.batch.ID != b.batch.ID || a.replayed == b.replayed {
		t.Fatalf("batches %s (replayed %v) and %s (replayed %v); want one firming and one replay", a.batch.ID, a.replayed, b.batch.ID, b.replayed)
	}
}
