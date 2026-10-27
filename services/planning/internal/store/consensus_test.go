package store_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	planevents "github.com/BenjaminSRussell/passage/services/planning/internal/events"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// asUser is a tenant context for one named user.
func asUser(tenantN int, user string) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(tenantN), UserID: user, Duties: map[string]struct{}{}})
}

// TestForecastConsensus covers FR-22.9 on the database: submit locks the
// cells, the submitter and cell writers cannot decide, reject reopens the
// model, publish marks the cells consensus and writes forecast.published.v1
// in the same transaction, and another tenant sees none of it.
func TestForecastConsensus(t *testing.T) {
	st, admin, _ := setup(t)
	alice, carol, bob := asUser(1, "alice"), asUser(1, "carol"), asUser(1, "bob")
	itemA, itemB, site := ids.New(), ids.New(), ids.New()

	if _, err := st.SubmitForecastConsensus(alice, "BASE", 1); problemCode(err) != store.CodeForecastNotFound {
		t.Fatalf("submit of an unknown model: %v", err)
	}
	if _, err := st.UpsertForecastLines(alice, "BASE", 0, nil, []store.ForecastCellInput{
		cellIn(itemA, site, "2026-11-02", "2026-11-08", "100", "30"),
		cellIn(itemA, site, "2026-11-09", "2026-11-15", "50", "0"),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertForecastLines(carol, "BASE", 1, nil, []store.ForecastCellInput{
		cellIn(itemB, site, "2026-11-02", "2026-11-08", "40", "0"),
	}); err != nil {
		t.Fatal(err)
	}

	m, err := st.SubmitForecastConsensus(alice, "BASE", 2)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != domain.ConsensusSubmitted || m.Version != 3 || m.SubmittedBy == nil || *m.SubmittedBy != "alice" {
		t.Fatalf("submitted = %+v", m)
	}
	if _, err := st.UpsertForecastLines(alice, "BASE", 3, nil, []store.ForecastCellInput{cellIn(itemA, site, "2026-11-02", "2026-11-08", "1", "0")}); problemCode(err) != store.CodeForecastLocked {
		t.Fatalf("write on a submitted model: %v", err)
	}
	if _, err := st.SubmitForecastConsensus(alice, "BASE", 3); problemCode(err) != store.CodeConsensusState {
		t.Fatalf("second submit: %v", err)
	}
	for name, ctx := range map[string]context.Context{"submitter": alice, "cell writer": carol} {
		if _, _, err := st.DecideForecastConsensus(ctx, "BASE", 3, true); problemCode(err) != store.CodeConsensusSoD {
			t.Fatalf("%s publishes: %v", name, err)
		}
	}
	if _, _, err := st.DecideForecastConsensus(bob, "BASE", 2, true); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale publish: %v", err)
	}

	// Bob rejects; the model is a draft again and alice may change a cell.
	m, pub, err := st.DecideForecastConsensus(bob, "BASE", 3, false)
	if err != nil || pub != nil || m.Status != domain.ConsensusDraft || m.SubmittedBy != nil || m.DecidedBy == nil || *m.DecidedBy != "bob" {
		t.Fatalf("reject = %+v, %+v, %v", m, pub, err)
	}
	if m, err = st.UpsertForecastLines(alice, "BASE", 4, nil, []store.ForecastCellInput{cellIn(itemA, site, "2026-11-09", "2026-11-15", "60", "0")}); err != nil {
		t.Fatal(err)
	}
	if m, err = st.SubmitForecastConsensus(alice, "BASE", m.Version); err != nil {
		t.Fatal(err)
	}

	m, pub, err = st.DecideForecastConsensus(bob, "BASE", m.Version, true)
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != domain.ConsensusPublished || m.PublishedAt == nil || pub == nil {
		t.Fatalf("published = %+v, %+v", m, pub)
	}
	want := store.ForecastPublication{Items: 2, HorizonPeriods: 2, PeriodFrom: "2026-11-02", PeriodTo: "2026-11-15"}
	if *pub != want {
		t.Fatalf("publication = %+v; want %+v", *pub, want)
	}
	_, lines, err := st.ListForecastLines(bob, "BASE", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if l.Source != "consensus" {
			t.Fatalf("published cell %+v; want source consensus", l)
		}
	}
	if _, err := st.UpsertForecastLines(alice, "BASE", m.Version, nil, []store.ForecastCellInput{cellIn(itemA, site, "2026-11-02", "2026-11-08", "1", "0")}); problemCode(err) != store.CodeForecastLocked {
		t.Fatalf("write on a published model: %v", err)
	}
	if _, _, err := st.DecideForecastConsensus(bob, "BASE", m.Version, false); problemCode(err) != store.CodeConsensusState {
		t.Fatalf("reject of a published model: %v", err)
	}
	// A published consensus is still the forecast a run nets.
	if demand, err := st.ForecastDemand(alice, "BASE", site, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)); err != nil || len(demand) != 3 {
		t.Fatalf("forecast demand after publish = %+v, %v", demand, err)
	}

	var raw []byte
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*), max(payload::text) from outbox where type = $1`, planevents.ForecastPublished).Scan(&n, &raw); err != nil {
		t.Fatal(err)
	}
	var evt struct {
		Data planevents.ForecastPublishedData `json:"data"`
	}
	if err := json.Unmarshal(raw, &evt); err != nil {
		t.Fatal(err)
	}
	if n != 1 || evt.Data.ModelCode != "BASE" || evt.Data.Version != m.Version || evt.Data.Items != 2 || evt.Data.HorizonPeriods != 2 ||
		evt.Data.SubmittedBy != "alice" || evt.Data.ApprovedBy != "bob" {
		t.Fatalf("outbox has %d forecast.published events, last %+v", n, evt.Data)
	}

	if _, err := st.SubmitForecastConsensus(asUser(2, "alice"), "BASE", 1); problemCode(err) != store.CodeForecastNotFound {
		t.Fatalf("tenant 2 submits tenant 1's model: %v", err)
	}
}

// A model without cells cannot be submitted, and a submit needs a user. The
// API never writes an empty model, so the row is inserted directly.
func TestForecastConsensus_EmptyModelIsRefused(t *testing.T) {
	st, admin, _ := setup(t)
	if _, err := admin.Exec(context.Background(), `insert into forecast_model (tenant_id, id, code, version) values ($1, $2, 'EMPTY', 1)`,
		testkit.TenantID(1), ids.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SubmitForecastConsensus(asUser(1, "alice"), "EMPTY", 1); problemCode(err) != store.CodeConsensusEmpty {
		t.Fatalf("submit of an empty model: %v", err)
	}
	if _, err := st.SubmitForecastConsensus(asUser(1, ""), "EMPTY", 1); problemCode(err) != store.CodeConsensusSoD {
		t.Fatalf("anonymous submit: %v", err)
	}
}

// TestRunPlan_StoresIncrease: the action check of migration 0005 admits the
// Increase message and it reads back by its action filter.
func TestRunPlan_StoresIncrease(t *testing.T) {
	st, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(1), "planning.run", "planning.read")
	res, err := domain.Run(0, []domain.Item{{
		ID: ids.New(), Coverage: domain.CoverageRequirement, OrderType: domain.OrderPurchase, FreezeDays: 10,
		Demand:   []domain.Requirement{{Ref: "SO-1", RefType: domain.RefSales, Date: 3, Qty: decimal.NewFromInt(60), Priority: domain.PrioritySales}},
		Receipts: []domain.Requirement{{Ref: "PO-1", RefType: "purchase-line", Date: 1, Qty: decimal.NewFromInt(50)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.RunPlan(ctx, runRequest(res, "run-1", "in-1")); err != nil {
		t.Fatal(err)
	}
	msgs, err := st.ListMessages(ctx, "MAIN", domain.ActionIncrease, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].SupplyRef != "PO-1" || msgs[0].Qty != "10.000000" || msgs[0].DemandRef != "SO-1" {
		t.Fatalf("increase messages = %+v; want PO-1 by 10 for SO-1", msgs)
	}
}
