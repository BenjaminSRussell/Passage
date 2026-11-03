package domain

import (
	"testing"
	"time"
)

func backwardProblem(ops []Operation, busy []Busy, backward bool) Problem {
	return Problem{Resources: []Resource{res("r1", "R1", true, weekdayShift(8*60, 16*60))}, Operations: ops, Booked: busy,
		Start: monday, HorizonDays: 30, Backward: backward}
}

func TestBackward_StartsAsLateAsTheDueDateAllows(t *testing.T) {
	// 4 h of work due Wednesday 12:00 on an 08:00-16:00 calendar: the job runs
	// Wednesday 08:00-12:00, not Monday morning.
	op := Operation{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 240, DueAt: at(2, 12, 0), Candidates: []string{"r1"}}
	got := Schedule(backwardProblem([]Operation{op}, nil, true))
	if len(got.Placements) != 1 {
		t.Fatalf("placements = %d, unplaced = %v", len(got.Placements), got.Unplaced)
	}
	pl := got.Placements[0]
	if !pl.Start.Equal(at(2, 8, 0)) || !pl.End.Equal(at(2, 12, 0)) || pl.LateMinutes != 0 {
		t.Fatalf("placed %v-%v late %d, want Wed 08:00-12:00", pl.Start, pl.End, pl.LateMinutes)
	}
	fwd := Schedule(backwardProblem([]Operation{op}, nil, false))
	if !fwd.Placements[0].Start.Equal(at(0, 8, 0)) {
		t.Fatalf("forward start = %v, want Monday 08:00", fwd.Placements[0].Start)
	}
}

func TestBackward_ChainEndsOnDueDateAndKeepsPrecedence(t *testing.T) {
	ops := []Operation{
		{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 240, DueAt: at(3, 16, 0), Candidates: []string{"r1"}},
		{ID: "o2", OrderID: "a", OperNumber: 20, Minutes: 480, DueAt: at(3, 16, 0), Candidates: []string{"r1"}},
	}
	got := Schedule(backwardProblem(ops, nil, true))
	if len(got.Placements) != 2 {
		t.Fatalf("placements = %d", len(got.Placements))
	}
	o1, o2 := got.Placements[0], got.Placements[1]
	if !o2.End.Equal(at(3, 16, 0)) {
		t.Fatalf("last operation ends %v, want Thursday 16:00", o2.End)
	}
	if o2.Start.Before(o1.End) {
		t.Fatalf("operation 20 starts %v before operation 10 ends %v", o2.Start, o1.End)
	}
	if !o1.Start.Equal(at(2, 12, 0)) {
		t.Fatalf("operation 10 starts %v, want Wednesday 12:00 (the latest start)", o1.Start)
	}
}

func TestBackward_AvoidsBookedWorkAndStaysInsideDue(t *testing.T) {
	busy := []Busy{{ResourceID: "r1", Start: at(2, 10, 0), End: at(2, 12, 0), JobID: "other"}}
	op := Operation{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 120, DueAt: at(2, 12, 0), Candidates: []string{"r1"}}
	got := Schedule(backwardProblem([]Operation{op}, busy, true))
	pl := got.Placements[0]
	if !pl.End.Equal(at(2, 10, 0)) || !pl.Start.Equal(at(2, 8, 0)) {
		t.Fatalf("placed %v-%v, want Wednesday 08:00-10:00 before the booking", pl.Start, pl.End)
	}
}

