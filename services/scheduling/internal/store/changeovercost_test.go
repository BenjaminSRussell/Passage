package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func wantBarCost(t *testing.T, b store.Bar, cost, currency string, cleaning *string) {
	t.Helper()
	if b.ChangeoverCost == nil || !b.ChangeoverCost.Equal(decimal.RequireFromString(cost)) || b.ChangeoverCurrency == nil || *b.ChangeoverCurrency != currency {
		t.Fatalf("bar %s cost = %v %v, want %s %s", b.JobID, b.ChangeoverCost, b.ChangeoverCurrency, cost, currency)
	}
	if (cleaning == nil) != (b.CleaningSetID == nil) || (cleaning != nil && *cleaning != *b.CleaningSetID) {
		t.Fatalf("bar %s cleaning set = %v, want %v", b.JobID, b.CleaningSetID, cleaning)
	}
}

// FR-23.15 through the database: each scheduled changeover stores the cost
// (in the matrix currency) and cleaning set of the matrix entry that priced
// it; the Gantt and job.scheduled carry them, and a job that keeps the setup
// group or uses the route setup (SCHED-0012) carries none.
func TestRunSchedule_StoresChangeoverCostAndCleaningSet(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	if _, _, err := s.CreateSetupMatrix(ctx, store.SetupMatrix{ResourceID: r.ID, ActiveFrom: "2027-01-01", Currency: "EUR",
		Entries: []domain.SetupEntry{
			{FromGroup: "A", ToGroup: "B", Minutes: 30, Cost: decimal.RequireFromString("45.50"), CleaningSetID: "WET-CLEAN"},
			{FromGroup: "B", ToGroup: "C", Minutes: 20, Cost: decimal.RequireFromString("30.125")},
		}}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	a := groupOperation(t, ctx, s, "A", 60, r.ID, 9)
	b := groupOperation(t, ctx, s, "B", 60, r.ID, 10)
	c := groupOperation(t, ctx, s, "C", 60, r.ID, 11)
	d := groupOperation(t, ctx, s, "D", 60, r.ID, 12)
	run, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Warnings) != 1 || run.Warnings[0].OperationID != d.ID {
		t.Fatalf("warnings = %+v, want one SCHED-0012 for D", run.Warnings)
	}
	bars := barsOf(t, ctx, s)
	if first := bars[a.ID]; first.ChangeoverCost != nil || first.ChangeoverCurrency != nil || first.CleaningSetID != nil {
		t.Fatalf("first job = %+v, want no changeover cost from an unknown group", first)
	}
	wantBarCost(t, bars[b.ID], "45.50", "EUR", strp("WET-CLEAN"))
	wantBarCost(t, bars[c.ID], "30.125", "EUR", nil)
	if fallback := bars[d.ID]; fallback.ChangeoverMinutes != 15 || fallback.ChangeoverCost != nil || fallback.ChangeoverCurrency != nil {
		t.Fatalf("route setup job = %+v, want 15 min with no cost", fallback)
	}
	evt := lastJobEvent(t, admin, b.ID)
	if evt.ChangeoverCost != "45.5" || evt.ChangeoverCurrency != "EUR" || evt.CleaningSetID != "WET-CLEAN" {
		t.Fatalf("job.scheduled = %+v, want cost 45.5 EUR and cleaning set WET-CLEAN", evt)
	}
	if evt := lastJobEvent(t, admin, d.ID); evt.ChangeoverCost != "" || evt.ChangeoverCurrency != "" || evt.CleaningSetID != "" {
		t.Fatalf("job.scheduled for D = %+v, want no cost or cleaning set", evt)
	}

	// Firming re-announces the job from the stored row, cost included.
	if _, _, err := s.FirmJob(ctx, b.ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if evt := lastJobEvent(t, admin, b.ID); !evt.Firmed || evt.ChangeoverCost != "45.5" || evt.CleaningSetID != "WET-CLEAN" {
		t.Fatalf("job.scheduled after firm = %+v, want the stored cost and cleaning set", evt)
	}

	// The database refuses a cost without a currency and a cost on a later
	// segment, so the row stays well formed whoever writes it.
	for name, q := range map[string]string{
		"cost without currency": `update scheduled_job set changeover_currency = null where job_id = $1`,
		"cost on segment 2":     `update scheduled_job set segment_no = 2 where job_id = $1`,
	} {
		var pgErr *pgconn.PgError
		if _, err := admin.Exec(context.Background(), q, c.ID); !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Errorf("%s: err = %v, want a check violation (23514)", name, err)
		}
	}
}

// A move reprices the moved job's cost from its new neighbour (br2312, USD):
// C straight after A is A -> C, 90 min costing 120; moved behind the next
// day's B it is B -> C, 20 min costing 30, and the stored row says so.
func TestMoveJob_RepricesChangeoverCost(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	br2312Matrix(t, ctx, s, r.ID, "2027-01-01")
	groupOperation(t, ctx, s, "A", 60, r.ID, 9)
	c := groupOperation(t, ctx, s, "C", 60, r.ID, 11)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	wantBarCost(t, barsOf(t, ctx, s)[c.ID], "120", "USD", nil)
	b := groupOperation(t, ctx, s, "B", 60, r.ID, 30)
	if _, _, err := s.RunSchedule(ctx, store.RunRequest{Scope: "selection", Mode: "jobs", Finite: true, OperationIDs: []string{b.ID},
		Start: at(1, 6, 0), HorizonDays: 60}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	wantBarCost(t, barsOf(t, ctx, s)[b.ID], "80", "USD", nil)
	out, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: c.ID, Start: at(1, 14, 0), HorizonDays: 60}, store.Idem{})
	if err != nil || out.Conflict != nil {
		t.Fatalf("move = %+v err=%v", out, err)
	}
	bar := barsOf(t, ctx, s)[c.ID]
	if bar.ChangeoverMinutes != 20 || !bar.Start.Equal(at(1, 14, 0)) {
		t.Fatalf("moved C = %+v, want the B -> C changeover of 20 min from 14:00", bar)
	}
	wantBarCost(t, bar, "30", "USD", nil)
	if evt := lastJobEvent(t, admin, c.ID); evt.ChangeoverCost != "30" || evt.ChangeoverCurrency != "USD" {
		t.Fatalf("job.scheduled after move = %+v, want cost 30 USD", evt)
	}
}

// The Gantt bar spells the cost as an exact decimal string (the OpenAPI
// Decimal schema) and an unpriced job as nulls, never as 0.
func TestBar_ChangeoverCostJSON(t *testing.T) {
	cost := decimal.RequireFromString("45.50")
	priced, err := json.Marshal(store.Bar{ChangeoverMinutes: 30, ChangeoverCost: &cost, ChangeoverCurrency: strp("EUR"), CleaningSetID: strp("WET-CLEAN")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"changeoverCost":"45.5"`, `"changeoverCurrency":"EUR"`, `"cleaningSetId":"WET-CLEAN"`} {
		if !strings.Contains(string(priced), want) {
			t.Errorf("priced bar %s lacks %s", priced, want)
		}
	}
	plain, err := json.Marshal(store.Bar{})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"changeoverCost":null`, `"changeoverCurrency":null`, `"cleaningSetId":null`} {
		if !strings.Contains(string(plain), want) {
			t.Errorf("unpriced bar %s lacks %s", plain, want)
		}
	}
}
