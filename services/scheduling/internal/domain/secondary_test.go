package domain

import (
	"slices"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func secOp(id, order string, n, minutes int, secs ...string) Operation {
	return Operation{ID: id, OrderID: order, OperNumber: n, Minutes: minutes, Candidates: []string{"M"}, Secondaries: secs}
}

func TestSchedule_SecondaryBookedWithThePrimary(t *testing.T) {
	m := res("M", "M", true, weekdayShift(8*60, 16*60))
	k := res("K", "K", true, weekdayShift(8*60, 16*60))
	r := Schedule(Problem{Resources: []Resource{m, k}, Start: monday, HorizonDays: 7,
		Operations: []Operation{secOp("a", "o1", 10, 120, "K")}})
	pl := placementOf(t, r, "a")
	if !slices.Equal(pl.Secondaries, []string{"K"}) || !pl.Start.Equal(at(0, 8, 0)) {
		t.Fatalf("placement = %+v", pl)
	}
	b := secondaryBusy(pl)
	if len(b) != 1 || b[0].ResourceID != "K" || !b[0].Start.Equal(pl.Start) || !b[0].End.Equal(pl.End) {
		t.Fatalf("secondary booking = %+v", b)
	}
}

func TestSchedule_SkipsPrimarySlotWhenSecondaryIsBooked(t *testing.T) {
	m := res("M", "M", true, weekdayShift(8*60, 16*60))
	k := res("K", "K", true, weekdayShift(8*60, 16*60))
	// K is held by another job 08:00-10:00, so the primary slot at 08:00 is skipped.
	booked := []Busy{{ResourceID: "K", Start: at(0, 8, 0), End: at(0, 10, 0), JobID: "other"}}
	r := Schedule(Problem{Resources: []Resource{m, k}, Booked: booked, Start: monday, HorizonDays: 7,
		Operations: []Operation{secOp("a", "o1", 10, 60, "K")}})
	if pl := placementOf(t, r, "a"); !pl.Start.Equal(at(0, 10, 0)) {
		t.Fatalf("start = %v, want 10:00 when the secondary frees up", pl.Start)
	}
}

func TestSchedule_TwoJobsShareASecondaryWithoutOverlap(t *testing.T) {
	m1 := res("M1", "M1", true, weekdayShift(8*60, 16*60))
	m2 := res("M2", "M2", true, weekdayShift(8*60, 16*60))
	k := res("K", "K", true, weekdayShift(8*60, 16*60))
	a := secOp("a", "o1", 10, 120, "K")
	a.Candidates = []string{"M1"}
	b := secOp("b", "o2", 10, 60, "K")
	b.Candidates = []string{"M2"}
	r := Schedule(Problem{Resources: []Resource{m1, m2, k}, Start: monday, HorizonDays: 7, Operations: []Operation{a, b}})
	pa, pb := placementOf(t, r, "a"), placementOf(t, r, "b")
	if pa.Start.Before(pb.End) && pb.Start.Before(pa.End) {
		t.Fatalf("jobs on different machines overlap on the shared secondary: %v-%v and %v-%v", pa.Start, pa.End, pb.Start, pb.End)
	}
}

func TestSchedule_SecondaryClosedTimeShiftsTheSlot(t *testing.T) {
	m := res("M", "M", true, weekdayShift(8*60, 16*60))
	k := res("K", "K", true, weekdayShift(10*60, 16*60))
	r := Schedule(Problem{Resources: []Resource{m, k}, Start: monday, HorizonDays: 7,
		Operations: []Operation{secOp("a", "o1", 10, 60, "K")}})
	if pl := placementOf(t, r, "a"); !pl.Start.Equal(at(0, 10, 0)) {
		t.Fatalf("start = %v, want 10:00 when the secondary opens", pl.Start)
	}
}

func TestSchedule_SecondaryNeverOpenLeavesOperationUnplaced(t *testing.T) {
	m := res("M", "M", true, weekdayShift(8*60, 16*60))
	k := res("K", "K", true, []Shift{{Weekday: time.Sunday, StartMinute: 0, EndMinute: 10}})
	r := Schedule(Problem{Resources: []Resource{m, k}, Start: monday, HorizonDays: 3,
		Operations: []Operation{secOp("a", "o1", 10, 60, "K")}})
	if len(r.Placements) != 0 || len(r.Unplaced) != 1 {
		t.Fatalf("placements %d unplaced %d, want the operation unplaced", len(r.Placements), len(r.Unplaced))
	}
	if !strings.HasPrefix(r.Unplaced[0].Reason, CodeSecondaryBusy) || !strings.Contains(r.Unplaced[0].Reason, "K") {
		t.Fatalf("reason = %q, want SCHED-0005 naming the secondary", r.Unplaced[0].Reason)
	}
}

// A 24-hour calendar opens one interval per day; work across midnight lies in
// open time on both, so the secondary must not push the job to the next day.
func TestSchedule_SecondaryOpenAcrossAdjacentIntervals(t *testing.T) {
	m := res("M", "M", true, allDays(0, 1440))
	k := res("K", "K", true, allDays(0, 1440))
	r := Schedule(Problem{Resources: []Resource{m, k}, Start: at(0, 20, 0), HorizonDays: 7,
		Operations: []Operation{secOp("a", "o1", 10, 600, "K"), secOp("b", "o2", 10, 3000, "K")}})
	if pl := placementOf(t, r, "a"); !pl.Start.Equal(at(0, 20, 0)) || !pl.End.Equal(at(1, 6, 0)) {
		t.Fatalf("a = %v-%v, want 20:00 to 06:00 across midnight", pl.Start, pl.End)
	}
	// A job longer than a day is placed at all (it always crosses midnight).
	if pl := placementOf(t, r, "b"); !pl.Start.Equal(at(1, 6, 0)) {
		t.Fatalf("b start = %v, want 06:00 after a frees the secondary", pl.Start)
	}

	// Back-to-back shifts 06-14 and 14-22 on the secondary count as one.
	shifts := append(weekdayShift(6*60, 14*60), weekdayShift(14*60, 22*60)...)
	m2 := res("M", "M", true, weekdayShift(6*60, 22*60))
	k2 := res("K", "K", true, shifts)
	r = Schedule(Problem{Resources: []Resource{m2, k2}, Start: monday, HorizonDays: 7,
		Operations: []Operation{secOp("c", "o3", 10, 600, "K")}})
	if pl := placementOf(t, r, "c"); !pl.Start.Equal(at(0, 6, 0)) || len(pl.Segments) != 1 {
		t.Fatalf("c = %+v, want one segment from 06:00 across the shift change", pl)
	}
}

// A gap in the secondary's calendar in the middle of the primary's open time
// moves the job past the gap, not merely one minute later.
func TestSchedule_SecondaryGapMidJobMovesPastIt(t *testing.T) {
	m := res("M", "M", true, weekdayShift(8*60, 16*60))
	k := res("K", "K", true, append(weekdayShift(8*60, 10*60), weekdayShift(11*60, 16*60)...))
	r := Schedule(Problem{Resources: []Resource{m, k}, Start: monday, HorizonDays: 7,
		Operations: []Operation{secOp("a", "o1", 10, 180, "K")}})
	if pl := placementOf(t, r, "a"); !pl.Start.Equal(at(0, 11, 0)) {
		t.Fatalf("start = %v, want 11:00 when the secondary reopens for the whole job", pl.Start)
	}
}

func TestFirstGap(t *testing.T) {
	iv := []Interval{{at(0, 0, 0), at(1, 0, 0)}, {at(1, 0, 0), at(2, 0, 0)}, {at(1, 2, 0), at(1, 3, 0)}, {at(2, 6, 0), at(2, 8, 0)}}
	cases := []struct {
		name       string
		start, end time.Time
		gap        time.Time
		closed     bool
	}{
		{"inside one interval", at(0, 1, 0), at(0, 2, 0), time.Time{}, false},
		{"across adjacent intervals", at(0, 20, 0), at(1, 20, 0), time.Time{}, false},
		{"a nested interval does not move the cursor back", at(1, 1, 0), at(2, 1, 0), at(2, 0, 0), true},
		{"starts in closed time", at(2, 5, 0), at(2, 7, 0), at(2, 5, 0), true},
		{"past the last interval", at(2, 7, 0), at(2, 9, 0), at(2, 8, 0), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			gap, closed := firstGap(iv, c.start, c.end)
			if closed != c.closed || (closed && !gap.Equal(c.gap)) {
				t.Fatalf("firstGap = %v %v, want %v %v", gap, closed, c.gap, c.closed)
			}
		})
	}
}

