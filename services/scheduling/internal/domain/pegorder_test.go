package domain

import (
	"strings"
	"testing"
	"time"
)

func pegProblem(ops []Operation, pegs []Peg) Problem {
	return Problem{Resources: []Resource{res("r1", "R1", true, weekdayShift(8*60, 16*60)), res("r2", "R2", true, weekdayShift(8*60, 16*60))},
		Operations: ops, Start: monday, HorizonDays: 30, Pegs: pegs}
}

func placedByOp(r Result) map[string]Placement {
	out := map[string]Placement{}
	for _, pl := range r.Placements {
		out[pl.OperationID] = pl
	}
	return out
}

func unplacedByOp(r Result) map[string]string {
	out := map[string]string{}
	for _, u := range r.Unplaced {
		out[u.OperationID] = u.Reason
	}
	return out
}

func TestSchedulePegs_ParentPlacedFirstEvenWhenDependentIsDueSooner(t *testing.T) {
	// Order plan alone puts the dependent first (earlier due date) and it takes
	// Monday 08:00 on the shared resource; the peg puts the parent first.
	ops := []Operation{
		{ID: "dep", OrderID: "a", OperNumber: 10, Minutes: 60, DueAt: at(1, 16, 0), Candidates: []string{"r1"}},
		{ID: "par", OrderID: "b", OperNumber: 10, Minutes: 240, DueAt: at(4, 16, 0), Candidates: []string{"r1"}},
	}
	free := placedByOp(Schedule(pegProblem(ops, nil)))
	if !free["dep"].Start.Equal(at(0, 8, 0)) {
		t.Fatalf("without pegs the dependent starts %v, want Monday 08:00", free["dep"].Start)
	}
	got := placedByOp(Schedule(pegProblem(ops, []Peg{{ParentOrderID: "b", DependentOrderID: "a"}})))
	if !got["par"].Start.Equal(at(0, 8, 0)) || !got["dep"].Start.Equal(at(0, 12, 0)) {
		t.Fatalf("parent %v, dependent %v: want parent Monday 08:00 and dependent at its end 12:00", got["par"].Start, got["dep"].Start)
	}
}

func TestSchedulePegs_DependentWaitsForTheLatestOfSeveralParents(t *testing.T) {
	ops := []Operation{
		{ID: "dep", OrderID: "a", OperNumber: 10, Minutes: 60, Candidates: []string{"r1"}},
		{ID: "p1", OrderID: "b", OperNumber: 10, Minutes: 120, Candidates: []string{"r1"}},
		{ID: "p2", OrderID: "c", OperNumber: 10, Minutes: 300, Candidates: []string{"r2"}},
	}
	got := placedByOp(Schedule(pegProblem(ops, []Peg{{ParentOrderID: "b", DependentOrderID: "a"}, {ParentOrderID: "c", DependentOrderID: "a"}})))
	if !got["p2"].End.Equal(at(0, 13, 0)) || !got["dep"].Start.Equal(got["p2"].End) {
		t.Fatalf("dependent starts %v, parents end %v and %v: want the later end 13:00", got["dep"].Start, got["p1"].End, got["p2"].End)
	}
}

func TestSchedulePegs_FloorFromParentOutsideRunAndFrozenParent(t *testing.T) {
	ops := []Operation{
		{ID: "d1", OrderID: "a", OperNumber: 10, Minutes: 60, Candidates: []string{"r1"}},
		{ID: "d2", OrderID: "c", OperNumber: 10, Minutes: 60, Candidates: []string{"r2"}},
		{ID: "d3", OrderID: "e", OperNumber: 10, Minutes: 60, Candidates: []string{"r2"}, ReadyAt: at(3, 8, 0)},
	}
	p := pegProblem(ops, []Peg{{ParentOrderID: "x", DependentOrderID: "a"}, {ParentOrderID: "f", DependentOrderID: "c"},
		{ParentOrderID: "x", DependentOrderID: "e"}, {ParentOrderID: "never", DependentOrderID: "e"}})
	// x is outside the run and ends Tuesday 10:00; f is a parent whose only job
	// is frozen and ends Wednesday 09:30; "never" has no jobs and sets no floor.
	p.ParentEnds = map[string]time.Time{"x": at(1, 10, 0)}
	p.Fixed = []Placement{{OperationID: "f10", OrderID: "f", OperNumber: 10, ResourceID: "r1", Start: at(2, 8, 0), End: at(2, 9, 30)}}
	got := placedByOp(Schedule(p))
	if !got["d1"].Start.Equal(at(1, 10, 0)) {
		t.Fatalf("d1 starts %v, want Tuesday 10:00 when the outside parent ends", got["d1"].Start)
	}
	if !got["d2"].Start.Equal(at(2, 9, 30)) {
		t.Fatalf("d2 starts %v, want Wednesday 09:30 when the frozen parent ends", got["d2"].Start)
	}
	if !got["d3"].Start.Equal(at(3, 8, 0)) {
		t.Fatalf("d3 starts %v, want its own later ready time Thursday 08:00", got["d3"].Start)
	}
}

