package domain

import (
	"strings"
	"testing"
	"time"
)

func queueResources() []Resource {
	return []Resource{res("r1", "R1", true, weekdayShift(8*60, 16*60)), res("r2", "R2", true, weekdayShift(8*60, 16*60))}
}

// The wait between two operations is elapsed time: queue after of the first
// plus queue before of the second, no resource booked, and it can run through
// closed time.
func TestSchedule_QueueTimesDelayTheNextOperation(t *testing.T) {
	ops := []Operation{
		{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 120, QueueAfter: 60, Candidates: []string{"r1"}},
		{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: 60, QueueBefore: 30, Candidates: []string{"r2"}},
		{ID: "o3", OrderID: "a", OperNumber: 30, Minutes: 60, QueueBefore: 600, Candidates: []string{"r1"}},
	}
	got := Schedule(Problem{Resources: queueResources(), Operations: ops, Start: monday, HorizonDays: 14})
	if len(got.Placements) != 3 {
		t.Fatalf("placements = %d, unplaced = %v", len(got.Placements), got.Unplaced)
	}
	o1, o2, o3 := got.Placements[0], got.Placements[1], got.Placements[2]
	if !o1.End.Equal(at(0, 10, 0)) {
		t.Fatalf("o1 ends %v, want 10:00", o1.End)
	}
	// 10:00 + 60 + 30 = 11:30.
	if !o2.Start.Equal(at(0, 11, 30)) {
		t.Fatalf("o2 starts %v, want 11:30", o2.Start)
	}
	// 12:30 + 600 min = 22:30, closed: the next open time is Tuesday 08:00.
	if !o3.Start.Equal(at(1, 8, 0)) {
		t.Fatalf("o3 starts %v, want Tuesday 08:00", o3.Start)
	}
	// Nothing was booked for the wait: another order fits in front of o2 on r2.
	other := Schedule(Problem{Resources: queueResources(), Operations: []Operation{
		{ID: "x", OrderID: "z", OperNumber: 10, Minutes: 60, Candidates: []string{"r2"}}}, Booked: []Busy{
		{ResourceID: "r2", Start: o2.Start, End: o2.End, JobID: "o2"}}, Start: monday, HorizonDays: 14})
	if !other.Placements[0].Start.Equal(at(0, 8, 0)) {
		t.Fatalf("x starts %v, want 08:00 (the queue holds no resource)", other.Placements[0].Start)
	}
}

func TestSchedule_NoQueueTimesKeepsBackToBack(t *testing.T) {
	ops := []Operation{
		{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 120, Candidates: []string{"r1"}},
		{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: 60, Candidates: []string{"r1"}},
	}
	got := Schedule(Problem{Resources: queueResources(), Operations: ops, Start: monday, HorizonDays: 14})
	if !got.Placements[1].Start.Equal(got.Placements[0].End) {
		t.Fatalf("o2 starts %v, want o1's end %v", got.Placements[1].Start, got.Placements[0].End)
	}
}

// Backward scheduling leaves room for the waits: the chain still ends by the
// due date.
func TestBackward_QueueTimesAreInsideTheDueDate(t *testing.T) {
	ops := []Operation{
		{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 120, QueueAfter: 120, DueAt: at(2, 16, 0), Candidates: []string{"r1"}},
		{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: 120, QueueBefore: 60, DueAt: at(2, 16, 0), Candidates: []string{"r1"}},
	}
	got := Schedule(backwardProblem(ops, nil, true))
	if len(got.Placements) != 2 {
		t.Fatalf("placements = %d, unplaced = %v", len(got.Placements), got.Unplaced)
	}
	o1, o2 := got.Placements[0], got.Placements[1]
	if o2.End.After(at(2, 16, 0)) || o2.LateMinutes != 0 {
		t.Fatalf("o2 ends %v late %d, want by Wednesday 16:00", o2.End, o2.LateMinutes)
	}
	if o2.Start.Before(o1.End.Add(3 * time.Hour)) {
		t.Fatalf("o2 starts %v, want at least 180 min after o1 ends %v", o2.Start, o1.End)
	}
}

func queueMoveInput(t *testing.T) (MoveInput, []Operation) {
	t.Helper()
	ops := []Operation{
		{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 60, QueueAfter: 30, Candidates: []string{"r1"}},
		{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: 60, QueueBefore: 30, Candidates: []string{"r2"}},
		{ID: "o3", OrderID: "a", OperNumber: 30, Minutes: 60, QueueBefore: 15, Candidates: []string{"r2"}},
	}
	got := Schedule(Problem{Resources: queueResources(), Operations: ops, Start: monday, HorizonDays: 14})
	cur := map[string]Placement{}
	var booked []Busy
	for _, pl := range got.Placements {
		cur[pl.OperationID] = pl
		booked = append(booked, Busy{ResourceID: pl.ResourceID, Start: pl.Start, End: pl.End, JobID: pl.OperationID})
	}
	return MoveInput{Resources: queueResources(), Start: monday, HorizonDays: 14, Booked: booked, Order: ops, Current: cur}, ops
}

