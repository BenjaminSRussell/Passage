package domain

import (
	"strings"
	"testing"
)

func overrideMoveInput(at0 Placement, booked []Busy) MoveInput {
	r := res("R", "R", true, weekdayShift(8*60, 16*60))
	op := Operation{ID: "op", OrderID: "o", OperNumber: 10, Minutes: 120, Candidates: []string{"R"}}
	return MoveInput{Resources: []Resource{r}, Start: monday, HorizonDays: 14, Order: []Operation{op}, Booked: booked,
		Current: map[string]Placement{"op": at0}, Moved: "op", ResourceID: "R"}
}

func TestPlanMove_OverrideCommitsOutsideOpenCapacityAsOneSpan(t *testing.T) {
	op := Operation{ID: "op", OrderID: "o", OperNumber: 10, Minutes: 120, Candidates: []string{"R"}}
	in := overrideMoveInput(pl(op, "R", at(0, 8, 0), 0), nil)
	in.At = at(0, 16, 30) // after the 16:00 close
	plan := PlanMove(in)
	if plan.Conflict == nil || plan.Conflict.Code != CodeOutsideCapacity {
		t.Fatalf("without override: %+v", plan)
	}
	in.Override = true
	plan = PlanMove(in)
	if plan.Conflict != nil || len(plan.Changes) != 1 {
		t.Fatalf("with override: %+v", plan)
	}
	c := plan.Changes[0]
	if !c.Start.Equal(at(0, 16, 30)) || !c.End.Equal(at(0, 18, 30)) || len(c.Segments) != 1 {
		t.Fatalf("placement = %v..%v segments %d, want one span 16:30-18:30", c.Start, c.End, len(c.Segments))
	}
	if len(plan.Overridden) != 1 || plan.Overridden[0].Code != CodeOutsideCapacity {
		t.Fatalf("overridden = %+v", plan.Overridden)
	}
	// A weekend start is wholly closed time.
	in.At = at(5, 9, 0)
	if plan = PlanMove(in); plan.Conflict != nil || !plan.Changes[0].End.Equal(at(5, 11, 0)) {
		t.Fatalf("weekend override: %+v", plan)
	}
}

func TestPlanMove_OverrideCoversABlockItDoesNotMatchButNotAnOverlap(t *testing.T) {
	r := res("R", "R", true, weekdayShift(8*60, 16*60))
	r.Blocks = []CapacityBlock{{ID: "white", ResourceID: "R", Windows: []Interval{{Start: at(0, 8, 0), End: at(0, 12, 0)}},
		Condition: []Predicate{{Attribute: "color", Op: "=", Value: "white"}}}}
	red := Operation{ID: "red", OrderID: "o", OperNumber: 10, Minutes: 60, Attributes: map[string]string{"color": "red"}, Candidates: []string{"R"}}
	in := MoveInput{Resources: []Resource{r}, Start: monday, HorizonDays: 14, Order: []Operation{red}, Override: true,
		Current: map[string]Placement{"red": pl(red, "R", at(0, 12, 0), 0)}, Moved: "red", ResourceID: "R", At: at(0, 9, 0)}
	plan := PlanMove(in)
	if plan.Conflict != nil || len(plan.Overridden) != 1 || !strings.Contains(plan.Overridden[0].Detail, "block") {
		t.Fatalf("block override: %+v", plan)
	}
	// Another job already holds 09:30-10:30: double booking is never overridden.
	in.Booked = []Busy{{ResourceID: "R", Start: at(0, 9, 30), End: at(0, 10, 30), JobID: "other"}}
	if plan = PlanMove(in); plan.Conflict == nil || plan.Conflict.Code != CodeOverlap {
		t.Fatalf("overlap override: %+v", plan)
	}
}

func TestPlanMove_OverrideNeverCoversAFirmedJobOrANonCandidate(t *testing.T) {
	op := Operation{ID: "op", OrderID: "o", OperNumber: 10, Minutes: 60, Candidates: []string{"R"}}
	in := overrideMoveInput(pl(op, "R", at(0, 8, 0), 0), nil)
	in.Override = true
	in.At = at(1, 9, 0)
	in.Firmed = map[string]bool{"op": true}
	if plan := PlanMove(in); plan.Conflict == nil || plan.Conflict.Code != CodeFirmed {
		t.Fatalf("firmed: %+v", plan)
	}
	in.Firmed = nil
	in.ResourceID = "other"
	in.Resources = append(in.Resources, res("other", "O", true, weekdayShift(8*60, 16*60)))
	if plan := PlanMove(in); plan.Conflict == nil || plan.Conflict.Code != CodeNotCandidate {
		t.Fatalf("non-candidate: %+v", plan)
	}
}

