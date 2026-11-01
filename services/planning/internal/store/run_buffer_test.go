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

// TestRunBuffers covers what a plan run reads for DDMRP (FR-22.16): the
// site-scope buffer of an item whose site-scope coverage is a decoupling point,
// for the caller's tenant only; warehouse buffers and items whose coverage is
// not a decoupling point (priority-based alone is not) are not returned.
func TestRunBuffers(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.setup.maintain", "planning.read", "planning.run")
	profile, _, err := st.CreateBufferProfile(ctx, store.BufferProfileCreate{Spec: br2211Profile("BUF-RUN")})
	if err != nil {
		t.Fatal(err)
	}
	group, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: domain.DefaultCoverageGroup("REQ", domain.CoverageRequirement)})
	if err != nil {
		t.Fatal(err)
	}
	site, wh := ids.New(), ids.New()
	managed, plain := ids.New(), ids.New()
	for _, item := range []string{managed, plain} {
		if _, err := st.UpsertDdmrpBuffer(ctx, br2211Buffer(t, item, site, "", profile.ID, 90)); err != nil {
			t.Fatal(err)
		}
		cov := domain.DefaultItemCoverage(item, site, group.ID)
		cov.DecouplingPoint = item != plain
		cov.PriorityBased = true
		if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: cov}); err != nil {
			t.Fatal(err)
		}
	}
	// A buffer in a warehouse scope is not the site's buffer.
	if _, err := st.UpsertDdmrpBuffer(ctx, br2211Buffer(t, ids.New(), site, wh, profile.ID, 10)); err != nil {
		t.Fatal(err)
	}
	all := []string{managed, plain}
	got, err := st.RunBuffers(ctx, site, "", all)
	if err != nil || len(got) != 1 {
		t.Fatalf("RunBuffers = %+v, %v; want the decoupling point only", got, err)
	}
	var b domain.RunBuffer
	for _, g := range got {
		if g.ItemID == managed {
			b = g
		}
		if g.ItemID == plain {
			t.Fatalf("an item that is not a decoupling point was returned: %+v", g)
		}
	}
	if b.Zone != domain.ZoneYellow || !b.PlannedQty.Equal(decimal.NewFromInt(115)) || !b.Priority.Equal(decimal.RequireFromString("0.5306")) || b.DLTDays.IsZero() {
		t.Fatalf("BR-22.11 buffer as read by the run: %+v", b)
	}
	other := testkit.Ctx(testkit.TenantID(2), "planning.run")
	if got, err := st.RunBuffers(other, site, "", all); err != nil || len(got) != 0 {
		t.Fatalf("other tenant sees %+v, %v", got, err)
	}
	if got, err := st.RunBuffers(ctx, ids.New(), "", all); err != nil || len(got) != 0 {
		t.Fatalf("other site: %+v, %v", got, err)
	}
	if got, err := st.RunBuffers(ctx, site, "", nil); err != nil || got != nil {
		t.Fatalf("no items: %+v, %v", got, err)
	}
}

// TestRunBuffers_LegalEntity: a run of a plan of one legal entity reads only
// that entity's buffers, and an entity-scoped caller never sees another's.
func TestRunBuffers_LegalEntity(t *testing.T) {
	st, _, _ := setup(t)
	entityA, entityB := ids.New(), ids.New()
	ctxA, ctxB, all := scopedCtx(testkit.TenantID(1), entityA), scopedCtx(testkit.TenantID(1), entityB), scopedCtx(testkit.TenantID(1), "")
	profile, _, err := st.CreateBufferProfile(ctxA, store.BufferProfileCreate{Spec: br2211Profile("BUF-LE")})
	if err != nil {
		t.Fatal(err)
	}
	group, _, err := st.CreateCoverageGroup(ctxA, store.CoverageCreate{Spec: domain.DefaultCoverageGroup("REQ", domain.CoverageRequirement)})
	if err != nil {
		t.Fatal(err)
	}
	item, site := ids.New(), ids.New()
	if _, err := st.UpsertDdmrpBuffer(ctxA, br2211Buffer(t, item, site, "", profile.ID, 90)); err != nil {
		t.Fatal(err)
	}
	cov := domain.DefaultItemCoverage(item, site, group.ID)
	cov.DecouplingPoint = true
	if _, _, err := st.CreateItemCoverage(ctxA, store.ItemCoverageCreate{Spec: cov}); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		ctx    context.Context
		entity string
		want   int
	}{
		"entity A's plan":                 {ctxA, entityA, 1},
		"all-entity run of A's plan":      {all, entityA, 1},
		"all-entity run of B's plan":      {all, entityB, 0},
		"entity B caller":                 {ctxB, entityB, 0},
		"entity B caller naming entity A": {ctxB, entityA, 0},
	} {
		if got, err := st.RunBuffers(c.ctx, site, c.entity, []string{item}); err != nil || len(got) != c.want {
			t.Errorf("%s: RunBuffers = %+v, %v; want %d", name, got, err, c.want)
		}
	}
}

// TestRunPlan_StoresBufferPriorityAndRedZoneMessage: the order of a red-zone
// buffer is stored with the buffer's priority and zone, and the PLAN-0010
// message is stored and read back by its action (migration 0019).
func TestRunPlan_StoresBufferPriorityAndRedZoneMessage(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	item := ids.New()
	buffers := []domain.RunBuffer{{ItemID: item, DLTDays: decimal.NewFromInt(10), PlannedQty: decimal.NewFromInt(80), Priority: decimal.RequireFromString("0.2449"), Zone: domain.ZoneRed}}
	items := domain.ApplyBuffers([]domain.Item{
		{ID: item, Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2},
		{ID: ids.New(), Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2,
			Demand: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 5, Qty: decimal.NewFromInt(3), Priority: domain.PrioritySales}}},
	}, buffers)
	res, err := domain.Run(0, items)
	if err != nil {
		t.Fatal(err)
	}
	res = domain.AddBufferResults(res, buffers)
	if _, _, err := st.RunPlan(ctx, runRequest(res, "run-red", "in-red")); err != nil {
		t.Fatal(err)
	}
	orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(orders) != 2 {
		t.Fatalf("orders = %+v, %v", orders, err)
	}
	var bufferOrder string
	for _, o := range orders {
		if o.ItemID != item {
			if o.PlanningPriority != nil || o.BufferZone != nil {
				t.Errorf("an order without a buffer has a priority: %+v", o)
			}
			continue
		}
		bufferOrder = o.Number
		if o.PlanningPriority == nil || !decimal.RequireFromString(*o.PlanningPriority).Equal(decimal.RequireFromString("0.2449")) || o.BufferZone == nil || *o.BufferZone != domain.ZoneRed {
			t.Errorf("buffer order = %+v", o)
		}
		got, err := st.GetPlannedOrder(ctx, o.ID)
		if err != nil || got.PlanningPriority == nil || got.BufferZone == nil {
			t.Errorf("get buffer order = %+v, %v", got, err)
		}
	}
	msgs, err := st.ListMessages(ctx, "MAIN", domain.ActionRedZone, 10)
	if err != nil || len(msgs) != 1 || msgs[0].SupplyRef != bufferOrder || !decimal.RequireFromString(msgs[0].Qty).Equal(decimal.NewFromInt(80)) {
		t.Fatalf("red-zone messages = %+v, %v", msgs, err)
	}
}
