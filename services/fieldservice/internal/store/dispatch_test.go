package store

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
)

// dispatchFixture is a migrated store with one territory and a Monday 2026-10-26
// (America/Chicago) to plan.
type dispatchFixture struct {
	lifecycle
	territory string
}

func newDispatch(t *testing.T, tenantN int) dispatchFixture {
	t.Helper()
	return dispatchFixture{lifecycle: newLifecycle(t, tenantN), territory: ids.New()}
}

func (d dispatchFixture) technician(t *testing.T, code string, skills ...string) Technician {
	t.Helper()
	tech, _, err := d.s.CreateTechnician(d.ctx, Technician{Code: code, Name: "Tech " + code, LegalEntityID: d.le, TerritoryIDs: []string{d.territory},
		Skills: skills, TimeZone: "America/Chicago", ShiftDays: []int{1, 2, 3, 4, 5}, ShiftStartMinute: 480, ShiftEndMinute: 1020,
		MaxDailyMinutes: 480, Active: true}, "key-"+code, "hash-"+code)
	if err != nil {
		t.Fatal(err)
	}
	return tech
}

func (d dispatchFixture) order(t *testing.T, minutes int, skills []string, caseID *string) WorkOrder {
	t.Helper()
	wo, _, err := d.s.CreateWorkOrder(d.ctx, WorkOrder{CaseID: caseID, CustomerPartyID: d.customer, LegalEntityID: d.le, TerritoryID: d.territory,
		EstDurationMinutes: minutes, Currency: "USD", RequiredSkills: skills}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return wo
}

func (d dispatchFixture) assign(t *testing.T, wo WorkOrder, tech Technician, start time.Time) WorkOrder {
	t.Helper()
	out, err := d.s.AssignWorkOrder(d.ctx, wo.ID, wo.Version, AssignInput{TechnicianID: tech.ID, Start: start.UTC()}, time.Now())
	if err != nil {
		t.Fatalf("assign %s: %v", wo.Number, err)
	}
	return out
}

func violationCodes(err error) map[string]string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Errors
	}
	return nil
}

// FR-45.7, BR-45.3 against Postgres as passage_app: assignment enforces the hard
// constraints and reports each broken one, a clean assignment makes the order SCHEDULED
// with its end from the estimate, a move keeps it SCHEDULED and an unassign releases it.
func TestStore_AssignmentEnforcesHardConstraints(t *testing.T) {
	d := newDispatch(t, 11)
	tech := d.technician(t, "ANA", "HVAC", "ELECTRICAL")
	first := d.order(t, 120, []string{"HVAC"}, nil)

	got := d.assign(t, first, tech, d.at(26, 9, 0))
	if got.Status != "SCHEDULED" || got.TechnicianID == nil || *got.TechnicianID != tech.ID || got.Version != first.Version+1 {
		t.Fatalf("assigned order %+v", got)
	}
	if want := d.at(26, 11, 0); !got.AssignedEnd.Equal(want) {
		t.Fatalf("end %s, want %s", got.AssignedEnd, want)
	}

	second := d.order(t, 60, []string{"HVAC", "WELDING"}, nil)
	_, err := d.s.AssignWorkOrder(d.ctx, second.ID, second.Version, AssignInput{TechnicianID: tech.ID, Start: d.at(26, 10, 0).UTC()}, time.Now())
	errs := violationCodes(err)
	if problemCode(err) != domain.CodeDspSkill || errs[domain.CodeDspSkill] == "" || errs[domain.CodeDspOverlap] == "" {
		t.Fatalf("skill and overlap: %v (%v)", err, errs)
	}
	// The failed attempt changed nothing.
	if again, err := d.s.GetWorkOrder(d.ctx, second.ID); err != nil || again.Status != "UNSCHEDULED" || again.Version != second.Version {
		t.Fatalf("after a refused assignment: %+v %v", again, err)
	}

	plain := d.order(t, 60, nil, nil)
	if _, err := d.s.AssignWorkOrder(d.ctx, plain.ID, plain.Version, AssignInput{TechnicianID: tech.ID, Start: d.at(26, 11, 3).UTC()}, time.Now()); problemCode(err) != domain.CodeDspOverlap {
		t.Fatalf("inside the buffer: %v", err)
	}
	moved := d.assign(t, plain, tech, d.at(26, 11, 5))
	// Moving the first order does not conflict with its own old slot.
	first = d.assign(t, got, tech, d.at(26, 8, 30))
	if first.Version != got.Version+1 || !first.AssignedStart.Equal(d.at(26, 8, 30)) {
		t.Fatalf("moved order %+v", first)
	}
	if _, err := d.s.AssignWorkOrder(d.ctx, moved.ID, moved.Version, AssignInput{TechnicianID: tech.ID, Start: d.at(26, 10, 0).UTC()}, time.Now()); problemCode(err) != domain.CodeDspOverlap {
		t.Fatalf("moving onto the other order: %v", err)
	}
	if _, err := d.s.AssignWorkOrder(d.ctx, first.ID, first.Version-1, AssignInput{TechnicianID: tech.ID, Start: d.at(26, 14, 0).UTC()}, time.Now()); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale version: %v", err)
	}

	free, err := d.s.UnassignWorkOrder(d.ctx, first.ID, first.Version, time.Now())
	if err != nil || free.Status != "UNSCHEDULED" || free.TechnicianID != nil || free.AssignedStart != nil {
		t.Fatalf("unassigned %+v %v", free, err)
	}
	want := []string{"dev.passage.service.assignment.changed.v1", "dev.passage.service.assignment.changed.v1", "dev.passage.service.assignment.changed.v1"}
	if got := d.eventTypes(t, first.ID); !slices.Equal(got, want) {
		t.Fatalf("outbox of the first order %v, want assign, move and unassign", got)
	}
	// The slot it left is free for the other order again.
	d.assign(t, moved, tech, d.at(26, 9, 0))
}

