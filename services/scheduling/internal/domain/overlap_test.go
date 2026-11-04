package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestOverlapShareBP(t *testing.T) {
	d := decimal.RequireFromString
	cases := []struct {
		name     string
		q, total string
		want     int
	}{
		{"quarter", "25", "100", 2500},
		{"rounds up", "1", "3", 3334},
		{"all of it is no overlap", "100", "100", MaxOverlapBP},
		{"more than all", "150", "100", MaxOverlapBP},
		{"zero", "0", "100", 0},
		{"no quantity", "5", "0", 0},
	}
	for _, c := range cases {
		if got := OverlapShareBP(d(c.q), d(c.total)); got != c.want {
			t.Errorf("%s: share = %d, want %d", c.name, got, c.want)
		}
	}
}

// The release point counts working minutes only, changeover first, and skips
// the closed time between segments.
func TestPlacement_Release(t *testing.T) {
	segs := []Segment{{Start: at(0, 8, 0), End: at(0, 10, 0)}, {Start: at(1, 8, 0), End: at(1, 10, 0)}}
	pl := Placement{Segments: segs, Start: segs[0].Start, End: segs[1].End}
	if !pl.Release().Equal(pl.End) {
		t.Fatalf("no overlap: release %v, want the end %v", pl.Release(), pl.End)
	}
	pl.OverlapBP = 2500 // 240 min of work: 60 min in
	if !pl.Release().Equal(at(0, 9, 0)) {
		t.Fatalf("release %v, want Monday 09:00", pl.Release())
	}
	pl.OverlapBP = 7500 // 180 min: Tuesday 09:00
	if !pl.Release().Equal(at(1, 9, 0)) {
		t.Fatalf("release %v, want Tuesday 09:00", pl.Release())
	}
	// 30 changeover minutes first, then 25% of the 180 process minutes (45).
	pl.ChangeoverMinutes, pl.OverlapBP = 30, 2500
	pl.Segments = []Segment{{Start: at(0, 8, 0), End: at(0, 11, 30)}}
	pl.End = at(0, 11, 30)
	if !pl.Release().Equal(at(0, 9, 15)) {
		t.Fatalf("release %v, want 09:15 (30 + 45 min in)", pl.Release())
	}
	pl.OverlapBP = MaxOverlapBP
	if !pl.Release().Equal(pl.End) {
		t.Fatalf("a full overlap releases at the end, got %v", pl.Release())
	}
}

func overlapProblem(o2Minutes int, bp int) Problem {
	return Problem{Resources: queueResources(), Start: monday, HorizonDays: 14, Operations: []Operation{
		{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 400, OverlapBP: bp, Candidates: []string{"r1"}},
		{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: o2Minutes, Candidates: []string{"r2"}},
	}}
}

// BR-23.5: the next operation starts once the overlap share is through, and
// still does not end before the operation it overlaps.
func TestSchedule_OverlapStartsTheNextOperationEarly(t *testing.T) {
	got := Schedule(overlapProblem(300, 2500))
	if len(got.Placements) != 2 {
		t.Fatalf("placements = %d, unplaced = %v", len(got.Placements), got.Unplaced)
	}
	o1, o2 := got.Placements[0], got.Placements[1]
	if !o1.End.Equal(at(0, 14, 40)) {
		t.Fatalf("o1 ends %v, want 14:40", o1.End)
	}
	// 25% of 400 = 100 min after 08:00.
	if !o2.Start.Equal(at(0, 9, 40)) || !o2.End.Equal(at(0, 14, 40)) {
		t.Fatalf("o2 runs %v-%v, want 09:40-14:40", o2.Start, o2.End)
	}
}