func TestSchedulePegs_UnplacedParentLeavesDependentsUnplaced(t *testing.T) {
	// b needs more minutes than the 30-day horizon holds, so it is unplaced; a
	// is pegged to b and c to a, so both follow. d is pegged to a parent outside
	// the run that holds an unplaced operation.
	ops := []Operation{
		{ID: "par", OrderID: "b", OperNumber: 10, Minutes: 100000, Candidates: []string{"r1"}},
		{ID: "dep", OrderID: "a", OperNumber: 10, Minutes: 60, Candidates: []string{"r2"}},
		{ID: "grand", OrderID: "c", OperNumber: 10, Minutes: 60, Candidates: []string{"r2"}},
		{ID: "out", OrderID: "d", OperNumber: 10, Minutes: 60, Candidates: []string{"r2"}},
		{ID: "free", OrderID: "z", OperNumber: 10, Minutes: 60, Candidates: []string{"r2"}},
	}
	p := pegProblem(ops, []Peg{{ParentOrderID: "b", DependentOrderID: "a"}, {ParentOrderID: "a", DependentOrderID: "c"},
		{ParentOrderID: "y", DependentOrderID: "d"}})
	p.UnplacedParents = map[string]bool{"y": true}
	r := Schedule(p)
	un := unplacedByOp(r)
	if !strings.Contains(un["dep"], "pegged parent order b is unplaced") {
		t.Fatalf("dep reason = %q, want the unplaced parent b", un["dep"])
	}
	if !strings.Contains(un["grand"], "pegged parent order a is unplaced") {
		t.Fatalf("grand reason = %q, want the unplaced parent a", un["grand"])
	}
	if !strings.Contains(un["out"], "pegged parent order y is unplaced") {
		t.Fatalf("out reason = %q, want the outside unplaced parent y", un["out"])
	}
	if _, ok := placedByOp(r)["free"]; !ok {
		t.Fatalf("an unpegged order must still be placed: unplaced = %v", un)
	}
}

func TestSchedulePegs_BackwardKeepsDependentBehindParent(t *testing.T) {
	// Backward puts the parent Wednesday 12:00-16:00 against its due date. The
	// dependent, also due Wednesday 16:00, would run 14:00-16:00 backward; the
	// peg floors it at 16:00, it cannot meet its due date and falls back to a
	// late forward schedule (SCHED-0003) starting Thursday 08:00.
	ops := []Operation{
		{ID: "par", OrderID: "b", OperNumber: 10, Minutes: 240, DueAt: at(2, 16, 0), Candidates: []string{"r1"}},
		{ID: "dep", OrderID: "a", OperNumber: 10, Minutes: 120, DueAt: at(2, 16, 0), Candidates: []string{"r2"}},
	}
	p := pegProblem(ops, []Peg{{ParentOrderID: "b", DependentOrderID: "a"}})
	p.Backward = true
	r := Schedule(p)
	got := placedByOp(r)
	if !got["par"].Start.Equal(at(2, 12, 0)) {
		t.Fatalf("parent starts %v, want Wednesday 12:00 (backward)", got["par"].Start)
	}
	if !got["dep"].Start.Equal(at(3, 8, 0)) || got["dep"].LateMinutes <= 0 {
		t.Fatalf("dependent %v late %d, want Thursday 08:00 and late", got["dep"].Start, got["dep"].LateMinutes)
	}
	if len(r.Warnings) != 1 || r.Warnings[0].Code != CodeLateFallback || r.Warnings[0].OperationID != "dep" {
		t.Fatalf("warnings = %+v, want SCHED-0003 on the dependent", r.Warnings)
	}
}

func TestSchedulePegs_LoopDoesNotStallTheRun(t *testing.T) {
	// The service refuses looping pegs (SCHED-0121); if one is stored anyway
	// the run still places every order.
	ops := []Operation{
		{ID: "o1", OrderID: "a", OperNumber: 10, Minutes: 60, Candidates: []string{"r1"}},
		{ID: "o2", OrderID: "b", OperNumber: 10, Minutes: 60, Candidates: []string{"r2"}},
	}
	r := Schedule(pegProblem(ops, []Peg{{ParentOrderID: "a", DependentOrderID: "b"}, {ParentOrderID: "b", DependentOrderID: "a"}}))
	if len(r.Placements) != 2 || len(r.Unplaced) != 0 {
		t.Fatalf("placements %d unplaced %v, want both placed", len(r.Placements), r.Unplaced)
	}
}