// Two dispatchers booking the same technician at the same time: the technician row
// lock lets exactly one of them in.
func TestStore_ConcurrentAssignmentsCannotOverlap(t *testing.T) {
	d := newDispatch(t, 12)
	tech := d.technician(t, "BOB", "HVAC")
	var orders []WorkOrder
	for i := 0; i < 4; i++ {
		orders = append(orders, d.order(t, 90, nil, nil))
	}
	var wg sync.WaitGroup
	results := make([]error, len(orders))
	for i, wo := range orders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = d.s.AssignWorkOrder(d.ctx, wo.ID, wo.Version, AssignInput{TechnicianID: tech.ID, Start: d.at(26, 9, 0).UTC()}, time.Now())
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range results {
		switch {
		case err == nil:
			won++
		case problemCode(err) != domain.CodeDspOverlap:
			t.Fatalf("unexpected failure %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d of 4 simultaneous assignments won, want exactly 1", won)
	}
}

// Publishing dispatches the day's SCHEDULED orders in route order, stops the response
// clock of their cases, writes one event and replays under its key; what is dispatched
// can no longer be moved.
func TestStore_PublishDispatchesTheTerritoryDay(t *testing.T) {
	d := newDispatch(t, 13)
	ana, bob := d.technician(t, "ANA", "HVAC"), d.technician(t, "BOB", "HVAC")
	c := d.open(t, time.Now().Add(-10*time.Minute))
	late := d.assign(t, d.order(t, 60, nil, &c.ID), ana, d.at(26, 13, 0))
	early := d.assign(t, d.order(t, 60, nil, nil), ana, d.at(26, 9, 0))
	other := d.assign(t, d.order(t, 60, nil, nil), bob, d.at(26, 9, 0))
	nextDay := d.assign(t, d.order(t, 60, nil, nil), bob, d.at(27, 9, 0))
	d.order(t, 60, nil, nil) // unscheduled: stays out

	in := PublishInput{LegalEntityID: d.le, TerritoryID: d.territory, Date: "2026-10-26", TimeZone: "America/Chicago"}
	run, replayed, err := d.s.PublishDispatch(d.ctx, in, time.Now(), "pub-1", "h1")
	if err != nil || replayed {
		t.Fatalf("publish: %v replayed=%v", err, replayed)
	}
	if run.DispatchedCount != 3 || len(run.Assignments) != 3 {
		t.Fatalf("run %+v, want 3 orders", run)
	}
	seq := map[string]int{}
	for _, a := range run.Assignments {
		seq[a.WorkOrderID] = a.Sequence
	}
	if seq[early.ID] != 1 || seq[late.ID] != 2 || seq[other.ID] != 1 {
		t.Fatalf("route order %v: early %s, late %s, other %s", seq, early.ID, late.ID, other.ID)
	}
	for _, id := range []string{early.ID, late.ID, other.ID} {
		if wo, _ := d.s.GetWorkOrder(d.ctx, id); wo.Status != "DISPATCHED" {
			t.Fatalf("order %s is %s", id, wo.Status)
		}
	}
	if wo, _ := d.s.GetWorkOrder(d.ctx, nextDay.ID); wo.Status != "SCHEDULED" {
		t.Fatalf("the next day's order is %s, want SCHEDULED", wo.Status)
	}
	if got, err := d.s.GetCase(d.ctx, c.ID); err != nil || got.RespondedAt == nil {
		t.Fatalf("case after dispatch: %+v %v", got, err)
	}
	if got := d.eventTypes(t, run.ID); !slices.Equal(got, []string{"dev.passage.service.dispatch.published.v1"}) {
		t.Fatalf("outbox %v", got)
	}

	again, replayed, err := d.s.PublishDispatch(d.ctx, in, time.Now(), "pub-1", "h1")
	if err != nil || !replayed || again.ID != run.ID {
		t.Fatalf("replay: %+v %v replayed=%v", again, err, replayed)
	}
	if _, _, err := d.s.PublishDispatch(d.ctx, in, time.Now(), "pub-1", "different"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("same key, other body: %v", err)
	}
	if _, _, err := d.s.PublishDispatch(d.ctx, in, time.Now(), "pub-2", "h2"); problemCode(err) != CodeDispatchEmpty {
		t.Fatalf("nothing left to dispatch: %v", err)
	}

	dispatched, _ := d.s.GetWorkOrder(d.ctx, early.ID)
	if _, err := d.s.AssignWorkOrder(d.ctx, early.ID, dispatched.Version, AssignInput{TechnicianID: bob.ID, Start: d.at(26, 15, 0).UTC()}, time.Now()); problemCode(err) != CodeWOInvalid {
		t.Fatalf("moving a dispatched order: %v", err)
	}
	if _, err := d.s.UnassignWorkOrder(d.ctx, early.ID, dispatched.Version, time.Now()); problemCode(err) != CodeWOInvalid {
		t.Fatalf("unassigning a dispatched order: %v", err)
	}
	if listed, err := d.s.ListDispatchRuns(d.ctx, ListFilter{Territory: d.territory, Limit: 10}); err != nil || len(listed.Items) != 1 {
		t.Fatalf("runs %+v %v", listed, err)
	}
	if got, err := d.s.GetDispatchRun(d.ctx, run.ID); err != nil || len(got.Assignments) != 3 {
		t.Fatalf("run read %+v %v", got, err)
	}
	if _, err := d.s.GetDispatchRun(d.ctx, ids.New()); problemCode(err) != CodeDispatchRunUnknown {
		t.Fatalf("unknown run: %v", err)
	}

	// A technician with orders ahead cannot be deactivated; one without can.
	upd := ana
	upd.Active = false
	if _, err := d.s.UpdateTechnician(d.ctx, ana.ID, ana.Version, upd); problemCode(err) != CodeTechnicianBusy {
		t.Fatalf("deactivate with orders ahead: %v", err)
	}
	idle := d.technician(t, "CY", "HVAC")
	idle.Active = false
	if got, err := d.s.UpdateTechnician(d.ctx, idle.ID, idle.Version, idle); err != nil || got.Active || got.Version != idle.Version+1 {
		t.Fatalf("deactivate an idle technician: %+v %v", got, err)
	}
	if _, err := d.s.AssignWorkOrder(d.ctx, nextDay.ID, nextDay.Version, AssignInput{TechnicianID: idle.ID, Start: d.at(28, 9, 0).UTC()}, time.Now()); problemCode(err) != domain.CodeDspEligible {
		t.Fatalf("assign to an inactive technician: %v", err)
	}
}

// The board lists the territory's active technicians with the local day's assignments
// and booked minutes, and the unscheduled orders; scheduling data is only editable
// before an order is scheduled.
func TestStore_BoardAndSchedulingData(t *testing.T) {
	d := newDispatch(t, 14)
	tech := d.technician(t, "DEE", "HVAC")
	d.technician(t, "EDD", "HVAC") // idle: still on the board
	inactive := d.technician(t, "OLD")
	inactive.Active = false
	if _, err := d.s.UpdateTechnician(d.ctx, inactive.ID, inactive.Version, inactive); err != nil {
		t.Fatal(err)
	}
	wo := d.order(t, 90, nil, nil)
	lat, lon := 41.8781, -87.6298
	ws, we := d.at(26, 8, 0).UTC(), d.at(26, 12, 0).UTC()
	wo, err := d.s.UpdateWorkOrderScheduling(d.ctx, wo.ID, wo.Version, Scheduling{RequiredSkills: []string{"HVAC"}, WindowStart: &ws, WindowEnd: &we,
		Address: "1 Main St, Chicago", Latitude: &lat, Longitude: &lon, EstDurationMinutes: 90})
	if err != nil || wo.Address != "1 Main St, Chicago" || wo.Latitude == nil || *wo.Latitude != lat || !slices.Equal(wo.RequiredSkills, []string{"HVAC"}) {
		t.Fatalf("scheduling update %+v %v", wo, err)
	}
	other := d.order(t, 30, nil, nil)

	board, err := d.s.Board(d.ctx, d.territory, "2026-10-26", "America/Chicago")
	if err != nil || len(board.Technicians) != 2 || len(board.Unscheduled) != 2 {
		t.Fatalf("board %+v %v", board, err)
	}
	// The visit must stay in the customer's window: 13:00 is outside it, 10:00 inside.
	if _, err := d.s.AssignWorkOrder(d.ctx, wo.ID, wo.Version, AssignInput{TechnicianID: tech.ID, Start: d.at(26, 13, 0).UTC()}, time.Now()); problemCode(err) != domain.CodeDspWindow {
		t.Fatalf("outside the window: %v", err)
	}
	wo = d.assign(t, wo, tech, d.at(26, 10, 0))
	if _, err := d.s.UpdateWorkOrderScheduling(d.ctx, wo.ID, wo.Version, Scheduling{EstDurationMinutes: 30}); problemCode(err) != CodeWOInvalid {
		t.Fatalf("editing a scheduled order: %v", err)
	}
	if _, err := d.s.UpdateWorkOrderScheduling(d.ctx, other.ID, other.Version+4, Scheduling{EstDurationMinutes: 30}); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale scheduling update: %v", err)
	}

	board, err = d.s.Board(d.ctx, d.territory, "2026-10-26", "America/Chicago")
	if err != nil || len(board.Unscheduled) != 1 || board.Unscheduled[0].ID != other.ID {
		t.Fatalf("board after the assignment %+v %v", board, err)
	}
	var row BoardTechnician
	for _, r := range board.Technicians {
		if r.Technician.ID == tech.ID {
			row = r
		}
	}
	if len(row.Assignments) != 1 || row.Assignments[0].ID != wo.ID || row.BookedMinutes != 90 {
		t.Fatalf("technician row %+v", row)
	}
	if next, err := d.s.Board(d.ctx, d.territory, "2026-10-27", "America/Chicago"); err != nil || len(next.Technicians[0].Assignments)+len(next.Technicians[1].Assignments) != 0 {
		t.Fatalf("the next day's board %+v %v", next, err)
	}

	// A follow-up keeps the skills and the address of the visit it follows.
	for _, to := range []domain.WOStatus{domain.WODispatched, domain.WOTraveling, domain.WOInProgress, domain.WOCompleted} {
		if wo, err = d.s.moveWorkOrder(d.ctx, wo.ID, wo.Version, to); err != nil {
			t.Fatal(err)
		}
	}
	child, _, err := d.s.CreateFollowUp(d.ctx, wo.ID, wo.Version, "fu", "fu")
	if err != nil || child.Address != wo.Address || !slices.Equal(child.RequiredSkills, wo.RequiredSkills) || child.TechnicianID != nil || child.WindowStart != nil {
		t.Fatalf("follow-up %+v %v", child, err)
	}
}

