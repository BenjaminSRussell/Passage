package store_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func productionResult(t *testing.T) domain.Result {
	t.Helper()
	res, err := domain.Run(0, []domain.Item{{ID: ids.New(), Coverage: domain.CoverageRequirement, OrderType: domain.OrderProduction, LeadTimeDays: 3,
		Demand: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 10, Qty: decimal.NewFromInt(7), Priority: domain.PrioritySales}}}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func autoFirmResult(t *testing.T) domain.Result {
	t.Helper()
	res, err := domain.Run(0, []domain.Item{{ID: ids.New(), Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, LeadTimeDays: 2,
		FirmingDays: 5, AutoFirm: true,
		Demand: []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 4, Qty: decimal.NewFromInt(10), Priority: domain.PrioritySales}}}})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func orderIDs(t *testing.T, st *store.Postgres, ctx context.Context, plan string) []string {
	t.Helper()
	orders, err := st.ListPlannedOrders(ctx, plan, store.OrderFilter{}, 10)
	if err != nil || len(orders) == 0 {
		t.Fatalf("orders of %s: %+v, %v", plan, orders, err)
	}
	out := make([]string, len(orders))
	for i, o := range orders {
		out[i] = o.ID
	}
	return out
}

func TestLegalEntityScope_FirmingBatchesCoveragesAndBuffers(t *testing.T) {
	st, admin, app := setup(t)
	testkit.AssertTenantRLS(t, admin)
	entityA, entityB := ids.New(), ids.New()
	ctxA, ctxB := scopedCtx(testkit.TenantID(1), entityA), scopedCtx(testkit.TenantID(1), entityB)
	ctxAB := scopedCtx(testkit.TenantID(1), entityA+","+entityB)
	all, otherTenant := scopedCtx(testkit.TenantID(1), ""), scopedCtx(testkit.TenantID(2), "")
	item, site := ids.New(), ids.New()

	group, _, err := st.CreateCoverageGroup(ctxA, store.CoverageCreate{Spec: domain.DefaultCoverageGroup("REQ", domain.CoverageRequirement)})
	if err != nil {
		t.Fatal(err)
	}
	profile, _, err := st.CreateBufferProfile(ctxA, store.BufferProfileCreate{Spec: br2211Profile("BUF-A")})
	if err != nil {
		t.Fatal(err)
	}
	covA, _, err := st.CreateItemCoverage(ctxA, store.ItemCoverageCreate{Spec: domain.DefaultItemCoverage(item, site, group.ID)})
	if err != nil {
		t.Fatalf("entity A item coverage: %v", err)
	}
	if _, err := st.UpsertDdmrpBuffer(ctxA, br2211Buffer(t, item, site, "", profile.ID, 90)); err != nil {
		t.Fatalf("entity A buffer: %v", err)
	}
	planA := runRequest(productionResult(t), "run-a", "in-a")
	planA.PlanCode = "A-PLAN"
	if _, _, err := st.RunPlan(ctxA, planA); err != nil {
		t.Fatal(err)
	}
	ordersA := orderIDs(t, st, ctxA, "A-PLAN")
	batchA, _, err := st.FirmPlannedOrders(ctxA, store.FirmRequest{IdempotencyKey: "firm-a", InputHash: "ha", Grouping: domain.GroupVendorDate, OrderIDs: ordersA})
	if err != nil {
		t.Fatalf("entity A firming: %v", err)
	}
	autoFirm := runRequest(autoFirmResult(t), "run-af", "in-af")
	autoFirm.PlanCode = "A-AUTO"
	if _, _, err := st.RunPlan(ctxA, autoFirm); err != nil {
		t.Fatal(err)
	}

	for _, table := range []string{"firming_batch", "item_coverage", "ddmrp_buffer"} {
		t.Run(table, func(t *testing.T) {
			q := "select count(*) from " + table
			own := countAs(t, app, ctxA, q)
			if own == 0 {
				t.Fatal("entity A cannot read its own rows")
			}
			if n := countAs(t, app, all, q); n != own {
				t.Errorf("all-entity caller sees %d rows, want %d", n, own)
			}
			if n := countAs(t, app, ctxAB, q); n != own {
				t.Errorf("caller scoped to A and B sees %d rows, want %d", n, own)
			}
			if n := countAs(t, app, ctxB, q); n != 0 {
				t.Errorf("entity B reads %d of entity A's rows", n)
			}
			if n := countAs(t, app, otherTenant, q); n != 0 {
				t.Errorf("another tenant reads %d rows", n)
			}
		})
	}

	t.Run("the run's auto-firm batch is owned by the plan's entity", func(t *testing.T) {
		if n := countAs(t, app, ctxA, `select count(*) from firming_batch where idempotency_key like 'plan-run:%'`); n != 1 {
			t.Errorf("entity A sees %d auto-firm batches, want 1", n)
		}
		if n := countAs(t, app, ctxB, `select count(*) from firming_batch where idempotency_key like 'plan-run:%'`); n != 0 {
			t.Errorf("entity B sees %d auto-firm batches", n)
		}
	})

	t.Run("firming gates hold across entities", func(t *testing.T) {
		_, _, err := st.FirmPlannedOrders(ctxB, store.FirmRequest{IdempotencyKey: "firm-b", InputHash: "hb", Grouping: domain.GroupVendorDate, OrderIDs: ordersA})
		if problemCode(err) != store.CodeOrderNotFound {
			t.Errorf("entity B firming entity A's orders = %v, want %s", err, store.CodeOrderNotFound)
		}
		_, _, err = st.FirmPlannedOrders(ctxA, store.FirmRequest{IdempotencyKey: "firm-a2", InputHash: "ha2", Grouping: domain.GroupVendorDate, OrderIDs: ordersA})
		if problemCode(err) != store.CodeAlreadyFirmed {
			t.Errorf("entity A firming twice = %v, want %s", err, store.CodeAlreadyFirmed)
		}
		replay, replayed, err := st.FirmPlannedOrders(ctxA, store.FirmRequest{IdempotencyKey: "firm-a", InputHash: "ha", Grouping: domain.GroupVendorDate, OrderIDs: ordersA})
		if err != nil || !replayed || replay.ID != batchA.ID {
			t.Errorf("entity A replay = %+v (replayed %v), %v", replay, replayed, err)
		}
		_, _, err = st.FirmPlannedOrders(ctxB, store.FirmRequest{IdempotencyKey: "firm-a", InputHash: "ha", Grouping: domain.GroupVendorDate, OrderIDs: orderIDs(t, st, all, "A-PLAN")})
		if problemCode(err) != store.CodeOrderNotFound {
			t.Errorf("entity B replaying entity A's key on orders it cannot see = %v", err)
		}
	})

	t.Run("a batch cannot span entities and takes the orders' entity", func(t *testing.T) {
		planB := runRequest(productionResult(t), "run-b", "in-b")
		planB.PlanCode = "B-PLAN"
		if _, _, err := st.RunPlan(ctxB, planB); err != nil {
			t.Fatal(err)
		}
		planA2 := runRequest(productionResult(t), "run-a2", "in-a2")
		planA2.PlanCode = "A-PLAN-2"
		if _, _, err := st.RunPlan(ctxA, planA2); err != nil {
			t.Fatal(err)
		}
		mixed := append(orderIDs(t, st, all, "B-PLAN"), orderIDs(t, st, all, "A-PLAN-2")...)
		_, _, err := st.FirmPlannedOrders(all, store.FirmRequest{IdempotencyKey: "firm-mixed", InputHash: "hm", Grouping: domain.GroupVendorDate, OrderIDs: mixed})
		if problemCode(err) != store.CodeFirmEntity {
			t.Fatalf("mixed-entity batch = %v, want %s", err, store.CodeFirmEntity)
		}
		batchB, _, err := st.FirmPlannedOrders(all, store.FirmRequest{IdempotencyKey: "firm-b2", InputHash: "hb2", Grouping: domain.GroupVendorDate, OrderIDs: orderIDs(t, st, all, "B-PLAN")})
		if err != nil {
			t.Fatalf("all-entity firming of entity B's orders: %v", err)
		}
		q := `select count(*) from firming_batch where id = $1`
		if countAs(t, app, ctxB, q, batchB.ID) != 1 || countAs(t, app, ctxA, q, batchB.ID) != 0 {
			t.Error("the batch did not take entity B from its orders")
		}
	})

	t.Run("a firming key held by another entity's batch is refused, not a replay", func(t *testing.T) {
		planB := runRequest(productionResult(t), "run-b3", "in-b3")
		planB.PlanCode = "B-PLAN-3"
		if _, _, err := st.RunPlan(ctxB, planB); err != nil {
			t.Fatal(err)
		}
		_, _, err := st.FirmPlannedOrders(ctxB, store.FirmRequest{IdempotencyKey: "firm-a", InputHash: "ha", Grouping: domain.GroupVendorDate, OrderIDs: orderIDs(t, st, ctxB, "B-PLAN-3")})
		if problemCode(err) != store.CodeIdempotencyReuse {
			t.Errorf("entity B reusing entity A's key = %v, want %s", err, store.CodeIdempotencyReuse)
		}
	})

	t.Run("item coverage reads, writes and the run's coverage lookup follow the entity", func(t *testing.T) {
		if _, err := st.GetItemCoverage(ctxB, covA.ID); problemCode(err) != store.CodeItemCoverageNotFound {
			t.Errorf("entity B get = %v", err)
		}
		if list, err := st.ListItemCoverages(ctxB, "", 10); err != nil || len(list) != 0 {
			t.Errorf("entity B lists %d coverages (%v)", len(list), err)
		}
		if _, err := st.UpdateItemCoverage(ctxB, covA.ID, 1, covA.Spec); problemCode(err) != store.CodeItemCoverageNotFound {
			t.Errorf("entity B update = %v", err)
		}
		if got, err := st.RunCoverages(ctxB, site, []string{item}); err != nil || len(got) != 0 {
			t.Errorf("entity B run coverage = %v, %v", got, err)
		}
		if got, err := st.RunCoverages(ctxA, site, []string{item}); err != nil || len(got) != 1 {
			t.Errorf("entity A run coverage = %v, %v", got, err)
		}
		if got, err := st.RunCoverages(all, site, []string{item}); err != nil || len(got) != 1 {
			t.Errorf("all-entity run coverage = %v, %v", got, err)
		}
		if _, err := st.UpdateItemCoverage(ctxA, covA.ID, 1, covA.Spec); err != nil {
			t.Errorf("entity A update: %v", err)
		}
		clash := domain.DefaultItemCoverage(item, site, group.ID)
		if _, _, err := st.CreateItemCoverage(ctxB, store.ItemCoverageCreate{Spec: clash}); problemCode(err) != store.CodeItemCoverageExists {
			t.Errorf("entity B coverage for the same item and site = %v, want %s", err, store.CodeItemCoverageExists)
		}
	})

	t.Run("item coverage and buffer owners are derived or named", func(t *testing.T) {
		spec := domain.DefaultItemCoverage(ids.New(), site, group.ID)
		_, _, err := st.CreateItemCoverage(all, store.ItemCoverageCreate{Spec: spec})
		requireStatus(t, err, http.StatusUnprocessableEntity, "all-entity coverage without an entity")
		_, _, err = st.CreateItemCoverage(ctxAB, store.ItemCoverageCreate{Spec: spec})
		requireStatus(t, err, http.StatusUnprocessableEntity, "two-entity coverage without an entity")
		_, _, err = st.CreateItemCoverage(ctxA, store.ItemCoverageCreate{LegalEntityID: entityB, Spec: spec})
		requireStatus(t, err, http.StatusForbidden, "entity A coverage owned by entity B")
		created, _, err := st.CreateItemCoverage(all, store.ItemCoverageCreate{LegalEntityID: entityB, Spec: spec})
		if err != nil {
			t.Fatalf("all-entity coverage naming entity B: %v", err)
		}
		if _, err := st.GetItemCoverage(ctxB, created.ID); err != nil {
			t.Errorf("entity B cannot read the coverage created for it: %v", err)
		}
		if _, err := st.GetItemCoverage(ctxA, created.ID); problemCode(err) != store.CodeItemCoverageNotFound {
			t.Errorf("entity A reads entity B's coverage = %v", err)
		}

		other := br2211Buffer(t, ids.New(), ids.New(), "", profile.ID, 90)
		_, err = st.UpsertDdmrpBuffer(all, other)
		requireStatus(t, err, http.StatusUnprocessableEntity, "all-entity buffer without an entity")
		other.LegalEntityID = entityB
		if _, err := st.UpsertDdmrpBuffer(all, other); err != nil {
			t.Fatalf("all-entity buffer naming entity B: %v", err)
		}
		list, err := st.ListDdmrpBuffers(ctxB, store.BufferQuery{Limit: 10})
		if err != nil || len(list) != 1 {
			t.Errorf("entity B lists %d buffers (%v), want its own 1", len(list), err)
		}
	})

	t.Run("a buffer of another entity is never overwritten", func(t *testing.T) {
		before, err := st.ListDdmrpBuffers(ctxA, store.BufferQuery{SiteID: site, Limit: 10})
		if err != nil || len(before) != 1 {
			t.Fatalf("entity A buffers = %+v, %v", before, err)
		}
		_, err = st.UpsertDdmrpBuffer(ctxB, br2211Buffer(t, item, site, "", profile.ID, 300))
		if problemCode(err) != store.CodeBufferEntity {
			t.Errorf("entity B recalculating entity A's buffer = %v, want %s", err, store.CodeBufferEntity)
		}
		named := br2211Buffer(t, item, site, "", profile.ID, 300)
		named.LegalEntityID = entityB
		if _, err := st.UpsertDdmrpBuffer(all, named); problemCode(err) != store.CodeBufferEntity {
			t.Errorf("all-entity recalculation naming entity B over entity A's buffer = %v, want %s", err, store.CodeBufferEntity)
		}
		after, err := st.ListDdmrpBuffers(ctxA, store.BufferQuery{SiteID: site, Limit: 10})
		if err != nil || len(after) != 1 || after[0].Status.Zone != before[0].Status.Zone || after[0].CalculatedAt != before[0].CalculatedAt {
			t.Errorf("entity A buffer changed: %+v -> %+v (%v)", before, after, err)
		}
		if _, err := st.UpsertDdmrpBuffer(ctxA, br2211Buffer(t, item, site, "", profile.ID, 300)); err != nil {
			t.Errorf("entity A recalculation of its own buffer: %v", err)
		}
	})

	t.Run("legacy rows without an entity are visible only to the all-entity caller", func(t *testing.T) {
		if _, err := admin.Exec(context.Background(), `update item_coverage set legal_entity_id = null where id = $1`, covA.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := st.GetItemCoverage(ctxA, covA.ID); problemCode(err) != store.CodeItemCoverageNotFound {
			t.Errorf("entity A reads an unassigned coverage = %v", err)
		}
		if _, err := st.GetItemCoverage(all, covA.ID); err != nil {
			t.Errorf("all-entity caller cannot read an unassigned coverage: %v", err)
		}
	})
}
