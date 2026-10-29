package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	"github.com/BenjaminSRussell/passage/services/eam/migrations"
)

// activeAssets creates n more ACTIVE assets on the fixture site.
func activeAssets(t *testing.T, ctx context.Context, s *Postgres, n int, prefix string) []string {
	t.Helper()
	out := make([]string, n)
	for i := range out {
		a, err := s.CreateAsset(ctx, Asset{Name: fmt.Sprintf("%s %d", prefix, i+1), AssetType: "VALVE", SiteID: fixtureSite, Criticality: 3},
			fmt.Sprintf("%s-asset-%d", prefix, i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.ActivateAsset(ctx, a.ID, a.Version); err != nil {
			t.Fatal(err)
		}
		out[i] = a.ID
	}
	return out
}

// sixStops is the AC-44.9 round: six stops, stop 4 mandatory, 5 minutes apart.
func sixStops(assets []string) []domain.RoundStop {
	out := make([]domain.RoundStop, len(assets))
	for i, a := range assets {
		out[i] = domain.RoundStop{Sequence: i + 1, AssetID: a, JobType: "INSPECT", Mandatory: i == 3, TravelMinutes: 5 * min(i, 1)}
	}
	return out
}

// TestRound_AC449: a weekly round of 6 stops scheduled by a ROUND plan line
// generates one work order with the 6 stops; it cannot complete while a stop is
// open (EAM-RND-001); with stop 4 SKIPPED on a mandatory job it completes, the
// line resets once, and a ROUND request exists for stop 4's asset.
func TestRound_AC449(t *testing.T) {
	f := setup(t)
	assets := append([]string{f.asset}, activeAssets(t, f.ctx, f.s, 5, "valve")...)
	round, err := f.s.CreateRound(f.ctx, Round{Code: "WEEKLY-A", Name: "Weekly area A", SiteID: fixtureSite, Stops: sixStops(assets)}, "round-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(round.Stops) != 6 || round.TravelMinutes != 25 || !round.Active {
		t.Fatalf("round = %d stops, %d travel minutes, active %v; want 6, 25, true", len(round.Stops), round.TravelMinutes, round.Active)
	}
	if again, err := f.s.CreateRound(f.ctx, Round{Code: "OTHER", Name: "x", SiteID: fixtureSite, Stops: sixStops(assets)}, "round-1"); err != nil || again.ID != round.ID {
		t.Fatalf("replayed key = %+v %v, want round %s", again, err, round.ID)
	}

	one := 1
	weeks := "WEEKS"
	start := clock.AddDate(0, 0, -7)
	plan, err := f.s.CreatePlan(f.ctx, PlanInput{Code: "ROUNDS", Name: "Rounds", Currency: "USD", Lines: []PlanLineInput{{
		Kind: "ROUND", RoundID: &round.ID, IntervalValue: &one, IntervalUnit: &weeks, StartAt: &start}}}, "plan-1")
	if err != nil {
		t.Fatal(err)
	}
	lineID := plan.LineIDs[0]
	page, err := f.s.ListPlans(f.ctx, ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	line := page.Items[0].Lines[0]
	if line.Kind != "ROUND" || line.RoundID == nil || *line.RoundID != round.ID || line.AssetID != f.asset || line.JobType != "" {
		t.Fatalf("line = %+v, want a ROUND line on the round, anchored on the first stop's asset", line)
	}
	var woID string
	f.inTenant(t, func(tx pgx.Tx) {
		if err := tx.QueryRow(f.ctx, `select id::text from work_order where plan_line_id = $1`, lineID).Scan(&woID); err != nil {
			t.Fatalf("the due round line generated no work order: %v", err)
		}
	})
	detail, err := f.s.GetWorkOrder(f.ctx, woID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Type != "ROUND" || detail.AssetID != f.asset || detail.DueAt == nil || !detail.DueAt.Equal(clock) || len(detail.Stops) != 6 {
		t.Fatalf("round order = type %s asset %s due %v with %d stops; want ROUND on the anchor, due %v, 6 stops",
			detail.Type, detail.AssetID, detail.DueAt, len(detail.Stops), clock)
	}
	for i, st := range detail.Stops {
		if st.Sequence != i+1 || st.AssetID != assets[i] || st.Outcome != nil || st.Mandatory != (i == 3) {
			t.Fatalf("stop %d = %+v, want stop %d on asset %s with no outcome", i, st, i+1, assets[i])
		}
	}
	if detail.WarrantyCovered {
		t.Fatal("a ROUND order is never warranty covered")
	}
	if n := f.count(t, `select count(*) from outbox where type = 'dev.passage.eam.workorder.created.v1' and aggregate_id = $1`, woID); n != 1 {
		t.Fatalf("workorder.created events for the round order = %d, want 1", n)
	}

	if _, err := f.s.RecordStopOutcome(f.ctx, woID, 1, StopOutcome{Outcome: domain.StopDone}, "early"); problemCode(err) != domain.CodeStopOutcome {
		t.Fatalf("outcome before the round started: %v, want %s", err, domain.CodeStopOutcome)
	}
	v := f.advance(t, woID, detail.Version, domain.StatusScheduled, domain.StatusReleased, domain.StatusInProgress)
	begin, end := clock.Add(-3*time.Hour), clock.Add(-time.Hour)
	if _, err := f.s.TransitionWorkOrder(f.ctx, woID, Transition{To: domain.StatusCompleted, Version: v, ActualStart: &begin, ActualEnd: &end}, ""); problemCode(err) != domain.CodeRoundIncomplete {
		t.Fatalf("completion with 6 open stops: %v, want %s", err, domain.CodeRoundIncomplete)
	}
	for seq := 1; seq <= 6; seq++ {
		in := StopOutcome{Outcome: domain.StopDone, Actor: "tech-1"}
		if seq == 4 {
			in = StopOutcome{Outcome: domain.StopSkipped, Reason: "  valve area flooded ", Actor: "tech-1"}
		}
		st, err := f.s.RecordStopOutcome(f.ctx, woID, seq, in, fmt.Sprintf("stop-%d", seq))
		if err != nil {
			t.Fatalf("stop %d: %v", seq, err)
		}
		if st.Outcome == nil || *st.Outcome != in.Outcome || st.RecordedBy == nil || *st.RecordedBy != "tech-1" || st.RecordedAt == nil {
			t.Fatalf("stop %d recorded = %+v", seq, st)
		}
		if seq == 4 && (st.Reason == nil || *st.Reason != "valve area flooded" || st.RequestID == nil) {
			t.Fatalf("skipped mandatory stop = %+v, want the trimmed reason and a request", st)
		}
		if seq != 4 && st.RequestID != nil {
			t.Fatalf("stop %d raised a request", seq)
		}
	}
	if _, err := f.s.TransitionWorkOrder(f.ctx, woID, Transition{To: domain.StatusCompleted, Version: v, ActualStart: &begin, ActualEnd: &end}, "complete"); err != nil {
		t.Fatalf("completion with every stop recorded: %v", err)
	}

	var reqID, reqSource, reqAsset string
	f.inTenant(t, func(tx pgx.Tx) {
		if err := tx.QueryRow(f.ctx, `select r.id::text, r.source, r.asset_id::text from maintenance_request r
		  join work_order_stop s on s.request_id = r.id where s.work_order_id = $1 and s.sequence = 4`, woID).Scan(&reqID, &reqSource, &reqAsset); err != nil {
			t.Fatal(err)
		}
	})
	if reqSource != "ROUND" || reqAsset != assets[3] {
		t.Fatalf("request = source %s asset %s, want ROUND on stop 4's asset %s", reqSource, reqAsset, assets[3])
	}
	if n := f.count(t, `select count(*) from maintenance_request where source = 'ROUND'`); n != 1 {
		t.Fatalf("ROUND requests = %d, want 1 (only the skipped mandatory stop)", n)
	}
	if n := f.count(t, `select count(*) from outbox where type = 'dev.passage.eam.request.created.v1' and aggregate_id = $1`, reqID); n != 1 {
		t.Fatalf("request.created events = %d, want 1", n)
	}
	// The line reset once for the whole round (BR-44.2 on the round's completion)
	// and rescheduled at once: one week after the completion, still one open order.
	page, err = f.s.ListPlans(f.ctx, ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	line = page.Items[0].Lines[0]
	if line.LastCompletedAt == nil || !line.LastCompletedAt.Equal(end) || line.NextDueAt == nil || !line.NextDueAt.Equal(end.AddDate(0, 0, 7)) {
		t.Fatalf("line after completion: last %v next %v; want %v and a week later", line.LastCompletedAt, line.NextDueAt, end)
	}
	if n := f.count(t, `select count(*) from work_order where plan_line_id = $1`, lineID); n != 2 {
		t.Fatalf("orders of the round line = %d, want 2 (completed and the next occurrence)", n)
	}
	if n := f.count(t, `select count(*) from work_order where plan_line_id = $1 and status = 'CREATED'`, lineID); n != 1 {
		t.Fatalf("open orders of the round line = %d, want 1", n)
	}
	if _, err := f.s.RecordStopOutcome(f.ctx, woID, 4, StopOutcome{Outcome: domain.StopDone}, "late"); problemCode(err) != domain.CodeStopOutcome {
		t.Fatalf("outcome on a completed round: %v, want %s", err, domain.CodeStopOutcome)
	}
	// Running the scheduler again creates nothing while the next order is open.
	if res, err := f.s.SchedulePlans(f.ctx); err != nil || res.Generated != 0 {
		t.Fatalf("rerun = %+v %v, want nothing generated", res, err)
	}
}

// TestRound_Rules covers the definition rules (EAM-RND-003, EAM-DUP-001), the
// stop outcome rules (EAM-RND-002, key replay and reuse), the ACTIVE check on
// every stop asset, and the fail-closed permit gate on rounds.
func TestRound_Rules(t *testing.T) {
	f := setup(t)
	others := activeAssets(t, f.ctx, f.s, 1, "gauge")
	stops := []domain.RoundStop{{Sequence: 1, AssetID: f.asset, JobType: "INSPECT", Mandatory: true}, {Sequence: 2, AssetID: others[0], JobType: "READ", TravelMinutes: 3}}
	round, err := f.s.CreateRound(f.ctx, Round{Code: "DAILY", Name: "Daily", SiteID: fixtureSite, Stops: stops}, "r-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreateRound(f.ctx, Round{Code: "DAILY", Name: "Again", SiteID: fixtureSite, Stops: stops}, "r-2"); problemCode(err) != CodeDuplicate {
		t.Fatalf("duplicate code: %v, want %s", err, CodeDuplicate)
	}
	if _, err := f.s.CreateRound(f.ctx, Round{Code: "ELSEWHERE", Name: "x", SiteID: testkit.TenantID(7), Stops: stops}, "r-3"); problemCode(err) != domain.CodeRoundDefinition {
		t.Fatalf("stop asset on another site: %v, want %s", err, domain.CodeRoundDefinition)
	}
	gap := []domain.RoundStop{{Sequence: 1, AssetID: f.asset, JobType: "INSPECT"}, {Sequence: 3, AssetID: f.asset, JobType: "INSPECT"}}
	if _, err := f.s.CreateRound(f.ctx, Round{Code: "GAP", Name: "x", SiteID: fixtureSite, Stops: gap}, "r-4"); problemCode(err) != domain.CodeRoundDefinition {
		t.Fatalf("stops 1 and 3: %v, want %s", err, domain.CodeRoundDefinition)
	}
	missing := []domain.RoundStop{{Sequence: 1, AssetID: testkit.TenantID(55), JobType: "INSPECT"}}
	if _, err := f.s.CreateRound(f.ctx, Round{Code: "MISSING", Name: "x", SiteID: fixtureSite, Stops: missing}, "r-5"); problemCode(err) != CodeNotFound {
		t.Fatalf("unknown stop asset: %v, want %s", err, CodeNotFound)
	}
	if _, err := f.s.UpsertPermitType(f.ctx, PermitType{Code: "HOT_WORK", Name: "Hot work", ApproverRole: "Safety", MaxValidityHours: 8}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.SetJobPermits(f.ctx, "WELD", []string{"HOT_WORK"}); err != nil {
		t.Fatal(err)
	}
	weld := []domain.RoundStop{{Sequence: 1, AssetID: f.asset, JobType: "WELD"}}
	if _, err := f.s.CreateRound(f.ctx, Round{Code: "WELDS", Name: "x", SiteID: fixtureSite, Stops: weld}, "r-6"); problemCode(err) != domain.CodeRoundDefinition {
		t.Fatalf("stop job type needing a permit: %v, want %s", err, domain.CodeRoundDefinition)
	}
	if page, err := f.s.ListRounds(f.ctx, ListOptions{Limit: 10}); err != nil || len(page.Items) != 1 || len(page.Items[0].Stops) != 2 {
		t.Fatalf("rounds = %+v %v, want the one round with its 2 stops", page, err)
	}

	// An extra occurrence on demand; a stop asset that is not ACTIVE blocks it.
	wo, err := f.s.CreateRoundWorkOrder(f.ctx, round.ID, RoundWorkOrderInput{Currency: "EUR"}, "rwo-1")
	if err != nil {
		t.Fatal(err)
	}
	if wo.Type != "ROUND" || wo.PlanLineID != nil || wo.Currency != "EUR" || wo.Priority != 3 {
		t.Fatalf("on-demand round order = %+v", wo)
	}
	if again, err := f.s.CreateRoundWorkOrder(f.ctx, round.ID, RoundWorkOrderInput{Currency: "EUR"}, "rwo-1"); err != nil || again.ID != wo.ID {
		t.Fatalf("replayed key = %+v %v, want %s", again, err, wo.ID)
	}
	plain, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: "PREVENTIVE", Currency: "USD"}, "plain")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreateRoundWorkOrder(f.ctx, round.ID, RoundWorkOrderInput{Currency: "USD"}, "plain"); problemCode(err) != CodeKeyReuse {
		t.Fatalf("key of a preventive order: %v, want %s", err, CodeKeyReuse)
	}
	gauge, err := f.s.GetAsset(f.ctx, others[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.ChangeAssetLifecycle(f.ctx, gauge.ID, "INACTIVE", gauge.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreateRoundWorkOrder(f.ctx, round.ID, RoundWorkOrderInput{Currency: "USD"}, "rwo-2"); problemCode(err) != CodeAssetState {
		t.Fatalf("stop asset INACTIVE: %v, want %s", err, CodeAssetState)
	}

	v := f.advance(t, wo.ID, wo.Version, domain.StatusScheduled, domain.StatusReleased, domain.StatusInProgress)
	f.advance(t, plain.ID, plain.Version, domain.StatusScheduled, domain.StatusReleased, domain.StatusInProgress)
	if _, err := f.s.RecordStopOutcome(f.ctx, plain.ID, 1, StopOutcome{Outcome: domain.StopDone}, "p-1"); problemCode(err) != domain.CodeStopOutcome {
		t.Fatalf("stop on a PREVENTIVE order: %v, want %s", err, domain.CodeStopOutcome)
	}
	if _, err := f.s.RecordStopOutcome(f.ctx, wo.ID, 1, StopOutcome{Outcome: domain.StopSkipped, Reason: "   "}, "s-0"); problemCode(err) != domain.CodeStopOutcome {
		t.Fatalf("skip without a reason: %v, want %s", err, domain.CodeStopOutcome)
	}
	if _, err := f.s.RecordStopOutcome(f.ctx, wo.ID, 3, StopOutcome{Outcome: domain.StopDone}, "s-3"); problemCode(err) != CodeNotFound {
		t.Fatalf("stop 3 of 2: %v, want %s", err, CodeNotFound)
	}
	first, err := f.s.RecordStopOutcome(f.ctx, wo.ID, 1, StopOutcome{Outcome: domain.StopSkipped, Reason: "no access"}, "s-1")
	if err != nil {
		t.Fatal(err)
	}
	if again, err := f.s.RecordStopOutcome(f.ctx, wo.ID, 1, StopOutcome{Outcome: domain.StopSkipped, Reason: "no access"}, "s-1"); err != nil || again.RequestID == nil || *again.RequestID != *first.RequestID {
		t.Fatalf("replayed stop = %+v %v, want the first recording", again, err)
	}
	if n := f.count(t, `select count(*) from maintenance_request where source = 'ROUND'`); n != 1 {
		t.Fatalf("ROUND requests after a replay = %d, want 1", n)
	}
	if _, err := f.s.RecordStopOutcome(f.ctx, wo.ID, 2, StopOutcome{Outcome: domain.StopDone}, "s-1"); problemCode(err) != CodeKeyReuse {
		t.Fatalf("key of stop 1 on stop 2: %v, want %s", err, CodeKeyReuse)
	}
	if _, err := f.s.RecordStopOutcome(f.ctx, wo.ID, 1, StopOutcome{Outcome: domain.StopDone}, "s-1b"); problemCode(err) != domain.CodeStopOutcome {
		t.Fatalf("second outcome on stop 1: %v, want %s", err, domain.CodeStopOutcome)
	}
	// An optional stop skipped raises no request.
	if st, err := f.s.RecordStopOutcome(f.ctx, wo.ID, 2, StopOutcome{Outcome: domain.StopSkipped, Reason: "gauge removed"}, "s-2"); err != nil || st.RequestID != nil {
		t.Fatalf("optional stop skipped = %+v %v, want no request", st, err)
	}
	if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusCompleted, Version: v}, ""); err != nil {
		t.Fatalf("completion with every stop recorded: %v", err)
	}

	// A permit requirement added to a stop's job type after the round was
	// defined stops its orders from starting (fail closed).
	g2, err := f.s.CreateRound(f.ctx, Round{Code: "SOLO", Name: "Solo", SiteID: fixtureSite,
		Stops: []domain.RoundStop{{Sequence: 1, AssetID: f.asset, JobType: "GREASE"}}}, "r-7")
	if err != nil {
		t.Fatal(err)
	}
	solo, err := f.s.CreateRoundWorkOrder(f.ctx, g2.ID, RoundWorkOrderInput{Currency: "USD"}, "rwo-3")
	if err != nil {
		t.Fatal(err)
	}
	sv := f.advance(t, solo.ID, solo.Version, domain.StatusScheduled, domain.StatusReleased)
	if _, err := f.s.SetJobPermits(f.ctx, "GREASE", []string{"HOT_WORK"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.TransitionWorkOrder(f.ctx, solo.ID, Transition{To: domain.StatusInProgress, Version: sv}, ""); problemCode(err) != domain.CodePermitMissing {
		t.Fatalf("round start with a permit job type: %v, want %s", err, domain.CodePermitMissing)
	}
}

// TestRoundLine_Validation: ROUND lines need a round and a time interval, and
// take no asset, job type or counter of their own (EAM-PLN-003).
func TestRoundLine_Validation(t *testing.T) {
	f := setup(t)
	one, weeks := 1, "WEEKS"
	round, err := f.s.CreateRound(f.ctx, Round{Code: "R1", Name: "R1", SiteID: fixtureSite,
		Stops: []domain.RoundStop{{Sequence: 1, AssetID: f.asset, JobType: "INSPECT"}}}, "r")
	if err != nil {
		t.Fatal(err)
	}
	interval := 100.0
	for name, line := range map[string]PlanLineInput{
		"no round":        {Kind: "ROUND", IntervalValue: &one, IntervalUnit: &weeks},
		"no interval":     {Kind: "ROUND", RoundID: &round.ID},
		"with an asset":   {Kind: "ROUND", RoundID: &round.ID, IntervalValue: &one, IntervalUnit: &weeks, AssetID: f.asset},
		"with a counter":  {Kind: "ROUND", RoundID: &round.ID, IntervalValue: &one, IntervalUnit: &weeks, CounterID: &f.counter, CounterInterval: &interval},
		"round on a TIME": {Kind: "TIME", RoundID: &round.ID, AssetID: f.asset, JobType: "LUBE", IntervalValue: &one, IntervalUnit: &weeks},
	} {
		if _, err := f.s.CreatePlan(f.ctx, PlanInput{Code: "PL", Name: "P", Currency: "USD", Lines: []PlanLineInput{line}}, "p-"+name); problemCode(err) != CodeLineInvalid {
			t.Errorf("%s: %v, want %s", name, err, CodeLineInvalid)
		}
	}
	missing := testkit.TenantID(77)
	if _, err := f.s.CreatePlan(f.ctx, PlanInput{Code: "PL", Name: "P", Currency: "USD", Lines: []PlanLineInput{
		{Kind: "ROUND", RoundID: &missing, IntervalValue: &one, IntervalUnit: &weeks}}}, "p-missing"); problemCode(err) != CodeNotFound {
		t.Errorf("unknown round: %v, want %s", err, CodeNotFound)
	}
}

// TestEmergencyOverdue_BR447: priority 1 orders still open and never scheduled
// or released are flagged once the window has passed (EAM-WO-003); planned,
// finished and non-emergency orders are not.
func TestEmergencyOverdue_BR447(t *testing.T) {
	f := setup(t)
	mk := func(typ, key string) WorkOrder {
		wo, err := f.s.CreateWorkOrder(f.ctx, WorkOrder{AssetID: f.asset, Type: typ, Currency: "USD"}, key)
		if err != nil {
			t.Fatal(err)
		}
		return wo
	}
	waiting := mk("EMERGENCY", "em-waiting")
	started := mk("EMERGENCY", "em-started")
	f.advance(t, started.ID, started.Version, domain.StatusInProgress)
	planned := mk("EMERGENCY", "em-planned")
	f.advance(t, planned.ID, planned.Version, domain.StatusReleased, domain.StatusInProgress)
	done := mk("EMERGENCY", "em-done")
	v := f.advance(t, done.ID, done.Version, domain.StatusInProgress)
	f.fault(t, done.ID)
	f.advance(t, done.ID, v, domain.StatusCompleted)
	mk("PREVENTIVE", "pm")

	// Orders are stamped by the database clock; read the alert a day later.
	later := NewWithClock(f.pool, func() time.Time { return time.Now().UTC().Add(25 * time.Hour) })
	got, err := later.EmergencyOverdue(f.ctx, domain.RegularizationWindow)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != waiting.ID || got[1].ID != started.ID {
		t.Fatalf("overdue = %+v, want the waiting and the started-before-release orders", got)
	}
	if got[0].Code != "EAM-WO-003" || got[0].Detail != "Emergency work order "+waiting.Number+" not regularized within 24 h" {
		t.Fatalf("alert = %s %q", got[0].Code, got[0].Detail)
	}
	if got, err := later.EmergencyOverdue(f.ctx, 26*time.Hour); err != nil || len(got) != 0 {
		t.Fatalf("26 h window = %+v %v, want none", got, err)
	}
	if got, err := f.s.EmergencyOverdue(f.ctx, domain.RegularizationWindow); err != nil || len(got) != 0 {
		t.Fatalf("right after creation = %+v %v, want none", got, err)
	}
}

// TestPlanReviews_BR4415: assets of criticality 4 or 5 need a review when never
// assessed or when the last assessment is 12 months old.
func TestPlanReviews_BR4415(t *testing.T) {
	f := setup(t)
	low := activeAssets(t, f.ctx, f.s, 1, "low")[0] // criticality 3
	got, err := f.s.PlanReviews(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].AssetID != f.asset || got[0].LastReviewedAt != nil || got[0].DueAt != nil {
		t.Fatalf("reviews = %+v, want only the never-assessed criticality 4 asset", got)
	}
	if _, err := f.s.AssessCriticality(f.ctx, f.asset, domain.Consequences{Safety: 3, Environment: 1, Production: 5, Quality: 1, Cost: 1}, 4, "rel-eng", "a-1"); err != nil {
		t.Fatal(err)
	}
	if got, err := f.s.PlanReviews(f.ctx); err != nil || len(got) != 0 {
		t.Fatalf("after an assessment = %+v %v, want none", got, err)
	}
	yearOn := NewWithClock(f.pool, func() time.Time { return clock.AddDate(1, 0, 0) })
	got, err = yearOn.PlanReviews(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Criticality != 5 || got[0].LastReviewedAt == nil || !got[0].LastReviewedAt.Equal(clock) ||
		got[0].DueAt == nil || !got[0].DueAt.Equal(clock.AddDate(1, 0, 0)) {
		t.Fatalf("a year on = %+v, want the asset due on %v", got, clock.AddDate(1, 0, 0))
	}
	for _, r := range got {
		if r.AssetID == low {
			t.Fatal("criticality 3 asset listed")
		}
	}
}

// TestKPIs_CostAndBacklog (FR-44.13): completed order costs roll up to the
// asset in decimal; orders outside the window or of another asset type are not
// counted; the backlog counts open orders per priority with the overdue ones.
func TestKPIs_CostAndBacklog(t *testing.T) {
	f := setup(t)
	complete := func(key string, hours, rate int64, end time.Time) {
		wo, v := f.inProgress(t, "PREVENTIVE", key)
		if _, err := f.s.RecordLabor(f.ctx, wo.ID, decimal.New(hours, -1), decimal.NewFromInt(rate), nil, key+"-labor"); err != nil {
			t.Fatal(err)
		}
		start := end.Add(-time.Hour)
		if _, err := f.s.TransitionWorkOrder(f.ctx, wo.ID, Transition{To: domain.StatusCompleted, Version: v, ActualStart: &start, ActualEnd: &end}, ""); err != nil {
			t.Fatal(err)
		}
	}
	complete("in-1", 80, 45, clock.Add(-2*time.Hour))    // 8 h at 45.00 = 360.00
	complete("in-2", 25, 40, clock.Add(-26*time.Hour))   // 2.5 h at 40.00 = 100.00
	complete("out", 10, 99, clock.Add(-10*24*time.Hour)) // outside the window
	costs, err := f.s.CostByAsset(f.ctx, CostFilter{From: clock.AddDate(0, 0, -7), To: clock})
	if err != nil {
		t.Fatal(err)
	}
	if len(costs) != 1 {
		t.Fatalf("cost rows = %+v, want one for the fixture asset", costs)
	}
	c := costs[0]
	if c.AssetID != f.asset || c.Currency != "USD" || c.WorkOrders != 2 || c.LaborCost != "460.00" || c.PartsCost != "0.00" || c.Total != "460.00" {
		t.Fatalf("cost = %+v, want 2 orders, labor 460.00, total 460.00", c)
	}
	if other, err := f.s.CostByAsset(f.ctx, CostFilter{From: clock.AddDate(0, 0, -7), To: clock, AssetType: "VALVE"}); err != nil || len(other) != 0 {
		t.Fatalf("VALVE costs = %+v %v, want none", other, err)
	}

	past, future := clock.Add(-48*time.Hour), clock.Add(48*time.Hour)
	for i, w := range []WorkOrder{
		{AssetID: f.asset, Type: "PREVENTIVE", Currency: "USD", Priority: 3, DueAt: &past},
		{AssetID: f.asset, Type: "PREVENTIVE", Currency: "USD", Priority: 3, DueAt: &future},
		{AssetID: f.asset, Type: "INSPECTION", Currency: "USD", Priority: 3},
		{AssetID: f.asset, Type: "EMERGENCY", Currency: "USD"},
	} {
		if _, err := f.s.CreateWorkOrder(f.ctx, w, fmt.Sprintf("open-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.s.Backlog(f.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Priority != 1 || rows[0].OpenCount != 1 || rows[1].Priority != 3 || rows[1].OpenCount != 3 ||
		rows[1].OverdueCount != 1 || rows[1].OldestDueAt == nil || !rows[1].OldestDueAt.Equal(past) {
		t.Fatalf("backlog = %+v, want priority 1: 1 open; priority 3: 3 open, 1 overdue, oldest due %v", rows, past)
	}
	if rows, err := f.s.Backlog(f.ctx, testkit.TenantID(7)); err != nil || len(rows) != 0 {
		t.Fatalf("backlog on another site = %+v %v, want none", rows, err)
	}
}

// TestGrants_AppRoleRounds runs the slice 5 paths as passage_app (no BYPASSRLS,
// migration 0007 grants): a round and a ROUND plan line, the generated order
// with its stops, a stop row lock and update that raises a request, completion,
// the reminders and KPIs; rounds and stops refuse updates and deletes, and
// another tenant sees none of it.
func TestGrants_AppRoleRounds(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := NewWithClock(app, func() time.Time { return clock })
	ctx := testkit.Ctx(testkit.TenantID(1))
	assets := activeAssets(t, ctx, s, 2, "app")
	round, err := s.CreateRound(ctx, Round{Code: "APP", Name: "App round", SiteID: fixtureSite, Stops: []domain.RoundStop{
		{Sequence: 1, AssetID: assets[0], JobType: "INSPECT", Mandatory: true}, {Sequence: 2, AssetID: assets[1], JobType: "INSPECT"}}}, "g-round")
	if err != nil {
		t.Fatalf("round insert as passage_app: %v", err)
	}
	one, weeks, start := 1, "WEEKS", clock.AddDate(0, 0, -7)
	_, err = s.CreatePlan(ctx, PlanInput{Code: "APP", Name: "App", Currency: "USD", Lines: []PlanLineInput{{Kind: "ROUND", RoundID: &round.ID,
		IntervalValue: &one, IntervalUnit: &weeks, StartAt: &start}}}, "g-plan")
	if err != nil {
		t.Fatalf("ROUND plan line as passage_app: %v", err)
	}
	orders, err := s.ListWorkOrders(ctx, ListOptions{Limit: 10})
	if err != nil || len(orders.Items) != 1 || orders.Items[0].Type != "ROUND" {
		t.Fatalf("generated orders as passage_app = %+v %v", orders, err)
	}
	wo := orders.Items[0]
	v := wo.Version
	for _, to := range []domain.Status{domain.StatusScheduled, domain.StatusReleased, domain.StatusInProgress} {
		next, err := s.TransitionWorkOrder(ctx, wo.ID, Transition{To: to, Version: v}, "")
		if err != nil {
			t.Fatal(err)
		}
		v = next.Version
	}
	if st, err := s.RecordStopOutcome(ctx, wo.ID, 1, StopOutcome{Outcome: domain.StopSkipped, Reason: "blocked"}, "g-s1"); err != nil || st.RequestID == nil {
		t.Fatalf("stop lock, update and request as passage_app = %+v %v", st, err)
	}
	if _, err := s.RecordStopOutcome(ctx, wo.ID, 2, StopOutcome{Outcome: domain.StopDone}, "g-s2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TransitionWorkOrder(ctx, wo.ID, Transition{To: domain.StatusCompleted, Version: v}, ""); err != nil {
		t.Fatalf("round completion and line reset as passage_app: %v", err)
	}
	if _, err := s.ListPlans(ctx, ListOptions{Limit: 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetWorkOrder(ctx, wo.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EmergencyOverdue(ctx, domain.RegularizationWindow); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PlanReviews(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CostByAsset(ctx, CostFilter{From: clock.AddDate(0, 0, -7), To: clock.AddDate(0, 0, 1)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Backlog(ctx, ""); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`update maintenance_round set name = name`,
		`delete from maintenance_round`,
		`update round_stop set mandatory = mandatory`,
		`delete from round_stop`,
		`delete from work_order_stop`,
	} {
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
	two := testkit.Ctx(testkit.TenantID(2))
	if _, err := s.GetRound(two, round.ID); problemCode(err) != CodeNotFound {
		t.Fatalf("tenant 2 reading tenant 1's round: %v, want %s", err, CodeNotFound)
	}
	if page, err := s.ListRounds(two, ListOptions{Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("tenant 2 rounds = %+v %v, want none", page, err)
	}
	if _, err := s.RecordStopOutcome(two, wo.ID, 1, StopOutcome{Outcome: domain.StopDone}, "x"); problemCode(err) != CodeNotFound {
		t.Fatalf("tenant 2 recording on tenant 1's round order: %v, want %s", err, CodeNotFound)
	}
}