// Moving an operation earlier than its predecessor's end plus the wait snaps
// it to the wait; moving the first one later pushes the others by their waits.
func TestPlanMove_KeepsQueueTimes(t *testing.T) {
	in, _ := queueMoveInput(t)
	if !in.Current["o2"].Start.Equal(at(0, 10, 0)) {
		t.Fatalf("setup: o2 starts %v, want 10:00 (o1 ends 09:00 + 60)", in.Current["o2"].Start)
	}
	in.Moved, in.ResourceID, in.At = "o2", "r2", at(0, 9, 0)
	plan := PlanMove(in)
	if plan.Conflict != nil || len(plan.Changes) == 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if !plan.Changes[0].Start.Equal(at(0, 10, 0)) {
		t.Fatalf("o2 moved to %v, want 10:00 (o1 ends 09:00 + queue after 30 + queue before 30)", plan.Changes[0].Start)
	}

	in, _ = queueMoveInput(t)
	in.Moved, in.ResourceID, in.At = "o1", "r1", at(0, 12, 0)
	plan = PlanMove(in)
	if plan.Conflict != nil || len(plan.Changes) != 3 {
		t.Fatalf("plan = %+v", plan)
	}
	// o1 12:00-13:00, o2 at 14:00 (30 + 30), o3 at 15:15 (o2 ends 15:00 + o2's 0 after + 15 before).
	wantStart := []time.Time{at(0, 12, 0), at(0, 14, 0), at(0, 15, 15)}
	for i, pl := range plan.Changes {
		if !pl.Start.Equal(wantStart[i]) {
			t.Fatalf("change %d (%s) starts %v, want %v", i, pl.OperationID, pl.Start, wantStart[i])
		}
	}
}

// A predecessor that keeps its slot (firmed, or outside the run's selection)
// still holds its queue after in front of the next operation.
func TestSchedule_FixedPredecessorKeepsItsQueueAfter(t *testing.T) {
	fixed := Placement{OperationID: "o1", OrderID: "a", OperNumber: 10, ResourceID: "r1", Start: at(0, 8, 0), End: at(0, 10, 0), QueueAfter: 60,
		Segments: []Segment{{Start: at(0, 8, 0), End: at(0, 10, 0)}}}
	ops := []Operation{{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: 60, QueueBefore: 30, Candidates: []string{"r2"}}}
	got := Schedule(Problem{Resources: queueResources(), Operations: ops, Fixed: []Placement{fixed}, Start: monday, HorizonDays: 14})
	if len(got.Placements) != 1 || !got.Placements[0].Start.Equal(at(0, 11, 30)) {
		t.Fatalf("placements = %+v unplaced = %v, want o2 at 11:30 (10:00 + 60 after + 30 before)", got.Placements, got.Unplaced)
	}
}

// Queue minutes are elapsed time, not wall-clock time: a day of queue across
// the US fall-back change ends 24 real hours later, an hour earlier on the
// local clock.
func TestSchedule_QueueIsElapsedAcrossADSTChange(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	r := func(id string) Resource {
		return Resource{ID: id, Code: strings.ToUpper(id), Finite: true, Location: ny, Shifts: allDays(8*60, 16*60)}
	}
	start := time.Date(2026, 10, 31, 0, 0, 0, 0, time.UTC)
	got := Schedule(Problem{Resources: []Resource{r("r1"), r("r2")}, Start: start, HorizonDays: 7, Operations: []Operation{
		{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 60, QueueAfter: 1440, Candidates: []string{"r1"}},
		{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: 60, Candidates: []string{"r2"}},
	}})
	if len(got.Placements) != 2 {
		t.Fatalf("placements = %d, unplaced = %v", len(got.Placements), got.Unplaced)
	}
	o1, o2 := got.Placements[0], got.Placements[1]
	// o1 Saturday 08:00-09:00 EDT (12:00-13:00 UTC); 1440 min later is
	// Sunday 13:00 UTC = 08:00 EST, the shift start (a wall-clock day would be
	// 09:00 EST).
	if !o1.End.Equal(time.Date(2026, 10, 31, 13, 0, 0, 0, time.UTC)) || !o2.Start.Equal(time.Date(2026, 11, 1, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("o1 ends %v, o2 starts %v; want 13:00 UTC both days", o1.End, o2.Start)
	}
}

// Backward scheduling with queues: an exact fit ends on the due date; a latest
// start inside the margin after the run start, or a chain that cannot end on
// time because of its queue, is scheduled forward from the run start (BR-23.2).
func TestBackward_QueueTimesAndTheRunStart(t *testing.T) {
	problem := func(due time.Time) Problem {
		return Problem{Resources: []Resource{res("r1", "R1", true, allDays(0, 1440))}, Start: monday, HorizonDays: 14, Backward: true,
			Operations: []Operation{
				{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 60, QueueAfter: 120, DueAt: due, Candidates: []string{"r1"}},
				{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: 60, DueAt: due, Candidates: []string{"r1"}},
			}}
	}
	cases := []struct {
		name           string
		due            time.Time
		o1, o2         time.Time
		late, warnings int
	}{
		{"exact fit ends on the due date", at(0, 6, 0), at(0, 2, 0), at(0, 5, 0), 0, 0},
		{"latest start inside the margin runs forward", at(0, 4, 30), at(0, 0, 0), at(0, 3, 0), 0, 0},
		{"the queue makes the order late", at(0, 3, 0), at(0, 0, 0), at(0, 3, 0), 60, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Schedule(problem(c.due))
			if len(got.Placements) != 2 {
				t.Fatalf("placements = %d, unplaced = %v", len(got.Placements), got.Unplaced)
			}
			o1, o2 := got.Placements[0], got.Placements[1]
			if !o1.Start.Equal(c.o1) || !o2.Start.Equal(c.o2) || o2.LateMinutes != c.late || len(got.Warnings) != c.warnings {
				t.Fatalf("o1 %v o2 %v late %d warnings %v; want %v, %v, late %d, %d warnings", o1.Start, o2.Start, o2.LateMinutes, got.Warnings, c.o1, c.o2, c.late, c.warnings)
			}
			if c.warnings > 0 && got.Warnings[0].Code != CodeLateFallback {
				t.Fatalf("warning %+v, want %s", got.Warnings[0], CodeLateFallback)
			}
		})
	}
}