func TestSchedule_OverlapSuccessorNeverEndsBeforeThePredecessor(t *testing.T) {
	got := Schedule(overlapProblem(60, 2500))
	o1, o2 := got.Placements[0], got.Placements[1]
	if o2.End.Before(o1.End) {
		t.Fatalf("o2 ends %v before o1 ends %v", o2.End, o1.End)
	}
	if !o2.Start.Equal(at(0, 13, 40)) {
		t.Fatalf("o2 starts %v, want 13:40 (its 60 minutes end with o1)", o2.Start)
	}
}

func TestSchedule_OverlapKeepsQueueTimesOnTopOfTheRelease(t *testing.T) {
	p := overlapProblem(300, 2500)
	p.Operations[0].QueueAfter = 20
	p.Operations[1].QueueBefore = 10
	got := Schedule(p)
	o2 := got.Placements[1]
	if !o2.Start.Equal(at(0, 10, 10)) {
		t.Fatalf("o2 starts %v, want 10:10 (release 09:40 + 20 + 10)", o2.Start)
	}
}

func TestSchedule_FullOrNoOverlapIsBackToBack(t *testing.T) {
	for _, bp := range []int{0, MaxOverlapBP} {
		got := Schedule(overlapProblem(60, bp))
		if !got.Placements[1].Start.Equal(got.Placements[0].End) {
			t.Fatalf("bp %d: o2 starts %v, want o1's end %v", bp, got.Placements[1].Start, got.Placements[0].End)
		}
	}
}

// A fixed predecessor with an overlap share releases the next operation early.
func TestSchedule_FixedPredecessorOverlap(t *testing.T) {
	fixed := Placement{OperationID: "o1", OrderID: "a", OperNumber: 10, ResourceID: "r1", Start: at(0, 8, 0), End: at(0, 12, 0), OverlapBP: 5000,
		Segments: []Segment{{Start: at(0, 8, 0), End: at(0, 12, 0)}}}
	ops := []Operation{{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: 240, Candidates: []string{"r2"}}}
	got := Schedule(Problem{Resources: queueResources(), Operations: ops, Fixed: []Placement{fixed}, Start: monday, HorizonDays: 14})
	if len(got.Placements) != 1 || !got.Placements[0].Start.Equal(at(0, 10, 0)) {
		t.Fatalf("placements = %+v unplaced = %v, want o2 at 10:00", got.Placements, got.Unplaced)
	}
}

func overlapMoveInput(t *testing.T) MoveInput {
	t.Helper()
	rs := append(queueResources(), res("r3", "R3", true, weekdayShift(8*60, 16*60)))
	ops := []Operation{
		{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 240, OverlapBP: 5000, Candidates: []string{"r1"}},
		{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: 120, Candidates: []string{"r2"}},
		{ID: "o3", OrderID: "a", OperNumber: 30, Minutes: 60, Candidates: []string{"r3"}},
	}
	got := Schedule(Problem{Resources: rs, Operations: ops, Start: monday, HorizonDays: 14})
	cur := map[string]Placement{}
	var booked []Busy
	for _, pl := range got.Placements {
		cur[pl.OperationID] = pl
		booked = append(booked, Busy{ResourceID: pl.ResourceID, Start: pl.Start, End: pl.End, JobID: pl.OperationID})
	}
	return MoveInput{Resources: rs, Start: monday, HorizonDays: 14, Booked: booked, Order: ops, Current: cur}
}