// Technicians are unique per code, replay under their key, and are invisible to other
// tenants and to callers scoped to another legal entity.
func TestStore_TechnicianRulesAndScope(t *testing.T) {
	d := newDispatch(t, 15)
	tech := d.technician(t, "FAY", "HVAC")
	if _, _, err := d.s.CreateTechnician(d.ctx, Technician{Code: "FAY", Name: "Again", LegalEntityID: d.le, TerritoryIDs: []string{d.territory}, TimeZone: "UTC",
		ShiftDays: []int{1}, ShiftStartMinute: 0, ShiftEndMinute: 60, MaxDailyMinutes: 60, Active: true}, "other-key", "other"); problemCode(err) != CodeTechnicianDuplicate {
		t.Fatalf("duplicate code: %v", err)
	}
	again, replayed, err := d.s.CreateTechnician(d.ctx, Technician{}, "key-FAY", "hash-FAY")
	if err != nil || !replayed || again.ID != tech.ID {
		t.Fatalf("replay: %+v %v", again, err)
	}
	if _, _, err := d.s.CreateTechnician(d.ctx, Technician{}, "key-FAY", "changed"); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("reuse: %v", err)
	}
	moved := tech
	moved.LegalEntityID = ids.New()
	if _, err := d.s.UpdateTechnician(d.ctx, tech.ID, tech.Version, moved); problemCode(err) != CodeTechnicianEntity {
		t.Fatalf("changing the legal entity: %v", err)
	}
	if _, err := d.s.UpdateTechnician(d.ctx, tech.ID, tech.Version+1, tech); problemCode(err) != CodeVersionConflict {
		t.Fatalf("stale update: %v", err)
	}
	if page, err := d.s.ListTechnicians(d.ctx, ListFilter{Territory: d.territory, Active: "true", Limit: 10}); err != nil || len(page.Items) != 1 {
		t.Fatalf("list %+v %v", page, err)
	}
	if page, err := d.s.ListTechnicians(d.ctx, ListFilter{Territory: ids.New(), Limit: 10}); err != nil || len(page.Items) != 0 {
		t.Fatalf("list of another territory %+v %v", page, err)
	}

	for name, ctx := range map[string]context.Context{
		"another tenant":       principalCtx(ids.New()),
		"another legal entity": tenant.With(context.Background(), tenant.Principal{TenantID: tenantOf(d.ctx), LegalEntityID: ids.New(), Duties: map[string]struct{}{}}),
	} {
		if _, err := d.s.GetTechnician(ctx, tech.ID); problemCode(err) != CodeTechnicianUnknown {
			t.Errorf("%s reads the technician: %v", name, err)
		}
		if page, err := d.s.ListTechnicians(ctx, ListFilter{Limit: 10}); err != nil || len(page.Items) != 0 {
			t.Errorf("%s lists %+v %v", name, page, err)
		}
	}
	if _, err := d.s.GetTechnician(d.ctx, ids.New()); problemCode(err) != CodeTechnicianUnknown {
		t.Fatalf("unknown technician: %v", err)
	}
}

