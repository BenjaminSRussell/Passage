package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// TestStrategySetup covers create, idempotent replay, the duplicate refusal,
// update under If-Match and tenant isolation of the strategy setup (FR-22.19).
func TestStrategySetup(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.setup.maintain", "planning.read")
	spec := domain.DefaultStrategy(domain.StrategyATOFinal)
	spec.ConsumptionMode, spec.BackwardDays, spec.ForwardDays = domain.ConsumptionBackwardForward, 14, 7

	created, replayed, err := st.CreateStrategy(ctx, store.StrategyCreate{IdempotencyKey: "k1", InputHash: "h1", Spec: spec})
	if err != nil || replayed || created.Version != 1 || created.Spec != spec {
		t.Fatalf("create = %+v, %v, %v", created, replayed, err)
	}
	again, replayed, err := st.CreateStrategy(ctx, store.StrategyCreate{IdempotencyKey: "k1", InputHash: "h1", Spec: spec})
	if err != nil || !replayed || again.ID != created.ID {
		t.Fatalf("replay = %+v, %v, %v", again, replayed, err)
	}
	if _, _, err := st.CreateStrategy(ctx, store.StrategyCreate{IdempotencyKey: "k1", InputHash: "other", Spec: spec}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if _, _, err := st.CreateStrategy(ctx, store.StrategyCreate{Spec: spec}); problemCode(err) != store.CodeStrategyExists {
		t.Fatalf("duplicate: %v", err)
	}

	spec.BackwardDays = 45
	updated, err := st.UpdateStrategy(ctx, 1, spec)
	if err != nil || updated.Version != 2 || updated.Spec.BackwardDays != 45 {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if _, err := st.UpdateStrategy(ctx, 1, spec); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale update: %v", err)
	}
	if _, err := st.UpdateStrategy(ctx, 1, domain.DefaultStrategy(domain.StrategyMTO)); problemCode(err) != store.CodeStrategyNotFound {
		t.Fatalf("update without setup: %v", err)
	}
	list, err := st.ListStrategies(ctx)
	if err != nil || len(list) != 1 || list[0].Version != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if _, err := st.GetStrategy(ctx, domain.StrategyMTO); problemCode(err) != store.CodeStrategyNotFound {
		t.Fatalf("get without setup: %v", err)
	}

	other := testkit.Ctx(testkit.TenantID(2), "planning.setup.maintain", "planning.read")
	if l, err := st.ListStrategies(other); err != nil || len(l) != 0 {
		t.Fatalf("other tenant lists %+v, %v", l, err)
	}
	if _, err := st.UpdateStrategy(other, 2, spec); problemCode(err) != store.CodeStrategyNotFound {
		t.Fatalf("other tenant updates: %v", err)
	}
	if _, _, err := st.CreateStrategy(other, store.StrategyCreate{Spec: spec}); err != nil {
		t.Fatalf("other tenant has its own setup: %v", err)
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from planning_strategy`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("rows = %d, %v; want 2", n, err)
	}
	// The database refuses a code outside the six strategies.
	bad := spec
	bad.Code = "nope"
	if _, _, err := st.CreateStrategy(ctx, store.StrategyCreate{Spec: bad}); err == nil {
		t.Fatal("unknown strategy code stored")
	}
}

// TestRunStrategies: a run reads the strategy of each item's site-scope
// coverage with its stored setup, or the defaults when none is stored; other
// tenants, sites, scopes and legal entities do not leak in.
func TestRunStrategies(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.setup.maintain", "planning.read", "planning.run")
	group, _, err := st.CreateCoverageGroup(ctx, store.CoverageCreate{Spec: domain.DefaultCoverageGroup("REQ", domain.CoverageRequirement)})
	if err != nil {
		t.Fatal(err)
	}
	ato := domain.DefaultStrategy(domain.StrategyATOFinal)
	ato.BackwardDays, ato.ForwardDays = 10, 3
	ato.ConsumptionMode = domain.ConsumptionBackwardForward
	if _, _, err := st.CreateStrategy(ctx, store.StrategyCreate{Spec: ato}); err != nil {
		t.Fatal(err)
	}
	site := ids.New()
	atoItem, mtoItem, netItem, uncovered := ids.New(), ids.New(), ids.New(), ids.New()
	for item, strategy := range map[string]string{atoItem: domain.StrategyATOFinal, mtoItem: domain.StrategyMTO, netItem: domain.StrategyMTSNet} {
		cov := domain.DefaultItemCoverage(item, site, group.ID)
		cov.PlanningStrategy = strategy
		if _, _, err := st.CreateItemCoverage(ctx, store.ItemCoverageCreate{Spec: cov}); err != nil {
			t.Fatal(err)
		}
	}
	all := []string{atoItem, mtoItem, netItem, uncovered}
	got, err := st.RunStrategies(ctx, site, "", all)
	if err != nil || len(got) != 3 {
		t.Fatalf("RunStrategies = %+v, %v; want the three covered items", got, err)
	}
	if got[atoItem] != ato {
		t.Fatalf("ato item strategy %+v; want the stored setup %+v", got[atoItem], ato)
	}
	if m := got[mtoItem]; m != domain.DefaultStrategy(domain.StrategyMTO) || !m.UsesMarking {
		t.Fatalf("mto item without setup: %+v; want the defaults", m)
	}
	if got[netItem].Code != domain.StrategyMTSNet {
		t.Fatalf("net item: %+v", got[netItem])
	}
	if _, ok := got[uncovered]; ok {
		t.Fatal("an item without coverage was returned")
	}
	if g, err := st.RunStrategies(ctx, ids.New(), "", all); err != nil || len(g) != 0 {
		t.Fatalf("other site: %+v, %v", g, err)
	}
	if g, err := st.RunStrategies(testkit.Ctx(testkit.TenantID(2), "planning.run"), site, "", all); err != nil || len(g) != 0 {
		t.Fatalf("other tenant: %+v, %v", g, err)
	}
	if g, err := st.RunStrategies(ctx, site, ids.New(), all); err != nil || len(g) != 0 {
		t.Fatalf("other legal entity: %+v, %v", g, err)
	}
	if g, err := st.RunStrategies(ctx, site, "", nil); err != nil || len(g) != 0 {
		t.Fatalf("no items: %+v, %v", g, err)
	}
}