func TestPlanMove_OverlapSnapsAndPushes(t *testing.T) {
	in := overlapMoveInput(t)
	// o1 08:00-12:00, release 10:00; o2 10:00-12:00 (ends with o1); o3 after o2.
	if !in.Current["o2"].Start.Equal(at(0, 10, 0)) || !in.Current["o3"].Start.Equal(at(0, 12, 0)) {
		t.Fatalf("setup: o2 %v o3 %v", in.Current["o2"].Start, in.Current["o3"].Start)
	}
	in.Moved, in.ResourceID, in.At = "o2", "r2", at(0, 8, 30)
	plan := PlanMove(in)
	if plan.Conflict != nil || len(plan.Changes) == 0 {
		t.Fatalf("plan = %+v", plan)
	}
	if !plan.Changes[0].Start.Equal(at(0, 10, 0)) {
		t.Fatalf("o2 moved to %v, want 10:00 (the release of o1)", plan.Changes[0].Start)
	}

	in = overlapMoveInput(t)
	in.Moved, in.ResourceID, in.At = "o1", "r1", at(0, 9, 0)
	plan = PlanMove(in)
	if plan.Conflict != nil {
		t.Fatalf("plan = %+v", plan)
	}
	want := map[string]time.Time{"o1": at(0, 9, 0), "o2": at(0, 11, 0), "o3": at(0, 13, 0)}
	for _, pl := range plan.Changes {
		if !pl.Start.Equal(want[pl.OperationID]) {
			t.Fatalf("%s starts %v, want %v", pl.OperationID, pl.Start, want[pl.OperationID])
		}
		delete(want, pl.OperationID)
	}
	if len(want) != 0 {
		t.Fatalf("no change for %v", want)
	}
}

// Backward scheduling uses the overlap: the chain ends by the due date and the
// successor starts before the predecessor ends.
func TestBackward_OverlapIsInsideTheDueDate(t *testing.T) {
	due := at(2, 16, 0)
	ops := []Operation{
		{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 240, OverlapBP: 5000, DueAt: due, Candidates: []string{"r1"}},
		{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: 240, DueAt: due, Candidates: []string{"r2"}},
	}
	p := backwardProblem(ops, nil, true)
	p.Resources = append(p.Resources, res("r2", "R2", true, weekdayShift(8*60, 16*60)))
	got := Schedule(p)
	if len(got.Placements) != 2 {
		t.Fatalf("placements = %d, unplaced = %v", len(got.Placements), got.Unplaced)
	}
	o1, o2 := got.Placements[0], got.Placements[1]
	if o2.End.After(due) || o2.LateMinutes != 0 {
		t.Fatalf("o2 ends %v late %d, want by the due date", o2.End, o2.LateMinutes)
	}
	if !o2.Start.Before(o1.End) || o2.End.Before(o1.End) {
		t.Fatalf("o1 %v-%v o2 %v-%v: want an overlap that ends together or later", o1.Start, o1.End, o2.Start, o2.End)
	}
}

// A successor whose ready time falls in its resource's closed time keeps the
// same slot however far the ready time moves inside that closed time, so the
// re-placement must move from the slot it found, not from the ready time: the
// successor still never ends before the overlapped operation.
func TestSchedule_OverlapSuccessorAfterClosedTimeStillEndsWithThePredecessor(t *testing.T) {
	saturday := at(5, 0, 0)
	rs := []Resource{res("r24", "R24", true, allDays(0, 1440)), res("r2", "R2", true, weekdayShift(8*60, 16*60))}
	// o1 runs Saturday 00:00 to Monday 10:00 around the clock; its 25% release
	// is Saturday 14:30, so o2's first slot is Monday 08:00-09:00.
	ops := []Operation{
		{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 3480, OverlapBP: 2500, Candidates: []string{"r24"}},
		{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: 60, Candidates: []string{"r2"}},
	}
	got := Schedule(Problem{Resources: rs, Operations: ops, Start: saturday, HorizonDays: 14})
	if len(got.Placements) != 2 {
		t.Fatalf("placements = %d, unplaced = %v", len(got.Placements), got.Unplaced)
	}
	o1, o2 := got.Placements[0], got.Placements[1]
	if !o1.End.Equal(at(7, 10, 0)) {
		t.Fatalf("o1 ends %v, want Monday 10:00", o1.End)
	}
	if o2.End.Before(o1.End) || !o2.Start.Equal(at(7, 9, 0)) {
		t.Fatalf("o2 runs %v-%v, want 09:00-10:00 on Monday (ends with o1)", o2.Start, o2.End)
	}
}