// A queue longer than the network's maximum wait can never be met, however
// late the predecessor runs: the order is NETWORK_INFEASIBLE at once, naming
// the queue, instead of delaying the predecessor up to the limit.
func TestSchedule_QueueLongerThanNetworkMaxWaitIsInfeasible(t *testing.T) {
	resources, net := mixFill(t)
	r := Schedule(Problem{Resources: resources, Networks: []ResourceNetwork{net}, Start: monday, HorizonDays: 14, Operations: []Operation{
		{ID: "mix", OrderID: "o1", OperNumber: 10, Minutes: 60, QueueAfter: 100, Candidates: []string{"V1"}},
		{ID: "fill", OrderID: "o1", OperNumber: 20, Minutes: 60, QueueBefore: 30, Network: "MIXFILL", Candidates: []string{"F1"}},
	}})
	if len(r.Placements) != 0 || len(r.Unplaced) != 2 {
		t.Fatalf("placements %+v unplaced %v, want the order unplaced", r.Placements, r.Unplaced)
	}
	if why := r.Unplaced[1].Reason; !strings.HasPrefix(why, CodeNetworkInfeasible) || !strings.Contains(why, "queue time 130 min") {
		t.Fatalf("fill reason %q, want SCHED-0009 naming the 130 min queue", why)
	}
	// Within the maximum wait the same order is placed with the queue kept.
	r = Schedule(Problem{Resources: resources, Networks: []ResourceNetwork{net}, Start: monday, HorizonDays: 14, Operations: []Operation{
		{ID: "mix", OrderID: "o1", OperNumber: 10, Minutes: 60, QueueAfter: 60, Candidates: []string{"V1"}},
		{ID: "fill", OrderID: "o1", OperNumber: 20, Minutes: 60, QueueBefore: 30, Network: "MIXFILL", Candidates: []string{"F1"}},
	}})
	if fill := placementOf(t, r, "fill"); !fill.Start.Equal(at(0, 2, 30)) {
		t.Fatalf("fill starts %v, want 02:30 (mix ends 01:00 + 90 min queue)", fill.Start)
	}
}

// A firmed successor that the queue would have to push refuses the move; the
// queue is not a capacity rule, so an override still snaps to it instead of
// listing it as overridden.
func TestPlanMove_QueueAgainstFirmedSuccessorAndOverride(t *testing.T) {
	in, _ := queueMoveInput(t)
	in.Firmed = map[string]bool{"o3": true}
	in.Moved, in.ResourceID, in.At = "o1", "r1", at(0, 8, 30)
	plan := PlanMove(in)
	if plan.Conflict == nil || plan.Conflict.Code != CodeFirmed || plan.Conflict.JobID != "o3" {
		t.Fatalf("plan = %+v, want SCHED firmed conflict on o3 (o2 ends 11:30, o3 needs 11:45)", plan)
	}

	in, _ = queueMoveInput(t)
	in.Override = true
	in.Moved, in.ResourceID, in.At = "o2", "r2", at(0, 9, 15)
	plan = PlanMove(in)
	if plan.Conflict != nil || len(plan.Overridden) != 0 || !plan.Changes[0].Start.Equal(at(0, 10, 0)) {
		t.Fatalf("plan = %+v, want o2 snapped to 10:00 with nothing overridden", plan)
	}
}
