package store_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func scenarioSpec(code string) domain.ScenarioSpec {
	return domain.ScenarioSpec{Code: code, PlanCode: "MAIN", Name: "Demand +20%", DemandFactor: decimal.RequireFromString("1.2"), Active: true,
		ItemFactors:       []domain.ItemFactor{{ItemID: ids.New(), Factor: decimal.RequireFromString("0.5")}},
		AddedDemand:       []domain.AddedDemand{{ItemID: ids.New(), Date: "2026-12-01", Qty: decimal.NewFromInt(7)}},
		LeadTimeOverrides: []domain.LeadTimeOverride{{ItemID: ids.New(), LeadTimeDays: 4}}}
}

// runPlanCode gives plan code an operational run, so scenarios can belong to it.
func runPlanCode(t *testing.T, st *store.Postgres, ctx context.Context, code string) {
	t.Helper()
	req := runRequest(oneItemPlan(t), "run-"+code, "in-"+code)
	req.PlanCode = code
	if _, _, err := st.RunPlan(ctx, req); err != nil {
		t.Fatalf("run %s: %v", code, err)
	}
}

// TestScenarios covers FR-22.15 setup on the database: create, idempotent
// replay, duplicate code, If-Match update, the limit of 10 active scenarios per
// plan, and tenant isolation under RLS.
func TestScenarios(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.simulate", "planning.read")
	spec := scenarioSpec("UP20")
	if _, _, err := st.CreateScenario(ctx, store.ScenarioCreate{Spec: spec}); problemCode(err) != store.CodePlanNotFound {
		t.Fatalf("scenario of a plan that never ran: %v; want %s", err, store.CodePlanNotFound)
	}
	runPlanCode(t, st, ctx, "MAIN")
	runPlanCode(t, st, ctx, "SECOND")
	sc, replayed, err := st.CreateScenario(ctx, store.ScenarioCreate{IdempotencyKey: "sc-1", InputHash: "h1", Spec: spec})
	if err != nil || replayed || sc.Version != 1 {
		t.Fatalf("create = %+v (replayed %v), %v", sc, replayed, err)
	}
	read, err := st.GetScenario(ctx, "UP20")
	if err != nil || !read.Spec.DemandFactor.Equal(spec.DemandFactor) || len(read.Spec.ItemFactors) != 1 || read.Spec.ItemFactors[0].ItemID != spec.ItemFactors[0].ItemID || !read.Spec.ItemFactors[0].Factor.Equal(spec.ItemFactors[0].Factor) ||
		read.Spec.AddedDemand[0].Date != "2026-12-01" || read.Spec.LeadTimeOverrides[0].LeadTimeDays != 4 {
		t.Fatalf("read back = %+v, %v", read, err)
	}
	again, replayed, err := st.CreateScenario(ctx, store.ScenarioCreate{IdempotencyKey: "sc-1", InputHash: "h1", Spec: spec})
	if err != nil || !replayed || again.ID != sc.ID {
		t.Fatalf("replay = %+v (replayed %v), %v", again, replayed, err)
	}
	if _, _, err := st.CreateScenario(ctx, store.ScenarioCreate{IdempotencyKey: "sc-1", InputHash: "h2", Spec: spec}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if _, _, err := st.CreateScenario(ctx, store.ScenarioCreate{Spec: spec}); problemCode(err) != store.CodeScenarioExists {
		t.Fatalf("duplicate code: %v", err)
	}

	changed := spec
	changed.Name, changed.Active, changed.ItemFactors = "Renamed", false, nil
	up, err := st.UpdateScenario(ctx, 1, changed)
	if err != nil || up.Version != 2 {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if _, err := st.UpdateScenario(ctx, 1, changed); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale update: %v", err)
	}
	moved := changed
	moved.PlanCode = "OTHER"
	if _, err := st.UpdateScenario(ctx, 2, moved); problemCode(err) != "PLAN-SCENARIO-PLAN" {
		t.Fatalf("plan change: %v", err)
	}
	missing := changed
	missing.Code = "NOPE"
	if _, err := st.UpdateScenario(ctx, 1, missing); problemCode(err) != store.CodeScenarioNotFound {
		t.Fatalf("unknown: %v", err)
	}
	if _, err := st.GetScenario(ctx, "NOPE"); problemCode(err) != store.CodeScenarioNotFound {
		t.Fatalf("get unknown: %v", err)
	}

	// Ten active scenarios per plan; the inactive UP20 does not count.
	for i := 0; i < domain.MaxActiveScenarios; i++ {
		if _, _, err := st.CreateScenario(ctx, store.ScenarioCreate{Spec: scenarioSpec(fmt.Sprintf("S%02d", i))}); err != nil {
			t.Fatalf("scenario %d: %v", i, err)
		}
	}
	if _, _, err := st.CreateScenario(ctx, store.ScenarioCreate{Spec: scenarioSpec("S99")}); problemCode(err) != store.CodeScenarioLimit {
		t.Fatalf("11th active: %v; want %s", err, store.CodeScenarioLimit)
	}
	inactive := scenarioSpec("S98")
	inactive.Active = false
	if _, _, err := st.CreateScenario(ctx, store.ScenarioCreate{Spec: inactive}); err != nil {
		t.Fatalf("inactive scenarios are not limited: %v", err)
	}
	reactivate := changed
	reactivate.Active = true
	if _, err := st.UpdateScenario(ctx, 2, reactivate); problemCode(err) != store.CodeScenarioLimit {
		t.Fatalf("reactivating past the limit: %v", err)
	}
	other := scenarioSpec("S99")
	other.PlanCode = "SECOND"
	if _, _, err := st.CreateScenario(ctx, store.ScenarioCreate{Spec: other}); err != nil {
		t.Fatalf("the limit is per plan: %v", err)
	}
	if list, err := st.ListScenarios(ctx, store.ScenarioQuery{PlanCode: "SECOND", Limit: 50}); err != nil || len(list) != 1 {
		t.Fatalf("list of SECOND = %+v, %v", list, err)
	}

	foreign := testkit.Ctx(testkit.TenantID(2), "planning.simulate", "planning.read")
	if list, err := st.ListScenarios(foreign, store.ScenarioQuery{Limit: 50}); err != nil || len(list) != 0 {
		t.Fatalf("other tenant sees %+v, %v", list, err)
	}
	if _, err := st.GetScenario(foreign, "UP20"); problemCode(err) != store.CodeScenarioNotFound {
		t.Fatalf("other tenant reads UP20: %v", err)
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from planning_scenario`).Scan(&n); err != nil || n != 13 {
		t.Fatalf("rows = %d, %v; want 13", n, err)
	}
}

func simulationRequest(res domain.Result, scenario, key, input string) store.SimulationRequest {
	return store.SimulationRequest{ScenarioCode: scenario, SiteID: ids.New(), AsOf: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		ItemsPlanned: 1, DurationMs: 1, InputHash: input, IdempotencyKey: key, Result: res}
}

// TestRunSimulation_IsolatedFromTheOperationalPlan covers BR-22.14 and AC-22.11
// on the database: a simulation is its own non-current version, leaves the
// operational version, its orders and its events alone, publishes only
// scenario.simulated.v1, and its planned orders cannot be firmed or changed.
func TestRunSimulation_IsolatedFromTheOperationalPlan(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read", "planning.simulate")
	if _, _, err := st.RunSimulation(ctx, simulationRequest(oneItemPlan(t), "UP20", "s-0", "i0")); problemCode(err) != store.CodeScenarioNotFound {
		t.Fatalf("unknown scenario: %v", err)
	}
	op, _, err := st.RunPlan(ctx, runRequest(oneItemPlan(t), "run-1", "in-1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateScenario(ctx, store.ScenarioCreate{Spec: scenarioSpec("UP20")}); err != nil {
		t.Fatal(err)
	}
	var outboxBefore int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox`).Scan(&outboxBefore); err != nil {
		t.Fatal(err)
	}

	sim, replayed, err := st.RunSimulation(ctx, simulationRequest(oneItemPlan(t), "UP20", "s-1", "i1"))
	if err != nil || replayed {
		t.Fatalf("simulation: %+v (replayed %v), %v", sim, replayed, err)
	}
	if sim.Kind != "simulation" || sim.ScenarioCode != "UP20" || sim.Status != "complete" || sim.Number != op.Number+1 || sim.PlanCode != "MAIN" {
		t.Fatalf("simulation version = %+v", sim)
	}
	again, replayed, err := st.RunSimulation(ctx, simulationRequest(oneItemPlan(t), "UP20", "s-1", "i1"))
	if err != nil || !replayed || again.ID != sim.ID || again.Kind != "simulation" {
		t.Fatalf("replay = %+v (replayed %v), %v", again, replayed, err)
	}
	if _, _, err := st.RunSimulation(ctx, simulationRequest(oneItemPlan(t), "UP20", "s-1", "other")); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}

	// The operational version is still the only current one, with its own orders.
	cur, err := st.GetVersion(ctx, "MAIN", op.Number)
	if err != nil || cur.Status != "current" || cur.Kind != "static" {
		t.Fatalf("operational version = %+v, %v", cur, err)
	}
	opOrders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(opOrders) != 1 || opOrders[0].VersionID != op.ID {
		t.Fatalf("current orders = %+v, %v", opOrders, err)
	}
	simOrders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{Version: sim.Number}, 10)
	if err != nil || len(simOrders) != 1 || simOrders[0].VersionID != sim.ID {
		t.Fatalf("simulation orders = %+v, %v", simOrders, err)
	}

	// The version diff compares the simulation with the operational version
	// (FR-22.15 notes); oneItemPlan plans a fresh item each time.
	diff, err := st.DiffVersions(ctx, "MAIN", op.Number, sim.Number, 50)
	if err != nil || diff.From.Kind != "static" || diff.To.Kind != "simulation" || diff.To.ScenarioCode != "UP20" || diff.AddedCount != 1 || diff.RemovedCount != 1 {
		t.Fatalf("diff operational vs simulation = %+v, %v", diff, err)
	}

	// Only scenario.simulated.v1 reached the outbox.
	rows, err := admin.Query(context.Background(), `select type from outbox order by id offset $1`, outboxBefore)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	for rows.Next() {
		var typ string
		if err := rows.Scan(&typ); err != nil {
			t.Fatal(err)
		}
		types = append(types, typ)
	}
	if len(types) != 1 || types[0] != "dev.passage.planning.scenario.simulated.v1" {
		t.Fatalf("outbox types = %v; want only scenario.simulated.v1", types)
	}

	// BR-22.14: the planned orders cannot be firmed or changed.
	firm := store.FirmRequest{IdempotencyKey: "f-1", InputHash: "h", Grouping: domain.GroupVendorDate, OrderIDs: []string{simOrders[0].ID}}
	if _, _, err := st.FirmPlannedOrders(ctx, firm); problemCode(err) != store.CodeSimulationNotFirmable {
		t.Fatalf("firm a simulated order: %v; want %s", err, store.CodeSimulationNotFirmable)
	}
	qty := decimal.NewFromInt(5)
	if _, err := st.UpdatePlannedOrder(ctx, simOrders[0].ID, simOrders[0].Version, store.OrderPatch{Qty: &qty}); problemCode(err) != store.CodeSimulationNotFirmable {
		t.Fatalf("change a simulated order: %v", err)
	}
	var firmed int
	if err := admin.QueryRow(context.Background(), `select count(*) from planned_order where status = 'firmed'`).Scan(&firmed); err != nil || firmed != 0 {
		t.Fatalf("firmed orders = %d, %v", firmed, err)
	}

	// An inactive scenario cannot run; another tenant cannot see the simulation.
	off := scenarioSpec("OFF")
	off.Active = false
	if _, _, err := st.CreateScenario(ctx, store.ScenarioCreate{Spec: off}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RunSimulation(ctx, simulationRequest(oneItemPlan(t), "OFF", "s-2", "i2")); problemCode(err) != store.CodeScenarioInactive {
		t.Fatalf("inactive: %v", err)
	}
	foreign := testkit.Ctx(testkit.TenantID(2), "planning.read")
	if o, err := st.ListPlannedOrders(foreign, "MAIN", store.OrderFilter{Version: sim.Number}, 10); err != nil || len(o) != 0 {
		t.Fatalf("other tenant sees %+v, %v", o, err)
	}
	// A database check keeps a simulation out of the current slot.
	if _, err := admin.Exec(context.Background(), `update plan_version set status = 'current' where id = $1`, sim.ID); err == nil {
		t.Fatal("a simulation version was made current")
	}
}

