package store

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/optimizer"
)

const optDate, optZone = "2026-10-26", "America/Chicago"

func (d dispatchFixture) homed(t *testing.T, code string) Technician {
	t.Helper()
	tech, _, err := d.s.CreateTechnician(d.ctx, Technician{Code: code, Name: "Tech " + code, LegalEntityID: d.le, TerritoryIDs: []string{d.territory},
		Skills: []string{"HVAC"}, TimeZone: optZone, ShiftDays: []int{1, 2, 3, 4, 5}, ShiftStartMinute: 480, ShiftEndMinute: 1020,
		MaxDailyMinutes: 480, Active: true, HomeLatitude: ref(41.88), HomeLongitude: ref(-87.63)}, "key-"+code, "hash-"+code)
	if err != nil {
		t.Fatal(err)
	}
	return tech
}

func (d dispatchFixture) located(t *testing.T, minutes int, lat, lon float64, caseID *string) WorkOrder {
	t.Helper()
	wo, _, err := d.s.CreateWorkOrder(d.ctx, WorkOrder{CaseID: caseID, CustomerPartyID: d.customer, LegalEntityID: d.le, TerritoryID: d.territory,
		EstDurationMinutes: minutes, Currency: "USD", RequiredSkills: []string{"HVAC"}, Latitude: &lat, Longitude: &lon}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return wo
}

func (d dispatchFixture) request(t *testing.T, key string) Optimization {
	t.Helper()
	o, _, err := d.s.RequestOptimization(d.ctx, OptimizeInput{LegalEntityID: d.le, TerritoryID: d.territory, Date: optDate, TimeZone: optZone, TimeLimit: 10},
		time.Now(), key, "hash-"+key)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func (d dispatchFixture) succeeded(t *testing.T, o Optimization, plan ...optimizer.Assignment) Optimization {
	t.Helper()
	now := time.Now()
	if _, err := d.s.RecordOptimizationSubmitted(d.ctx, o.ID, o.AttemptCount, "remote-1", now); err != nil {
		t.Fatal(err)
	}
	done, err := d.s.RecordOptimizationResult(d.ctx, o.ID, "remote-1", optimizer.Result{Assignments: plan, Unassigned: []string{}, Solver: "cpsat", CPSatStatus: "OPTIMAL"}, now)
	if err != nil || done.Status != OptSucceeded {
		t.Fatalf("result stored: %+v %v", done, err)
	}
	return done
}

func TestStore_OptimizationSnapshotsTheTerritoryDay(t *testing.T) {
	d := newDispatch(t, 21)
	ana := d.homed(t, "ANA")
	if _, _, err := d.s.CreateTechnician(d.ctx, Technician{Code: "NOH", Name: "No home", LegalEntityID: d.le, TerritoryIDs: []string{d.territory},
		TimeZone: optZone, ShiftDays: []int{1}, ShiftStartMinute: 480, ShiftEndMinute: 1020, MaxDailyMinutes: 480, Active: true}, "", ""); err != nil {
		t.Fatal(err)
	}
	c := d.open(t, d.at(26, 8, 0))
	urgent := d.located(t, 60, 41.9, -87.65, &c.ID)
	plain := d.located(t, 90, 41.92, -87.6, nil)
	held := d.assign(t, d.located(t, 60, 41.881, -87.631, nil), ana, d.at(26, 14, 0))
	d.order(t, 30, nil, nil)

	o := d.request(t, "snap-1")
	if o.Status != OptPending || o.Technicians != 1 || o.WorkOrders != 2 || o.Pinned != 1 || o.AttemptCount != 0 {
		t.Fatalf("optimization %+v", o)
	}
	if len(o.Skipped) != 2 {
		t.Fatalf("skipped %+v, want the technician without a home and the order without a position", o.Skipped)
	}
	req, err := d.s.OptimizationRequest(d.ctx, o.ID)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]optimizer.WorkOrder{}
	for _, w := range req.WorkOrders {
		byID[w.ID] = w
	}
	if u := byID[urgent.ID]; u.Priority != 2 || u.Due == nil || byID[plain.ID].Priority != 4 || byID[plain.ID].Due != nil {
		t.Fatalf("priorities and due: %+v %+v", u, byID[plain.ID])
	}
	if len(req.Pinned) != 1 || req.Pinned[0].WorkOrderID != held.ID || req.Pinned[0].Start != 840 || req.TimeLimitSeconds != 10 {
		t.Fatalf("pinned %+v", req.Pinned)
	}

	again, replayed, err := d.s.RequestOptimization(d.ctx, OptimizeInput{LegalEntityID: d.le, TerritoryID: d.territory, Date: optDate, TimeZone: optZone, TimeLimit: 10},
		time.Now(), "snap-1", "hash-snap-1")
	if err != nil || !replayed || again.ID != o.ID {
		t.Fatalf("replay: %+v %v %v", again, replayed, err)
	}
	if _, _, err := d.s.RequestOptimization(d.ctx, OptimizeInput{LegalEntityID: d.le, TerritoryID: d.territory, Date: optDate, TimeZone: optZone}, time.Now(), "snap-1", "other"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("reused key: %v", err)
	}
}

func TestStore_OptimizationNeedsTechniciansAndOpenWork(t *testing.T) {
	d := newDispatch(t, 22)
	d.located(t, 60, 41.9, -87.65, nil)
	_, _, err := d.s.RequestOptimization(d.ctx, OptimizeInput{LegalEntityID: d.le, TerritoryID: d.territory, Date: optDate, TimeZone: optZone}, time.Now(), "", "")
	if problemCode(err) != CodeOptimizationEmpty {
		t.Fatalf("no technicians: %v", err)
	}
	d.homed(t, "ANA")
	if _, _, err := d.s.RequestOptimization(d.ctx, OptimizeInput{LegalEntityID: d.le, TerritoryID: ids.New(), Date: optDate, TimeZone: optZone}, time.Now(), "", ""); problemCode(err) != CodeOptimizationEmpty {
		t.Fatalf("other territory: %v", err)
	}
	if _, _, err := d.s.RequestOptimization(d.ctx, OptimizeInput{LegalEntityID: d.le, TerritoryID: d.territory, Date: "2026-13-01", TimeZone: optZone}, time.Now(), "", ""); err == nil {
		t.Fatal("a date that does not exist was accepted")
	}
}

func TestStore_OptimizationBacksOffAndParks(t *testing.T) {
	d := newDispatch(t, 23)
	d.homed(t, "ANA")
	d.located(t, 60, 41.9, -87.65, nil)
	o := d.request(t, "")
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	for attempt, wait := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute} {
		var err error
		if o, err = d.s.RecordOptimizationFailure(d.ctx, o.ID, o.AttemptCount, "connection refused", false, now); err != nil {
			t.Fatal(err)
		}
		if o.Status != OptPending || o.AttemptCount != attempt+1 || !o.NextAttemptAt.Equal(now.Add(wait)) || o.LastError == nil || *o.LastError != "connection refused" {
			t.Fatalf("failure %d: %+v, want next attempt in %s", attempt+1, o, wait)
		}
	}
	o, err := d.s.RecordOptimizationFailure(d.ctx, o.ID, o.AttemptCount, "connection refused", false, now)
	if err != nil || o.Status != OptParked || o.AttemptCount != MaxOptimizationAttempts {
		t.Fatalf("fifth failure: %+v %v", o, err)
	}
	if after, err := d.s.RecordOptimizationFailure(d.ctx, o.ID, o.AttemptCount, "again", false, now); err != nil || after.AttemptCount != o.AttemptCount {
		t.Fatalf("a parked optimization counts no more failures: %+v %v", after, err)
	}

	retried, err := d.s.RetryOptimization(d.ctx, o.ID, now)
	if err != nil || retried.Status != OptPending || retried.AttemptCount != 0 || retried.Generation != 1 || !retried.NextAttemptAt.Equal(now) {
		t.Fatalf("retry: %+v %v", retried, err)
	}
	if _, err := d.s.RetryOptimization(d.ctx, o.ID, now); problemCode(err) != CodeOptimizationState {
		t.Fatalf("retry of a pending optimization: %v", err)
	}
	if stale, err := d.s.RecordOptimizationFailure(d.ctx, o.ID, 3, "late answer", false, now); err != nil || stale.AttemptCount != 0 || (stale.LastError != nil && *stale.LastError == "late answer") {
		t.Fatalf("a failure of an earlier attempt was counted: %+v %v", stale, err)
	}
	refused, err := d.s.RecordOptimizationFailure(d.ctx, o.ID, retried.AttemptCount, "422 pinned assignments are infeasible", true, now)
	if err != nil || refused.Status != OptParked || refused.AttemptCount != 1 {
		t.Fatalf("permanent refusal: %+v %v", refused, err)
	}
}

