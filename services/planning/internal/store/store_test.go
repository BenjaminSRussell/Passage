package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
	"github.com/BenjaminSRussell/passage/services/planning/migrations"
)

// setup migrates with the admin pool and returns a store over the application
// role, so row-level security applies to every query. Skipped without
// PASSAGE_TEST_DATABASE_URL.
func setup(t *testing.T) (*store.Postgres, *pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(app), admin, app
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

// oneItemPlan is a purchased item with one sales line of 40 on day 10.
func oneItemPlan(t *testing.T) domain.Result {
	t.Helper()
	res, err := domain.Run(0, []domain.Item{{
		ID:           ids.New(),
		Coverage:     domain.CoverageRequirement,
		OrderType:    domain.OrderPurchase,
		LeadTimeDays: 2,
		Demand: []domain.Requirement{{
			Ref: "SO-1", RefType: domain.RefSales, Date: 10,
			Qty: decimal.NewFromInt(40), Priority: domain.PrioritySales,
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Orders) != 1 {
		t.Fatalf("want 1 planned order, got %d", len(res.Orders))
	}
	return res
}

func runRequest(res domain.Result, key, input string) store.RunRequest {
	return store.RunRequest{
		PlanCode:       "MAIN",
		SiteID:         ids.New(),
		AsOf:           time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		ItemsPlanned:   1,
		DurationMs:     1,
		InputHash:      input,
		IdempotencyKey: key,
		Result:         res,
	}
}

// TestRunPlan_PersistsVersionAndPegging runs a one-item plan through the engine,
// persists it, and checks replay, version numbering, the outbox event and
// explain-tree completeness.
func TestRunPlan_PersistsVersionAndPegging(t *testing.T) {
	st, admin, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	res := oneItemPlan(t)
	req := runRequest(res, "run-1", "input-1")

	v, replayed, err := st.RunPlan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if replayed || v.Number != 1 || v.Status != "current" || v.PlanHash != res.Hash {
		t.Fatalf("first run: replayed=%v version=%+v", replayed, v)
	}

	again, replayed, err := st.RunPlan(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed || again.ID != v.ID {
		t.Fatalf("same key must replay the stored version: replayed=%v id=%s want %s", replayed, again.ID, v.ID)
	}

	reuse := runRequest(res, "run-1", "input-other")
	if _, _, err := st.RunPlan(ctx, reuse); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("same key, other input: %v; want %s", err, store.CodeIdempotencyReuse)
	}

	v2, _, err := st.RunPlan(ctx, runRequest(res, "run-2", "input-1"))
	if err != nil {
		t.Fatal(err)
	}
	if v2.Number != 2 {
		t.Fatalf("second version number = %d", v2.Number)
	}
	old, err := st.GetVersion(ctx, "MAIN", 1)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != "complete" {
		t.Fatalf("superseded version status = %s, want complete", old.Status)
	}
	if _, err := st.GetVersion(ctx, "MAIN", 9); problemCode(err) != store.CodeVersionNotFound {
		t.Fatalf("missing version: %v", err)
	}
	history, err := st.ListVersions(ctx, "MAIN", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || history[0].Number != 2 || history[0].Status != "current" || history[1].Number != 1 {
		t.Fatalf("history = %+v; want version 2 (current) then 1", history)
	}

	orders, err := st.ListPlannedOrders(ctx, "MAIN", store.OrderFilter{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].VersionID != v2.ID || orders[0].DeliveryDate != "2026-11-12" || orders[0].Qty != "40.000000" {
		t.Fatalf("current version orders = %+v", orders)
	}
	got, err := st.GetPlannedOrder(ctx, orders[0].ID)
	if err != nil || got.Number != orders[0].Number {
		t.Fatalf("get planned order: %+v, %v", got, err)
	}
	if _, err := st.GetPlannedOrder(ctx, ids.New()); problemCode(err) != store.CodeOrderNotFound {
		t.Fatalf("unknown planned order: %v", err)
	}

	pegs, err := st.PegsOf(ctx, v2.ID, res.Orders[0].Number)
	if err != nil {
		t.Fatal(err)
	}
	sum := decimal.Zero
	for _, p := range pegs {
		sum = sum.Add(p.Qty)
	}
	if !sum.Equal(res.Orders[0].Qty) {
		t.Fatalf("pegged %s, planned %s", sum, res.Orders[0].Qty)
	}

	// Each new version writes plan.started then plan.completed in its
	// transaction (event ids are UUIDv7, so id order is write order). The run
	// raises no message, so there is no message.raised.
	rows, err := admin.Query(context.Background(), `select type, payload from outbox order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var types []string
	var started []planevents.PlanStartedData
	var events []planevents.PlanCompletedData
	for rows.Next() {
		var typ string
		var raw []byte
		if err := rows.Scan(&typ, &raw); err != nil {
			t.Fatal(err)
		}
		types = append(types, typ)
		switch typ {
		case planevents.PlanStarted:
			var envelope struct {
				Data planevents.PlanStartedData `json:"data"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			started = append(started, envelope.Data)
		case planevents.PlanCompleted:
			var envelope struct {
				Data planevents.PlanCompletedData `json:"data"`
			}
			if err := json.Unmarshal(raw, &envelope); err != nil {
				t.Fatal(err)
			}
			events = append(events, envelope.Data)
		}
	}
	if !slices.Equal(types, []string{planevents.PlanStarted, planevents.PlanCompleted, planevents.PlanStarted, planevents.PlanCompleted}) {
		t.Fatalf("outbox types = %v; want started, completed per version", types)
	}
	if len(events) != 2 || events[1].VersionID != v2.ID || events[1].PlanID == "" || events[1].PlannedOrders.Purchase != 1 || events[1].PlanHash != res.Hash {
		t.Fatalf("outbox events = %+v; want two plan.completed, the last for version 2", events)
	}
	if s := started[1]; s.VersionID != v2.ID || s.VersionNumber != 2 || s.Kind != "static" || s.AsOf != "2026-11-02" || s.PlanID != events[1].PlanID || s.ItemsPlanned != 1 {
		t.Fatalf("plan.started of version 2 = %+v", s)
	}
}

// TestRunPlan_TenantIsolation: row-level security hides one tenant's plans
// from another, and each tenant numbers its own versions of plan MAIN.
func TestRunPlan_TenantIsolation(t *testing.T) {
	st, _, _ := setup(t)
	one := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	two := testkit.Ctx(testkit.TenantID(2), "planning.run", "planning.read")
	res := oneItemPlan(t)
	if _, _, err := st.RunPlan(one, runRequest(res, "k", "in")); err != nil {
		t.Fatal(err)
	}
	orders, err := st.ListPlannedOrders(one, "MAIN", store.OrderFilter{}, 10)
	if err != nil || len(orders) != 1 {
		t.Fatalf("tenant 1 orders: %+v, %v", orders, err)
	}

	if _, err := st.GetVersion(two, "MAIN", 1); problemCode(err) != store.CodeVersionNotFound {
		t.Fatalf("tenant 2 reads tenant 1's version: %v", err)
	}
	if got, err := st.ListPlannedOrders(two, "MAIN", store.OrderFilter{}, 10); err != nil || len(got) != 0 {
		t.Fatalf("tenant 2 lists tenant 1's orders: %+v, %v", got, err)
	}
	if _, err := st.GetPlannedOrder(two, orders[0].ID); problemCode(err) != store.CodeOrderNotFound {
		t.Fatalf("tenant 2 reads tenant 1's order: %v", err)
	}
	// The same Idempotency-Key in another tenant is a new run, not a replay.
	v, replayed, err := st.RunPlan(two, runRequest(res, "k", "in"))
	if err != nil || replayed || v.Number != 1 {
		t.Fatalf("tenant 2 first run: %+v replayed=%v %v", v, replayed, err)
	}
}

// TestRunPlan_RefusesConcurrentRun: while another transaction holds the plan
// (a run in progress), a second run fails fast with 409 PLAN-0001 (AC-22.9).
func TestRunPlan_RefusesConcurrentRun(t *testing.T) {
	st, _, app := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	res := oneItemPlan(t)
	if _, _, err := st.RunPlan(ctx, runRequest(res, "k1", "in")); err != nil {
		t.Fatal(err)
	}

	tx, err := app.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true), set_config('passage.legal_entity_scopes', '*', true)`, testkit.TenantID(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `select id from plan where code = 'MAIN' for update`); err != nil {
		t.Fatal(err)
	}

	_, _, err = st.RunPlan(ctx, runRequest(res, "k2", "in"))
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != store.CodeAlreadyRunning || p.Status != http.StatusConflict {
		t.Fatalf("concurrent run: %v; want 409 %s", err, store.CodeAlreadyRunning)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _, err := st.RunPlan(ctx, runRequest(res, "k2", "in")); err != nil || v.Number != 2 {
		t.Fatalf("run after the lock is released: %+v, %v", v, err)
	}
}