func TestBackward_FallsBackToForwardAndFlagsLate(t *testing.T) {
	// 24 h of work due Monday 12:00 cannot finish: forward from the start, late.
	op := Operation{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 24 * 60, DueAt: at(0, 12, 0), Candidates: []string{"r1"}}
	got := Schedule(backwardProblem([]Operation{op}, nil, true))
	if len(got.Placements) != 1 {
		t.Fatalf("placements = %d", len(got.Placements))
	}
	pl := got.Placements[0]
	if !pl.Start.Equal(at(0, 8, 0)) || pl.LateMinutes <= 0 {
		t.Fatalf("start %v late %d, want Monday 08:00 and late", pl.Start, pl.LateMinutes)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Code != CodeLateFallback {
		t.Fatalf("warnings = %+v, want SCHED-0003", got.Warnings)
	}
}

func TestBackward_TooCloseToNowIsForwardWithoutWarning(t *testing.T) {
	// Latest start is Monday 08:00, less than an hour after the 07:30 run start.
	op := Operation{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 240, DueAt: at(0, 12, 0), Candidates: []string{"r1"}}
	p := backwardProblem([]Operation{op}, nil, true)
	p.Start = at(0, 7, 30)
	got := Schedule(p)
	if len(got.Placements) != 1 || len(got.Warnings) != 0 || got.Placements[0].LateMinutes != 0 {
		t.Fatalf("got %+v", got)
	}
	if !got.Placements[0].Start.Equal(at(0, 8, 0)) {
		t.Fatalf("start = %v", got.Placements[0].Start)
	}
}

func TestBackward_OrderWithoutDueDateIsForward(t *testing.T) {
	op := Operation{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 60, Candidates: []string{"r1"}}
	got := Schedule(backwardProblem([]Operation{op}, nil, true))
	if len(got.Placements) != 1 || !got.Placements[0].Start.Equal(at(0, 8, 0)) || len(got.Warnings) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestBackward_LeavesNoBookingBehindFromProbes(t *testing.T) {
	// Two orders on one resource: probing the first must not leave bookings
	// that push the second.
	ops := []Operation{
		{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 240, DueAt: at(2, 12, 0), Candidates: []string{"r1"}},
		{ID: "o2", OrderID: "b", OperNumber: 10, Minutes: 240, DueAt: at(3, 12, 0), Candidates: []string{"r1"}},
	}
	got := Schedule(backwardProblem(ops, nil, true))
	if len(got.Placements) != 2 {
		t.Fatalf("placements = %d", len(got.Placements))
	}
	for _, pl := range got.Placements {
		switch pl.OrderID {
		case "a":
			if !pl.End.Equal(at(2, 12, 0)) {
				t.Fatalf("a ends %v", pl.End)
			}
		case "b":
			if !pl.End.Equal(at(3, 12, 0)) {
				t.Fatalf("b ends %v", pl.End)
			}
		}
	}
}

// TestBackward_FeasibleAndUnderBudgetAtScale runs AC-23.4's 5,000 operations
// backward: probing each order's latest start must stay inside the 60 s
// budget, leave no double booking, and every on-time order must end by its
// due date while every late one carries SCHED-0003.
func TestBackward_FeasibleAndUnderBudgetAtScale(t *testing.T) {
	if testing.Short() {
		t.Skip("performance check skipped in -short mode")
	}
	p := randomProblem(7, 200, 5000, 1000, 180)
	p.Backward = true
	began := time.Now()
	r := Schedule(p)
	if elapsed := time.Since(began); elapsed > 60*time.Second {
		t.Fatalf("5000 operations backward took %s, budget is 60s", elapsed)
	}
	checkFeasible(t, p, r)
	if len(r.Placements) == 0 {
		t.Fatal("no operations placed")
	}
	late := map[string]bool{}
	for _, pl := range r.Placements {
		if pl.LateMinutes > 0 {
			late[pl.OrderID] = true
		}
	}
	flagged := map[string]bool{}
	for _, w := range r.Warnings {
		if w.Code == CodeLateFallback {
			flagged[w.OperationID] = true
		}
	}
	lastOp := map[string]string{}
	for _, op := range orderPlan(p.Operations) {
		lastOp[op.OrderID] = op.ID
	}
	for order := range late {
		if !flagged[lastOp[order]] {
			t.Fatalf("order %s is late without SCHED-0003", order)
		}
	}
}

// BR-23.1 with BR-23.2: on a resource at 200 percent 240 process minutes book
// 120, so the job due Wednesday 12:00 starts at 10:00, not at 08:00 as the
// unstretched minutes would bound it.
func TestBackward_FastResourceStartsLater(t *testing.T) {
	p := backwardProblem([]Operation{{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 240, DueAt: at(2, 12, 0), Candidates: []string{"r1"}}}, nil, true)
	p.Resources[0].EfficiencyPct = 200
	got := Schedule(p)
	if len(got.Placements) != 1 || !got.Placements[0].Start.Equal(at(2, 10, 0)) || !got.Placements[0].End.Equal(at(2, 12, 0)) {
		t.Fatalf("placements = %+v, want Wed 10:00-12:00", got.Placements)
	}
}