func TestStore_AcceptWritesThePlanAndReportsWhatItCannot(t *testing.T) {
	d := newDispatch(t, 24)
	ana := d.homed(t, "ANA")
	first := d.located(t, 60, 41.9, -87.65, nil)
	second := d.located(t, 60, 41.92, -87.6, nil)
	tooEarly := d.located(t, 60, 41.9, -87.6, nil)
	taken := d.located(t, 60, 41.91, -87.61, nil)
	held := d.assign(t, d.located(t, 60, 41.881, -87.631, nil), ana, d.at(26, 14, 0))

	o := d.request(t, "")
	if o.Pinned != 1 || o.WorkOrders != 4 {
		t.Fatalf("snapshot %+v", o)
	}
	manual := d.assign(t, taken, ana, d.at(26, 15, 30))
	plan := []optimizer.Assignment{
		{WorkOrderID: held.ID, TechnicianID: ana.ID, Start: 840, End: 900, Sequence: 3},
		{WorkOrderID: second.ID, TechnicianID: ana.ID, Start: 660, End: 720, Sequence: 2},
		{WorkOrderID: tooEarly.ID, TechnicianID: ana.ID, Start: 360, End: 420, Sequence: 0},
		{WorkOrderID: first.ID, TechnicianID: ana.ID, Start: 540, End: 600, Sequence: 1},
		{WorkOrderID: taken.ID, TechnicianID: ana.ID, Start: 750, End: 810, Sequence: 4},
	}
	if _, _, err := d.s.AcceptOptimization(d.ctx, o.ID, time.Now()); problemCode(err) != CodeOptimizationState {
		t.Fatalf("accept before a plan exists: %v", err)
	}
	d.succeeded(t, o, plan...)

	done, replayed, err := d.s.AcceptOptimization(d.ctx, o.ID, time.Now())
	if err != nil || replayed || done.Status != OptAccepted || done.Outcome == nil || done.AcceptedBy == nil || done.AcceptedAt == nil {
		t.Fatalf("accept: %+v %v %v", done, replayed, err)
	}
	var applied []string
	for _, a := range done.Outcome.Applied {
		applied = append(applied, a.WorkOrderID)
	}
	if !slices.Equal(applied, []string{first.ID, second.ID}) {
		t.Fatalf("applied %v, want the two visits inside the shift in route order", applied)
	}
	rejected := map[string]string{}
	for _, r := range done.Outcome.Rejected {
		rejected[r.WorkOrderID] = r.Code
	}
	if len(rejected) != 2 || rejected[tooEarly.ID] != domain.CodeDspShift || rejected[taken.ID] != CodeWOInvalid {
		t.Fatalf("rejected %v", rejected)
	}
	for _, id := range applied {
		wo, err := d.s.GetWorkOrder(d.ctx, id)
		if err != nil || wo.Status != "SCHEDULED" || wo.TechnicianID == nil || *wo.TechnicianID != ana.ID || wo.AssignmentPinned {
			t.Fatalf("applied order %+v %v", wo, err)
		}
		if want := []string{"dev.passage.service.assignment.changed.v1"}; !slices.Equal(d.eventTypes(t, id), want) {
			t.Fatalf("events of %s: %v", id, d.eventTypes(t, id))
		}
	}
	if wo, _ := d.s.GetWorkOrder(d.ctx, first.ID); !wo.AssignedStart.Equal(d.at(26, 9, 0)) || !wo.AssignedEnd.Equal(d.at(26, 10, 0)) {
		t.Fatalf("first visit %v to %v", wo.AssignedStart, wo.AssignedEnd)
	}
	if wo, _ := d.s.GetWorkOrder(d.ctx, tooEarly.ID); wo.Status != "UNSCHEDULED" {
		t.Fatalf("a rejected visit stays open: %+v", wo)
	}
	if wo, _ := d.s.GetWorkOrder(d.ctx, held.ID); wo.Version != held.Version {
		t.Fatalf("the held visit was rewritten: version %d, was %d", wo.Version, held.Version)
	}
	if wo, _ := d.s.GetWorkOrder(d.ctx, taken.ID); wo.Version != manual.Version || !wo.AssignedStart.Equal(d.at(26, 15, 30)) {
		t.Fatalf("the dispatcher's own assignment was overwritten: %+v", wo)
	}

	again, replayed, err := d.s.AcceptOptimization(d.ctx, o.ID, time.Now())
	if err != nil || !replayed || len(again.Outcome.Applied) != 2 {
		t.Fatalf("second accept: %+v %v %v", again, replayed, err)
	}

	other := principalCtx(testkit.TenantID(25))
	if _, err := d.s.GetOptimization(other, o.ID); problemCode(err) != CodeOptimizationUnknown {
		t.Fatalf("another tenant read the optimization: %v", err)
	}
	page, err := d.s.ListOptimizations(d.ctx, ListFilter{Territory: d.territory, Status: OptAccepted, Limit: 10})
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != o.ID {
		t.Fatalf("list: %+v %v", page, err)
	}
}

