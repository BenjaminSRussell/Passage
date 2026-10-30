package store_test

import (
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// demandPlan plans one sales demand of qty for each item.
func demandPlan(t *testing.T, qty int64, items ...string) domain.Result {
	t.Helper()
	in := make([]domain.Item, 0, len(items))
	for _, item := range items {
		in = append(in, domain.Item{
			ID: item, Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2,
			Demand: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 10, Qty: decimal.NewFromInt(qty), Priority: domain.PrioritySales}},
		})
	}
	res, err := domain.Run(0, in)
	if err != nil || len(res.Orders) != len(items) {
		t.Fatalf("fixture: %v %+v", err, res.Orders)
	}
	return res
}

// TestDiffVersions compares real runs: identical input has equal hashes and no
// difference, a larger demand is a changed order, and a different item is one
// order added and one removed (FR-22.14).
func TestDiffVersions(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	item := ids.New()
	for i, c := range []struct {
		item string
		qty  int64
	}{{item, 40}, {item, 40}, {item, 55}, {ids.New(), 55}} {
		req := runRequest(demandPlan(t, c.qty, c.item), "k"+string(rune('a'+i)), "in"+string(rune('a'+i)))
		if _, _, err := st.RunPlan(ctx, req); err != nil {
			t.Fatal(err)
		}
	}

	d, err := st.DiffVersions(ctx, "MAIN", 1, 2, store.MaxPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Identical || d.Unchanged != 1 || len(d.Added)+len(d.Removed)+len(d.Changed) != 0 || d.From.Number != 1 || d.To.Number != 2 || len(d.QtyDelta) != 0 {
		t.Fatalf("identical runs differ: %+v", d)
	}

	d, err = st.DiffVersions(ctx, "MAIN", 2, 3, store.MaxPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if d.ChangedCount != 1 || len(d.Changed) != 1 || d.Changed[0].QtyDelta != "15" || d.Identical || d.QtyDelta["purchase"] != "15" || d.Truncated {
		t.Fatalf("changed demand: %+v", d)
	}

	d, err = st.DiffVersions(ctx, "MAIN", 3, 4, store.MaxPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Added) != 1 || len(d.Removed) != 1 || d.Added[0].Qty != "55" || d.Added[0].VendorID != nil || d.QtyDelta["purchase"] != "0" {
		t.Fatalf("swapped item: %+v", d)
	}

	if _, err := st.DiffVersions(ctx, "MAIN", 1, 99, store.MaxPageSize); problemCode(err) != "PLAN-VERSION-NOT-FOUND" {
		t.Fatalf("missing version: %v", err)
	}
	if _, err := st.DiffVersions(ctx, "NOPE", 1, 2, store.MaxPageSize); problemCode(err) != "PLAN-VERSION-NOT-FOUND" {
		t.Fatalf("missing plan: %v", err)
	}
	other := testkit.Ctx(testkit.TenantID(2), "planning.read")
	if _, err := st.DiffVersions(other, "MAIN", 1, 2, store.MaxPageSize); problemCode(err) != "PLAN-VERSION-NOT-FOUND" {
		t.Fatalf("tenant 2 reads tenant 1: %v", err)
	}
}

// The counts and quantity deltas cover every order while each list stops at
// the limit; a deleted planned order is on neither side.
func TestDiffVersions_LimitAndDeletedOrders(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	for i, items := range [][]string{{ids.New(), ids.New(), ids.New()}, {ids.New(), ids.New()}} {
		req := runRequest(demandPlan(t, 10, items...), "w"+string(rune('a'+i)), "wide"+string(rune('a'+i)))
		req.ItemsPlanned = len(items)
		if _, _, err := st.RunPlan(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	d, err := st.DiffVersions(ctx, "MAIN", 1, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if d.AddedCount != 2 || d.RemovedCount != 3 || len(d.Added) != 1 || len(d.Removed) != 1 || !d.Truncated || d.QtyDelta["purchase"] != "-10" {
		t.Fatalf("limited diff: %+v", d)
	}

	if _, err := admin.Exec(ctx, `update planned_order o set status = 'deleted' from plan_version v
		where v.id = o.plan_version_id and v.number = 1 and o.item_id::text = $1`, d.Removed[0].ItemID); err != nil {
		t.Fatal(err)
	}
	d, err = st.DiffVersions(ctx, "MAIN", 1, 2, store.MaxPageSize)
	if err != nil {
		t.Fatal(err)
	}
	if d.RemovedCount != 2 || len(d.Removed) != 2 || d.Truncated || d.QtyDelta["purchase"] != "0" {
		t.Fatalf("deleted order still compared: %+v", d)
	}
}

// A caller scoped to another legal entity cannot diff the plan's versions.
func TestDiffVersions_LegalEntityScope(t *testing.T) {
	st, _, _ := setup(t)
	entityA, entityB := ids.New(), ids.New()
	ctxA, ctxB := scopedCtx(testkit.TenantID(1), entityA), scopedCtx(testkit.TenantID(1), entityB)
	for i := range 2 {
		if _, _, err := st.RunPlan(ctxA, runRequest(demandPlan(t, 5, ids.New()), "le"+string(rune('a'+i)), "le"+string(rune('a'+i)))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.DiffVersions(ctxA, "MAIN", 1, 2, store.MaxPageSize); err != nil {
		t.Fatalf("entity A diff of its own plan: %v", err)
	}
	_, err := st.DiffVersions(ctxB, "MAIN", 1, 2, store.MaxPageSize)
	requireStatus(t, err, http.StatusNotFound, "entity B diff of entity A's plan")
}