func TestSchedule_InfiniteRunIgnoresSecondaryBookings(t *testing.T) {
	m := res("M", "M", true, weekdayShift(8*60, 16*60))
	k := res("K", "K", true, weekdayShift(8*60, 16*60))
	booked := []Busy{{ResourceID: "K", Start: at(0, 8, 0), End: at(0, 10, 0), JobID: "other"}}
	r := Schedule(Problem{Resources: []Resource{m, k}, Booked: booked, Infinite: true, Start: monday, HorizonDays: 7,
		Operations: []Operation{secOp("a", "o1", 10, 60, "K")}})
	if pl := placementOf(t, r, "a"); !pl.Start.Equal(at(0, 8, 0)) {
		t.Fatalf("start = %v, want 08:00 in an infinite run", pl.Start)
	}
}

// plS is pl with the operation's secondaries carried on the placement.
func plS(op Operation, rid string, start time.Time) Placement {
	p := pl(op, rid, start, 0)
	p.Secondaries = op.Secondaries
	return p
}

func TestPlanMove_RefusesWhenSecondaryIsBusy(t *testing.T) {
	m := res("M", "M", true, weekdayShift(8*60, 16*60))
	k := res("K", "K", true, weekdayShift(8*60, 16*60))
	op := secOp("a", "o1", 10, 60, "K")
	current := map[string]Placement{"a": plS(op, "M", at(0, 8, 0))}
	booked := append(busyOf(op, current["a"]), Busy{ResourceID: "K", Start: at(1, 9, 0), End: at(1, 11, 0), JobID: "other"})
	booked = append(booked, secondaryBusy(current["a"])...)
	in := MoveInput{Resources: []Resource{m, k}, Start: monday, HorizonDays: 7, Booked: booked, Order: []Operation{op},
		Current: current, Moved: "a", ResourceID: "M", At: at(1, 10, 0)}
	plan := PlanMove(in)
	if plan.Conflict == nil || plan.Conflict.Code != CodeSecondaryBusy || !strings.Contains(plan.Conflict.Detail, "K") {
		t.Fatalf("conflict = %+v, want SCHED-0005", plan.Conflict)
	}
	in.At = at(1, 11, 0)
	plan = PlanMove(in)
	if plan.Conflict != nil || !plan.Changes[0].Start.Equal(at(1, 11, 0)) || len(plan.Changes[0].Secondaries) != 1 {
		t.Fatalf("move to a free slot: %+v", plan)
	}
	// The job's own old booking on the secondary is released: moving within it is fine.
	in.At = at(0, 8, 30)
	if plan := PlanMove(in); plan.Conflict != nil {
		t.Fatalf("moving over its own old secondary booking: %+v", plan.Conflict)
	}
}

