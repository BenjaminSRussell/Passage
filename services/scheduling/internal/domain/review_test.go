package domain

import (
	"testing"
	"time"
)

// Regression: booked work later in the same shift must not block a job that
// finishes before it starts. The draft treated any booking in the open
// interval as a conflict, so a Gantt move into a free gap was refused.
func TestFill_GapBeforeLaterBookingInSameShiftIsUsable(t *testing.T) {
	iv := []Interval{{at(0, 8, 0), at(0, 16, 0)}}
	busy := []Busy{{Start: at(0, 12, 0), End: at(0, 13, 0), JobID: "later"}}
	segs, conf := fill(iv, busy, at(0, 8, 0), 120, true)
	if conf != nil || len(segs) != 1 || !segs[0].End.Equal(at(0, 10, 0)) {
		t.Fatalf("segs = %v conflict = %+v, want 08:00-10:00", segs, conf)
	}
	if _, conf := fill(iv, busy, at(0, 11, 0), 90, true); conf == nil || conf.Code != CodeOverlap || conf.JobID != "later" {
		t.Fatalf("conflict = %+v, want overlap with later", conf)
	}
}

func TestSchedule_PlacesIntoGapBeforeExistingBooking(t *testing.T) {
	p := Problem{
		Resources:  []Resource{res("r", "R", true, weekdayShift(8*60, 16*60))},
		Operations: []Operation{{ID: "op", OrderID: "o", OperNumber: 10, Minutes: 60, Candidates: []string{"r"}}},
		Booked:     []Busy{{ResourceID: "r", Start: at(0, 12, 0), End: at(0, 14, 0), JobID: "x"}},
		Start:      monday, HorizonDays: 7,
	}
	r := Schedule(p)
	if len(r.Placements) != 1 || !r.Placements[0].Start.Equal(at(0, 8, 0)) {
		t.Fatalf("placements = %+v, want start 08:00 Monday", r.Placements)
	}
}

func TestSchedule_FixedPredecessorHoldsBackSuccessor(t *testing.T) {
	shifts := weekdayShift(8*60, 16*60)
	p := Problem{
		Resources: []Resource{res("r", "R", true, shifts), res("s", "S", true, shifts)},
		Operations: []Operation{
			{ID: "op20", OrderID: "o", OperNumber: 20, Minutes: 30, Candidates: []string{"s"}},
		},
		Booked: []Busy{{ResourceID: "r", Start: at(1, 9, 0), End: at(1, 11, 0), JobID: "op10"}},
		Fixed:  []Placement{{OperationID: "op10", OrderID: "o", OperNumber: 10, ResourceID: "r", Start: at(1, 9, 0), End: at(1, 11, 0)}},
		Start:  monday, HorizonDays: 7,
	}
	r := Schedule(p)
	if len(r.Placements) != 1 || !r.Placements[0].Start.Equal(at(1, 11, 0)) {
		t.Fatalf("placements = %+v, want op20 at Tuesday 11:00 after firmed op10", r.Placements)
	}
}

func TestSchedule_InfiniteRunIgnoresBookingsButNotCalendar(t *testing.T) {
	p := Problem{
		Resources: []Resource{res("r", "R", true, weekdayShift(8*60, 16*60))},
		Operations: []Operation{
			{ID: "a", OrderID: "o1", OperNumber: 10, Minutes: 60, Candidates: []string{"r"}},
			{ID: "b", OrderID: "o2", OperNumber: 10, Minutes: 60, Candidates: []string{"r"}},
		},
		Start: at(0, 6, 0), HorizonDays: 7, Infinite: true,
	}
	r := Schedule(p)
	if len(r.Placements) != 2 {
		t.Fatalf("placements = %+v", r.Placements)
	}
	for _, pl := range r.Placements {
		if !pl.Start.Equal(at(0, 8, 0)) {
			t.Fatalf("%s starts %v, want 08:00 (shift open, bookings ignored)", pl.OperationID, pl.Start)
		}
	}
}

func TestPlanMove_FirmedJobsAreFrozen(t *testing.T) {
	shifts := weekdayShift(8*60, 16*60)
	rs := []Resource{res("r", "R", true, shifts)}
	order := []Operation{
		{ID: "op1", OrderID: "o", OperNumber: 10, Minutes: 60, Candidates: []string{"r"}},
		{ID: "op2", OrderID: "o", OperNumber: 20, Minutes: 60, Candidates: []string{"r"}},
	}
	current := map[string]Placement{
		"op1": {OperationID: "op1", ResourceID: "r", Start: at(0, 8, 0), End: at(0, 9, 0), Segments: []Segment{{at(0, 8, 0), at(0, 9, 0)}}},
		"op2": {OperationID: "op2", ResourceID: "r", Start: at(0, 9, 0), End: at(0, 10, 0), Segments: []Segment{{at(0, 9, 0), at(0, 10, 0)}}},
	}
	booked := []Busy{
		{ResourceID: "r", Start: at(0, 8, 0), End: at(0, 9, 0), JobID: "op1"},
		{ResourceID: "r", Start: at(0, 9, 0), End: at(0, 10, 0), JobID: "op2"},
	}
	base := MoveInput{Resources: rs, Start: monday, HorizonDays: 14, Booked: booked, Order: order, Current: current, Moved: "op1", ResourceID: "r"}
	cases := []struct {
		name    string
		firmed  map[string]bool
		at      time.Time
		wantJob string
	}{
		{"moved job firmed", map[string]bool{"op1": true}, at(0, 11, 0), "op1"},
		{"firmed successor would be pushed", map[string]bool{"op2": true}, at(0, 11, 0), "op2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := base
			in.Firmed, in.At = c.firmed, c.at
			plan := PlanMove(in)
			if plan.Conflict == nil || plan.Conflict.Code != CodeFirmed || plan.Conflict.JobID != c.wantJob {
				t.Fatalf("plan = %+v, want %s on %s", plan, CodeFirmed, c.wantJob)
			}
		})
	}
	t.Run("firmed successor that need not move is kept", func(t *testing.T) {
		in := base
		in.Firmed = map[string]bool{"op2": true}
		in.Booked = []Busy{booked[1]}
		in.Current = map[string]Placement{"op1": current["op1"], "op2": {OperationID: "op2", ResourceID: "r", Start: at(0, 14, 0), End: at(0, 15, 0)}}
		in.At = at(0, 10, 0)
		plan := PlanMove(in)
		if plan.Conflict != nil || len(plan.Changes) != 1 || plan.Changes[0].OperationID != "op1" {
			t.Fatalf("plan = %+v, want only op1 to move", plan)
		}
	})
}

func TestWeeklyLoad_MidWeekFromCountsTheWholeWeek(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	r := Resource{ID: "r", Code: "R", Finite: true, Location: berlin, Shifts: allDays(0, 1440)}
	thursday := monday.AddDate(0, 0, 3)
	loads := WeeklyLoad(r, LoadIntervals(r, thursday, 1), nil, thursday, 1, 90, 3)
	if len(loads) != 1 || !loads[0].WeekStart.Equal(monday) || loads[0].OpenMinutes != 7*1440 {
		t.Fatalf("loads = %+v, want the full week of %d open minutes from Monday", loads, 7*1440)
	}
}