// The last units of the overlapped operation still wait the queue after and
// queue before (FR-23.3) before the successor can finish them: a fast
// successor ends no earlier than the predecessor's end plus the queue, in a
// run, on a move and on a push.
func TestSchedule_OverlapSuccessorEndsAfterTheLastUnitsQueue(t *testing.T) {
	p := overlapProblem(60, 2500)
	p.Operations[0].QueueAfter = 20
	got := Schedule(p)
	o1, o2 := got.Placements[0], got.Placements[1]
	if !o1.End.Equal(at(0, 14, 40)) || !o2.Start.Equal(at(0, 14, 0)) || !o2.End.Equal(at(0, 15, 0)) {
		t.Fatalf("o1 ends %v, o2 runs %v-%v; want o2 14:00-15:00 (o1's end plus 20 min queue)", o1.End, o2.Start, o2.End)
	}

	in := overlapMoveInput(t)
	in.Order[0].QueueAfter = 30
	pl := in.Current["o1"]
	pl.QueueAfter = 30
	in.Current["o1"] = pl
	in.Order[1].Minutes = 60
	// o1 08:00-12:00 with a 10:00 release: o2 (60 min) may start at 10:30 but
	// must not end before 12:30, so it lands at 11:30.
	in.Moved, in.ResourceID, in.At = "o2", "r2", at(0, 8, 0)
	plan := PlanMove(in)
	if plan.Conflict != nil || len(plan.Changes) == 0 || !plan.Changes[0].Start.Equal(at(0, 11, 30)) {
		t.Fatalf("move o2: %+v, want it at 11:30", plan)
	}
	in = overlapMoveInput(t)
	in.Order[0].QueueAfter = 30
	in.Order[1].Minutes = 30
	cur := in.Current["o2"]
	cur.End = cur.Start.Add(30 * time.Minute)
	cur.Segments = []Segment{{Start: cur.Start, End: cur.End}}
	in.Current["o2"] = cur
	// o1 moved to 09:00-13:00 (release 11:00): o2 needs 30 minutes and ends
	// no earlier than 13:30, so it starts at 13:00.
	in.Moved, in.ResourceID, in.At = "o1", "r1", at(0, 9, 0)
	plan = PlanMove(in)
	if plan.Conflict != nil {
		t.Fatalf("plan = %+v", plan)
	}
	pushed := false
	for _, c := range plan.Changes {
		if c.OperationID == "o2" {
			pushed = true
			if !c.Start.Equal(at(0, 13, 0)) {
				t.Fatalf("o2 pushed to %v, want 13:00", c.Start)
			}
		}
	}
	if !pushed {
		t.Fatalf("o2 was not pushed: %+v", plan.Changes)
	}
}

// Backward with a faster successor: the chain is as long as the overlapped
// operation, so the search finds the latest start at due minus o1, and o2
// ends with o1 at the due date.
func TestBackward_OverlapWithAFasterSuccessor(t *testing.T) {
	due := at(2, 16, 0)
	ops := []Operation{
		{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 240, OverlapBP: 5000, DueAt: due, Candidates: []string{"r1"}},
		{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: 60, DueAt: due, Candidates: []string{"r2"}},
	}
	p := backwardProblem(ops, nil, true)
	p.Resources = append(p.Resources, res("r2", "R2", true, weekdayShift(8*60, 16*60)))
	got := Schedule(p)
	if len(got.Placements) != 2 || len(got.Warnings) != 0 {
		t.Fatalf("placements = %+v, unplaced = %v, warnings = %v", got.Placements, got.Unplaced, got.Warnings)
	}
	o1, o2 := got.Placements[0], got.Placements[1]
	if !o1.Start.Equal(at(2, 12, 0)) || !o2.Start.Equal(at(2, 15, 0)) || !o2.End.Equal(due) {
		t.Fatalf("o1 %v-%v o2 %v-%v, want o1 12:00-16:00 and o2 15:00-16:00 on Wednesday", o1.Start, o1.End, o2.Start, o2.End)
	}
}