func TestPlanMove_WithoutViolationsOverrideListsNothing(t *testing.T) {
	op := Operation{ID: "op", OrderID: "o", OperNumber: 10, Minutes: 120, Candidates: []string{"R"}}
	in := overrideMoveInput(pl(op, "R", at(0, 8, 0), 0), nil)
	in.Override, in.At = true, at(1, 9, 0)
	plan := PlanMove(in)
	if plan.Conflict != nil || len(plan.Overridden) != 0 {
		t.Fatalf("plan = %+v", plan)
	}
}

// A secondary that is closed for the slot is overridden; one another job holds
// would be double booked, so it is refused even with the override.
func TestPlanMove_OverrideCoversAClosedSecondaryButNotABookedOne(t *testing.T) {
	m := res("M", "M", true, weekdayShift(8*60, 18*60))
	k := res("K", "K", true, weekdayShift(8*60, 16*60))
	op := secOp("a", "o1", 10, 60, "K")
	current := map[string]Placement{"a": plS(op, "M", at(0, 8, 0))}
	booked := append(busyOf(op, current["a"]), secondaryBusy(current["a"])...)
	in := MoveInput{Resources: []Resource{m, k}, Start: monday, HorizonDays: 7, Booked: booked, Order: []Operation{op},
		Current: current, Moved: "a", ResourceID: "M", At: at(1, 16, 30), Override: true}
	plan := PlanMove(in)
	if plan.Conflict != nil || len(plan.Overridden) != 1 || plan.Overridden[0].Code != CodeSecondaryBusy {
		t.Fatalf("closed secondary: %+v", plan)
	}
	in.At = at(1, 10, 0)
	in.Booked = append(in.Booked, Busy{ResourceID: "K", Start: at(1, 9, 30), End: at(1, 10, 30), JobID: "other"})
	plan = PlanMove(in)
	if plan.Conflict == nil || plan.Conflict.Code != CodeSecondaryBusy || !strings.Contains(plan.Conflict.Detail, "other") {
		t.Fatalf("booked secondary: %+v, want SCHED-0005 refused", plan)
	}
	// Closed and booked at once: the booking still refuses.
	in.At = at(1, 15, 30)
	in.Booked = append(in.Booked, Busy{ResourceID: "K", Start: at(1, 15, 0), End: at(1, 16, 0), JobID: "late"})
	if plan = PlanMove(in); plan.Conflict == nil || !strings.Contains(plan.Conflict.Detail, "late") {
		t.Fatalf("closed and booked secondary: %+v", plan)
	}
}

// A start beyond the planning horizon is not forced: nothing there says the
// time is open, so the move keeps its refusal.
func TestPlanMove_OverrideStopsAtThePlanningHorizon(t *testing.T) {
	op := Operation{ID: "op", OrderID: "o", OperNumber: 10, Minutes: 120, Candidates: []string{"R"}}
	in := overrideMoveInput(pl(op, "R", at(0, 8, 0), 0), nil)
	in.Override, in.At = true, at(20, 9, 0) // HorizonDays is 14
	if plan := PlanMove(in); plan.Conflict == nil || plan.Conflict.Code != CodeOutsideCapacity {
		t.Fatalf("beyond the horizon: %+v", plan)
	}
}

// A start inside a block the job does not match whose span also runs past the
// close lists both reasons.
func TestPlanMove_OverrideListsABlockStartAndTheClosedTimeAfterIt(t *testing.T) {
	r := res("R", "R", true, weekdayShift(8*60, 16*60))
	r.Blocks = []CapacityBlock{{ID: "white", ResourceID: "R", Windows: []Interval{{Start: at(0, 14, 0), End: at(0, 16, 0)}},
		Condition: []Predicate{{Attribute: "color", Op: "=", Value: "white"}}}}
	red := Operation{ID: "red", OrderID: "o", OperNumber: 10, Minutes: 120, Attributes: map[string]string{"color": "red"}, Candidates: []string{"R"}}
	in := MoveInput{Resources: []Resource{r}, Start: monday, HorizonDays: 14, Order: []Operation{red}, Override: true,
		Current: map[string]Placement{"red": pl(red, "R", at(0, 8, 0), 0)}, Moved: "red", ResourceID: "R", At: at(0, 15, 0)}
	plan := PlanMove(in)
	if plan.Conflict != nil || len(plan.Overridden) != 2 || !plan.Changes[0].End.Equal(at(0, 17, 0)) {
		t.Fatalf("block start past the close: %+v", plan)
	}
	if !strings.Contains(plan.Overridden[0].Detail, "block") || !strings.Contains(plan.Overridden[1].Detail, "outside") {
		t.Fatalf("overridden = %+v", plan.Overridden)
	}
}