// TestScenarios_LegalEntityScope: a scenario belongs to the legal entity of
// its plan. Another entity can neither create one for that plan, read, change
// or run it, nor read the simulation version it wrote.
func TestScenarios_LegalEntityScope(t *testing.T) {
	st, admin, _ := setup(t)
	entityA, entityB := ids.New(), ids.New()
	ctxA, ctxB := scopedCtx(testkit.TenantID(1), entityA), scopedCtx(testkit.TenantID(1), entityB)
	all := scopedCtx(testkit.TenantID(1), "")
	if _, _, err := st.RunPlan(ctxA, runRequest(oneItemPlan(t), "run-a", "in-a")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.CreateScenario(ctxB, store.ScenarioCreate{Spec: scenarioSpec("FROMB")}); problemCode(err) != store.CodePlanNotFound {
		t.Fatalf("entity B adds a scenario to entity A's plan: %v; want %s", err, store.CodePlanNotFound)
	}
	sc, _, err := st.CreateScenario(ctxA, store.ScenarioCreate{Spec: scenarioSpec("UP20")})
	if err != nil {
		t.Fatal(err)
	}
	var owner string
	if err := admin.QueryRow(context.Background(), `select coalesce(legal_entity_id::text, '') from planning_scenario where id = $1`, sc.ID).Scan(&owner); err != nil || owner != entityA {
		t.Fatalf("scenario owner = %q, %v; want the plan's entity %s", owner, err, entityA)
	}
	sim, _, err := st.RunSimulation(ctxA, simulationRequest(oneItemPlan(t), "UP20", "s-a", "i-a"))
	if err != nil {
		t.Fatal(err)
	}

	if list, err := st.ListScenarios(ctxB, store.ScenarioQuery{Limit: 50}); err != nil || len(list) != 0 {
		t.Fatalf("entity B lists %+v, %v", list, err)
	}
	if _, err := st.GetScenario(ctxB, "UP20"); problemCode(err) != store.CodeScenarioNotFound {
		t.Fatalf("entity B reads UP20: %v", err)
	}
	changed := scenarioSpec("UP20")
	changed.Active = false
	if _, err := st.UpdateScenario(ctxB, 1, changed); problemCode(err) != store.CodeScenarioNotFound {
		t.Fatalf("entity B changes UP20: %v", err)
	}
	if _, _, err := st.RunSimulation(ctxB, simulationRequest(oneItemPlan(t), "UP20", "s-b", "i-b")); problemCode(err) != store.CodeScenarioNotFound {
		t.Fatalf("entity B runs UP20: %v", err)
	}
	if o, err := st.ListPlannedOrders(ctxB, "MAIN", store.OrderFilter{Version: sim.Number}, 10); err != nil || len(o) != 0 {
		t.Fatalf("entity B reads the simulation's orders: %+v, %v", o, err)
	}
	if list, err := st.ListScenarios(all, store.ScenarioQuery{Limit: 50}); err != nil || len(list) != 1 {
		t.Fatalf("all-entity caller lists %+v, %v", list, err)
	}
	if got, err := st.GetScenario(ctxA, "UP20"); err != nil || got.Version != 1 || !got.Spec.Active {
		t.Fatalf("UP20 after entity B's attempts = %+v, %v", got, err)
	}
}