func TestStore_OptimizationReportsAStaleHeldVisitAndPlansTheRest(t *testing.T) {
	d := newDispatch(t, 26)
	ana, bob := d.homed(t, "ANA"), d.homed(t, "BOB")
	stale := d.assign(t, d.located(t, 60, 41.881, -87.631, nil), ana, d.at(26, 14, 0))
	kept := d.assign(t, d.located(t, 60, 41.882, -87.632, nil), bob, d.at(26, 14, 0))
	d.located(t, 60, 41.9, -87.65, nil)
	err := dbx.WithTenantTx(d.ctx, d.s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), `update service_technician set skills = '{ELECTRICAL}' where tenant_id = $1 and id = $2`, tenantOf(d.ctx), ana.ID)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	o := d.request(t, "stale-1")
	got := skippedReasons(o.Skipped)
	if got[ana.ID] != "HELD_VISIT_STALE" || got[stale.ID] != "HELD_VISIT_STALE" || len(got) != 2 {
		t.Fatalf("skipped %+v", o.Skipped)
	}
	req, err := d.s.OptimizationRequest(d.ctx, o.ID)
	if err != nil || len(req.Technicians) != 1 || req.Technicians[0].ID != bob.ID || len(req.Pinned) != 1 || req.Pinned[0].WorkOrderID != kept.ID {
		t.Fatalf("request %+v %v", req, err)
	}
}