func TestPlanMove_PushedSuccessorWaitsForItsSecondary(t *testing.T) {
	m := res("M", "M", true, weekdayShift(8*60, 16*60))
	k := res("K", "K", true, weekdayShift(8*60, 16*60))
	first := secOp("a", "o1", 10, 60)
	next := secOp("b", "o1", 20, 60, "K")
	current := map[string]Placement{"a": pl(first, "M", at(0, 8, 0), 0), "b": plS(next, "M", at(0, 9, 0))}
	booked := append(busyOf(first, current["a"]), busyOf(next, current["b"])...)
	booked = append(booked, secondaryBusy(current["b"])...)
	booked = append(booked, Busy{ResourceID: "K", Start: at(0, 10, 30), End: at(0, 12, 0), JobID: "other"})
	plan := PlanMove(MoveInput{Resources: []Resource{m, k}, Start: monday, HorizonDays: 7, Booked: booked, Order: []Operation{first, next},
		Current: current, Moved: "a", ResourceID: "M", At: at(0, 10, 0)})
	if plan.Conflict != nil || len(plan.Changes) != 2 {
		t.Fatalf("plan = %+v", plan)
	}
	if got := plan.Changes[1].Start; !got.Equal(at(0, 12, 0)) {
		t.Fatalf("successor start = %v, want 12:00 after the secondary frees", got)
	}
}