// An edit of a technician that would leave a SCHEDULED visit in breach of the hard
// constraints (a dropped skill, a shorter shift) is refused with the visit named; an
// edit the visits still fit is saved.
func TestStore_TechnicianEditKeepsScheduledVisitsFeasible(t *testing.T) {
	d := newDispatch(t, 16)
	tech := d.technician(t, "GUS", "HVAC")
	wo := d.assign(t, d.order(t, 120, []string{"HVAC"}, nil), tech, d.at(26, 9, 0))

	noSkill := tech
	noSkill.Skills = []string{"ELECTRICAL"}
	_, err := d.s.UpdateTechnician(d.ctx, tech.ID, tech.Version, noSkill)
	if problemCode(err) != CodeTechnicianBreaks || !strings.Contains(violationCodes(err)[wo.Number], domain.CodeDspSkill) {
		t.Fatalf("dropping the visit's skill: %v (%v)", err, violationCodes(err))
	}
	shorter := tech
	shorter.ShiftEndMinute = 600 // 10:00, before the visit ends at 11:00
	if _, err := d.s.UpdateTechnician(d.ctx, tech.ID, tech.Version, shorter); !strings.Contains(violationCodes(err)[wo.Number], domain.CodeDspShift) {
		t.Fatalf("shortening the shift: %v (%v)", err, violationCodes(err))
	}
	if again, err := d.s.GetTechnician(d.ctx, tech.ID); err != nil || again.Version != tech.Version || !slices.Equal(again.Skills, tech.Skills) {
		t.Fatalf("a refused edit changed the technician: %+v %v", again, err)
	}

	renamed := tech
	renamed.Name = "Gus Renamed"
	renamed.Skills = []string{"ELECTRICAL", "HVAC"}
	saved, err := d.s.UpdateTechnician(d.ctx, tech.ID, tech.Version, renamed)
	if err != nil || saved.Name != "Gus Renamed" || saved.Version != tech.Version+1 {
		t.Fatalf("an edit the visit still fits: %+v %v", saved, err)
	}
}