func TestSchedule_PropertySecondaryNeverDoubleBooked(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		machines := []Resource{res("M1", "M1", true, allDays(0, 600)), res("M2", "M2", true, allDays(120, 720)), res("K", "K", true, allDays(60, 660))}
		n := rapid.IntRange(1, 10).Draw(t, "ops")
		var ops []Operation
		for i := 0; i < n; i++ {
			c := []string{"M1", "M2"}[rapid.IntRange(0, 1).Draw(t, "m")]
			op := Operation{ID: string(rune('a' + i)), OrderID: string(rune('a' + i)), OperNumber: 10, Minutes: rapid.IntRange(10, 300).Draw(t, "min"), Candidates: []string{c}}
			if rapid.Bool().Draw(t, "uses") {
				op.Secondaries = []string{"K"}
			}
			ops = append(ops, op)
		}
		r := Schedule(Problem{Resources: machines, Operations: ops, Start: monday, HorizonDays: 30})
		var held []Busy
		for _, p := range r.Placements {
			for _, b := range secondaryBusy(p) {
				for _, s := range held {
					if b.Start.Before(s.End) && s.Start.Before(b.End) {
						t.Fatalf("secondary K double booked: %v-%v and %v-%v", b.Start, b.End, s.Start, s.End)
					}
				}
				held = append(held, b)
				// every secondary segment lies inside K's open capacity 01:00-11:00
				day := b.Start.Truncate(24 * time.Hour)
				if b.Start.Before(day.Add(time.Hour)) || b.End.After(day.Add(11*time.Hour)) {
					t.Fatalf("secondary booking outside open capacity: %v-%v", b.Start, b.End)
				}
			}
		}
	})
}

// A resource can be one job's primary and another's secondary; the scheduler
// keeps the two apart because both are busy time on the resource.
func TestSchedule_PrimaryAndSecondaryUseOfOneResourceDoNotOverlap(t *testing.T) {
	m := res("M", "M", true, weekdayShift(8*60, 16*60))
	k := res("K", "K", true, weekdayShift(8*60, 16*60))
	onK := Operation{ID: "a", OrderID: "o1", OperNumber: 10, Minutes: 120, Candidates: []string{"K"}}
	withK := secOp("b", "o2", 10, 60, "K")
	r := Schedule(Problem{Resources: []Resource{m, k}, Start: monday, HorizonDays: 7, Operations: []Operation{onK, withK}})
	pa, pb := placementOf(t, r, "a"), placementOf(t, r, "b")
	if pa.Start.Before(pb.End) && pb.Start.Before(pa.End) {
		t.Fatalf("K is held as primary %v-%v and secondary %v-%v at once", pa.Start, pa.End, pb.Start, pb.End)
	}
}
