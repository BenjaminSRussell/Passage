package domain

import (
	"strings"
	"testing"
	"time"
)

func TestDayCeil(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	mid := time.Date(2027, 3, 8, 0, 0, 0, 0, ny).UTC()
	if got := DayCeil(mid, ny); !got.Equal(mid) {
		t.Fatalf("midnight stays: %v", got)
	}
	if got, want := DayCeil(mid.Add(time.Minute), ny), time.Date(2027, 3, 9, 0, 0, 0, 0, ny); !got.Equal(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	// The spring-forward day is 23 hours: 2027-03-14 in New York.
	d := time.Date(2027, 3, 14, 6, 0, 0, 0, ny)
	if got, want := DayCeil(d, ny), time.Date(2027, 3, 15, 0, 0, 0, 0, ny); !got.Equal(want) || got.Sub(time.Date(2027, 3, 14, 0, 0, 0, 0, ny)) != 23*time.Hour {
		t.Fatalf("DST day: got %v want %v", got, want)
	}
	if got := DayCeil(mid.Add(time.Minute), nil); got.Hour() != 0 {
		t.Fatalf("nil location is UTC: %v", got)
	}
}

func dailyChain() ([]Resource, []Operation) {
	shifts := weekdayShift(8*60, 16*60)
	r := res("r", "R", true, shifts)
	ops := []Operation{
		{ID: "a", OrderID: "o", OperNumber: 10, Minutes: 60, Candidates: []string{"r"}},
		{ID: "b", OrderID: "o", OperNumber: 20, Minutes: 60, Candidates: []string{"r"}},
		{ID: "c", OrderID: "o", OperNumber: 30, Minutes: 60, Candidates: []string{"r"}},
	}
	return []Resource{r}, ops
}

func TestSchedule_JobsModeChainsWithinTheDay(t *testing.T) {
	rs, ops := dailyChain()
	r := Schedule(Problem{Resources: rs, Operations: ops, Start: monday, HorizonDays: 14})
	if len(r.Placements) != 3 || !r.Placements[2].Start.Equal(at(0, 10, 0)) {
		t.Fatalf("jobs mode runs the chain back to back: %+v", r.Placements)
	}
}

func TestSchedule_OperationsModeStartsFollowersOnDayBoundaries(t *testing.T) {
	rs, ops := dailyChain()
	r := Schedule(Problem{Resources: rs, Operations: ops, Start: monday, HorizonDays: 14, DailyBuckets: true})
	if len(r.Placements) != 3 || len(r.Unplaced) != 0 {
		t.Fatalf("placements %+v unplaced %+v", r.Placements, r.Unplaced)
	}
	for i, wantDay := range []int{0, 1, 2} {
		p := r.Placements[i]
		if !p.Start.Equal(at(wantDay, 8, 0)) || !p.End.Equal(at(wantDay, 9, 0)) {
			t.Fatalf("op %d = %v-%v, want day %d 08:00-09:00", i, p.Start, p.End, wantDay)
		}
	}
}

func TestSchedule_OperationsModeKeepsTheMinutesAndBackwardWorks(t *testing.T) {
	rs, ops := dailyChain()
	ops[0].Minutes = 600 // splits across two days of 8 h
	r := Schedule(Problem{Resources: rs, Operations: ops, Start: monday, HorizonDays: 14, DailyBuckets: true})
	if len(r.Placements) != 3 {
		t.Fatalf("%+v", r.Placements)
	}
	first := r.Placements[0]
	if m := spanMinutes(first.Segments); m != 600 || len(first.Segments) != 2 {
		t.Fatalf("first op books %d min in %d segments", m, len(first.Segments))
	}
	if s := r.Placements[1].Start; !s.Equal(at(2, 8, 0)) {
		t.Fatalf("second op starts %v, want the day after the first ends", s)
	}
	for _, p := range r.Placements[1:] {
		if p.Start.Hour() != 8 || p.Start.Minute() != 0 {
			t.Fatalf("follower not at the start of the open day: %v", p.Start)
		}
	}
	due := at(6, 17, 0)
	for i := range ops {
		ops[i].DueAt = due
	}
	b := Schedule(Problem{Resources: rs, Operations: ops, Start: monday, HorizonDays: 14, DailyBuckets: true, Backward: true})
	if len(b.Placements) != 3 || len(b.Unplaced) != 0 {
		t.Fatalf("backward: %+v", b)
	}
	// Latest start that still ends by Sunday 17:00: c Friday 08:00, b
	// Thursday 08:00, so a must end by Wednesday; its 600 minutes start
	// Tuesday 14:00 (a minute later it ends Thursday and the chain is late).
	for i, want := range []time.Time{at(1, 14, 0), at(3, 8, 0), at(4, 8, 0)} {
		if p := b.Placements[i]; !p.Start.Equal(want) || p.End.After(due) {
			t.Fatalf("backward op %d = %v-%v, want start %v and end by %v", i, p.Start, p.End, want, due)
		}
	}
}

// A follower whose predecessor ended on an earlier day (a fixed or firmed
// job) starts at the run start: only what the predecessor sets is moved to a
// day boundary, not the run start itself.
func TestSchedule_OperationsModeDoesNotRoundTheRunStart(t *testing.T) {
	rs := []Resource{res("r", "R", true, weekdayShift(8*60, 16*60))}
	b := Operation{ID: "b", OrderID: "o", OperNumber: 20, Minutes: 60, Candidates: []string{"r"}}
	for _, tc := range []struct {
		name     string
		fixedEnd time.Time
		want     time.Time
	}{
		{"predecessor ended last week", at(-3, 9, 0), at(0, 10, 0)},
		{"predecessor ended earlier the same day", at(0, 9, 0), at(1, 8, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixed := Placement{OperationID: "a", OrderID: "o", OperNumber: 10, ResourceID: "r", Start: tc.fixedEnd.Add(-time.Hour), End: tc.fixedEnd}
			got := Schedule(Problem{Resources: rs, Operations: []Operation{b}, Fixed: []Placement{fixed}, Start: at(0, 10, 0), HorizonDays: 14, DailyBuckets: true})
			if len(got.Placements) != 1 || !got.Placements[0].Start.Equal(tc.want) {
				t.Fatalf("b = %+v unplaced %v, want start %v", got.Placements, got.Unplaced, tc.want)
			}
		})
	}
}

// A predecessor that ends exactly at midnight has used only the day before:
// the follower starts at that midnight, not a day later. One minute past it
// costs the whole day.
func TestSchedule_OperationsModeAtMidnight(t *testing.T) {
	rs := []Resource{res("r", "R", true, allDays(0, 24*60))}
	for _, tc := range []struct {
		minutes int
		want    time.Time
	}{{24 * 60, at(1, 0, 0)}, {24*60 + 1, at(2, 0, 0)}} {
		ops := []Operation{
			{ID: "a", OrderID: "o", OperNumber: 10, Minutes: tc.minutes, Candidates: []string{"r"}},
			{ID: "b", OrderID: "o", OperNumber: 20, Minutes: 60, Candidates: []string{"r"}},
		}
		got := Schedule(Problem{Resources: rs, Operations: ops, Start: monday, HorizonDays: 14, DailyBuckets: true})
		if len(got.Placements) != 2 || !got.Placements[1].Start.Equal(tc.want) {
			t.Fatalf("a of %d min: b = %+v, want start %v", tc.minutes, got.Placements, tc.want)
		}
	}
}

// Queue times (FR-23.3) are elapsed minutes added before the day boundary is
// taken: a queue that reaches past midnight moves the follower a further day.
func TestSchedule_OperationsModeQueueTimeCountsBeforeTheDayBoundary(t *testing.T) {
	rs := []Resource{res("r", "R", true, weekdayShift(8*60, 16*60))}
	for _, tc := range []struct {
		queue int
		want  time.Time
	}{{15 * 60, at(1, 8, 0)}, {15*60 + 1, at(2, 8, 0)}} {
		ops := []Operation{
			{ID: "a", OrderID: "o", OperNumber: 10, Minutes: 60, QueueAfter: tc.queue, Candidates: []string{"r"}},
			{ID: "b", OrderID: "o", OperNumber: 20, Minutes: 60, Candidates: []string{"r"}},
		}
		got := Schedule(Problem{Resources: rs, Operations: ops, Start: monday, HorizonDays: 14, DailyBuckets: true})
		if len(got.Placements) != 2 || !got.Placements[1].Start.Equal(tc.want) {
			t.Fatalf("queue %d min: b = %+v, want start %v", tc.queue, got.Placements, tc.want)
		}
	}
}

// The day boundary is the follower's candidate resource's local midnight, not
// the predecessor's: Monday 10:00 UTC is 05:00 in New York, so the next day
// there starts Tuesday 05:00 UTC.
func TestSchedule_OperationsModeUsesTheFollowersTimeZone(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	nyRes := res("ny", "NY", true, allDays(0, 24*60))
	nyRes.Location = ny
	rs := []Resource{res("utc", "UTC", true, allDays(0, 24*60)), nyRes}
	ops := []Operation{
		{ID: "a", OrderID: "o", OperNumber: 10, Minutes: 600, Candidates: []string{"utc"}},
		{ID: "b", OrderID: "o", OperNumber: 20, Minutes: 60, Candidates: []string{"ny"}},
	}
	got := Schedule(Problem{Resources: rs, Operations: ops, Start: monday, HorizonDays: 14, DailyBuckets: true})
	if len(got.Placements) != 2 || !got.Placements[1].Start.Equal(at(1, 5, 0)) {
		t.Fatalf("b = %+v, want Tuesday 05:00 UTC (midnight in New York)", got.Placements)
	}
}

// Overlap (BR-23.5) still applies in operations mode, at day granularity: the
// follower starts on the day after the overlap share is through, which can be
// while the predecessor still runs, and never ends before it.
func TestSchedule_OperationsModeKeepsOverlapByTheDay(t *testing.T) {
	p := overlapProblem(1200, 2500)
	p.Operations[0].Minutes = 1200 // Monday to Wednesday 12:00; 25% is through Monday 13:00
	p.DailyBuckets = true
	got := Schedule(p)
	if len(got.Placements) != 2 {
		t.Fatalf("placements %+v unplaced %v", got.Placements, got.Unplaced)
	}
	o1, o2 := got.Placements[0], got.Placements[1]
	if !o1.End.Equal(at(2, 12, 0)) || !o2.Start.Equal(at(1, 8, 0)) || o2.End.Before(o1.End) {
		t.Fatalf("o1 %v-%v, o2 %v-%v: want o2 from Tuesday 08:00 and not ending before o1", o1.Start, o1.End, o2.Start, o2.End)
	}
	p.Operations[1].Minutes = 60 // would end before o1: pushed to the day after o1 ends
	got = Schedule(p)
	if o2 := got.Placements[1]; !o2.Start.Equal(at(3, 8, 0)) {
		t.Fatalf("short follower starts %v, want Thursday 08:00", o2.Start)
	}
}

// A network's maximum wait (BR-23.11) still holds in operations mode: the
// filler starts at midnight, so the mixing is delayed to end within 120
// minutes before it. On a calendar closed overnight no pair meets the wait
// and the order is NETWORK_INFEASIBLE.
func TestSchedule_OperationsModeKeepsTheNetworkWait(t *testing.T) {
	resources, net := mixFill(t)
	p := Problem{Resources: resources, Operations: []Operation{
		{ID: "mix", OrderID: "o1", OperNumber: 10, Minutes: 60, ReadyAt: at(0, 8, 0), Candidates: []string{"V1"}},
		{ID: "fill", OrderID: "o1", OperNumber: 20, Minutes: 60, Network: "MIXFILL", Candidates: []string{"F3", "F2", "F1"}},
	}, Networks: []ResourceNetwork{net}, Start: monday, HorizonDays: 14, DailyBuckets: true}
	r := Schedule(p)
	if len(r.Unplaced) != 0 {
		t.Fatalf("unplaced: %v", r.Unplaced)
	}
	mix, fill := placementOf(t, r, "mix"), placementOf(t, r, "fill")
	if !fill.Start.Equal(at(1, 0, 0)) || fill.Start.Sub(mix.End) > 120*time.Minute {
		t.Fatalf("mix %v-%v, fill %v: want fill at Tuesday 00:00 within 120 min of mixing", mix.Start, mix.End, fill.Start)
	}
	for i := range p.Resources {
		p.Resources[i].Shifts = weekdayShift(8*60, 16*60)
	}
	r = Schedule(p)
	if len(r.Unplaced) != 2 || !strings.Contains(r.Unplaced[1].Reason, FlagNetworkInfeasible) {
		t.Fatalf("closed overnight: want both unplaced as network infeasible, got %+v", r.Unplaced)
	}
}

// moveChain places a, b, c on separate days (operations mode) and returns the
// input for moving one of them.
func moveChain(t *testing.T, daily bool) MoveInput {
	t.Helper()
	rs, ops := dailyChain()
	r := Schedule(Problem{Resources: rs, Operations: ops, Start: monday, HorizonDays: 14, DailyBuckets: daily})
	cur := map[string]Placement{}
	for _, p := range r.Placements {
		cur[p.OperationID] = p
	}
	return MoveInput{Resources: rs, Start: monday, HorizonDays: 14, Order: ops, Current: cur, Moved: "b", ResourceID: "r", DailyBuckets: daily}
}

func TestPlanMove_OperationsModeKeepsTheFollowerOffThePredecessorsDay(t *testing.T) {
	in := moveChain(t, true) // a Mon 08-09, b Tue 08-09, c Wed 08-09
	in.At = at(0, 10, 0)     // the planner drops b on a's day
	plan := PlanMove(in)
	if plan.Conflict != nil || len(plan.Changes) == 0 {
		t.Fatalf("plan %+v", plan)
	}
	if got := plan.Changes[0]; !got.Start.Equal(at(1, 8, 0)) {
		t.Fatalf("b = %v, want Tuesday 08:00 (first open minute after a's day)", got.Start)
	}
	// Jobs mode lets it sit behind a on the same day, as before.
	jobs := moveChain(t, false)
	jobs.At = at(0, 10, 0)
	if p := PlanMove(jobs); p.Conflict != nil || !p.Changes[0].Start.Equal(at(0, 10, 0)) {
		t.Fatalf("jobs mode: %+v", p)
	}
}

func TestPlanMove_OperationsModeAskedStartLaterIsKept(t *testing.T) {
	in := moveChain(t, true)
	in.At = at(1, 12, 0)
	plan := PlanMove(in)
	if plan.Conflict != nil || !plan.Changes[0].Start.Equal(at(1, 12, 0)) {
		t.Fatalf("plan %+v", plan)
	}
	// c is already on the day after b's day (Wed), so nothing is pushed.
	if len(plan.Changes) != 1 {
		t.Fatalf("changes %+v", plan.Changes)
	}
}

func TestPlanMove_OperationsModePushesSuccessorsAWholeDay(t *testing.T) {
	in := moveChain(t, true)
	in.At = at(2, 9, 0) // b to Wednesday 09:00, where c sits at 08:00
	plan := PlanMove(in)
	if plan.Conflict != nil || len(plan.Changes) != 2 {
		t.Fatalf("plan %+v", plan)
	}
	if c := plan.Changes[1]; c.OperationID != "c" || !c.Start.Equal(at(3, 8, 0)) {
		t.Fatalf("c = %+v, want Thursday 08:00 (not Wednesday behind b)", c)
	}
	jobs := moveChain(t, false)
	jobs.At = at(2, 9, 0)
	p := PlanMove(jobs)
	if p.Conflict != nil || len(p.Changes) != 2 || !p.Changes[1].Start.Equal(at(2, 10, 0)) {
		t.Fatalf("jobs mode pushes behind b on the same day: %+v", p)
	}
}

func TestPlanMove_OperationsModeFirmedSuccessorOnTheSameDayRefuses(t *testing.T) {
	in := moveChain(t, true)
	in.At = at(2, 9, 0)
	in.Firmed = map[string]bool{"c": true}
	if plan := PlanMove(in); plan.Conflict == nil || plan.Conflict.Code != CodeFirmed {
		t.Fatalf("plan %+v", plan)
	}
}

func TestPlanMove_OperationsModeDependentsStartAfterTheirParentsThenByTheDay(t *testing.T) {
	rs, ops := dailyChain()
	dep := []Operation{
		{ID: "d1", OrderID: "p", OperNumber: 10, Minutes: 60, Candidates: []string{"r"}},
		{ID: "d2", OrderID: "p", OperNumber: 20, Minutes: 60, Candidates: []string{"r"}},
	}
	// d1 Thu 08:00, d2 Fri 08:00 in a plan made in operations mode.
	cur := map[string]Placement{
		"a": pl(ops[0], "r", at(0, 8, 0), 0), "b": pl(ops[1], "r", at(1, 8, 0), 0), "c": pl(ops[2], "r", at(2, 8, 0), 0),
		"d1": pl(dep[0], "r", at(3, 8, 0), 0), "d2": pl(dep[1], "r", at(4, 8, 0), 0),
	}
	in := MoveInput{Resources: rs, Start: monday, HorizonDays: 21, Order: ops, Current: cur, Moved: "b", ResourceID: "r",
		At: at(3, 9, 0), Dependents: dep, Pegs: []Peg{{ParentOrderID: "o", DependentOrderID: "p"}}, DailyBuckets: true}
	plan := PlanMove(in)
	if plan.Conflict != nil {
		t.Fatalf("plan %+v", plan)
	}
	got := map[string]time.Time{}
	for _, c := range plan.Changes {
		got[c.OperationID] = c.Start
	}
	// b ends Thu 10:00, c moves to Fri 08:00 (order o ends Fri 09:00), so p's
	// first operation (peg floor, not rounded) starts Fri 09:00 and d2 the day after.
	if !got["c"].Equal(at(4, 8, 0)) || !got["d1"].Equal(at(4, 9, 0)) || !got["d2"].Equal(at(7, 8, 0)) {
		t.Fatalf("starts %+v", got)
	}
}

// A predecessor that ends exactly at midnight is already on a day boundary:
// the moved follower still goes to the first open minute of that day instead
// of the move being refused for starting in closed time.
func TestPlanMove_OperationsModePredecessorEndingAtMidnight(t *testing.T) {
	rs := []Resource{res("night", "NIGHT", true, allDays(0, 24*60)), res("r", "R", true, weekdayShift(8*60, 16*60))}
	ops := []Operation{
		{ID: "a", OrderID: "o", OperNumber: 10, Minutes: 60, Candidates: []string{"night"}},
		{ID: "b", OrderID: "o", OperNumber: 20, Minutes: 60, Candidates: []string{"r"}},
	}
	cur := map[string]Placement{"a": pl(ops[0], "night", at(0, 23, 0), 0), "b": pl(ops[1], "r", at(1, 9, 0), 0)}
	in := MoveInput{Resources: rs, Start: monday, HorizonDays: 14, Order: ops, Current: cur, Moved: "b", ResourceID: "r", At: at(0, 10, 0), DailyBuckets: true}
	if plan := PlanMove(in); plan.Conflict != nil || !plan.Changes[0].Start.Equal(at(1, 8, 0)) {
		t.Fatalf("plan %+v, want b at Tuesday 08:00", plan)
	}
	// Asked inside closed time after the floor, the planner's start is kept
	// and refused as in jobs mode.
	in.At = at(1, 6, 0)
	if plan := PlanMove(in); plan.Conflict == nil || plan.Conflict.Code != CodeOutsideCapacity {
		t.Fatalf("closed asked start: %+v", plan)
	}
}

// Day boundaries are the resource's local midnights across a DST change: on
// 2026-03-08 New York springs forward, so that day is 23 hours long and the
// next midnight is 04:00 UTC, not 05:00.
func TestPlanMove_OperationsModeAcrossDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tzdata")
	}
	r := res("ny", "NY", true, allDays(0, 24*60))
	r.Location = ny
	local := func(d, hh, mm int) time.Time { return time.Date(2026, 3, d, hh, mm, 0, 0, ny) }
	ops := []Operation{
		{ID: "a", OrderID: "o", OperNumber: 10, Minutes: 60, Candidates: []string{"ny"}},
		{ID: "b", OrderID: "o", OperNumber: 20, Minutes: 60, Candidates: []string{"ny"}},
		{ID: "c", OrderID: "o", OperNumber: 30, Minutes: 60, Candidates: []string{"ny"}},
	}
	// a Saturday 10:00, b Sunday (the short day) 00:00, c Monday 00:00, local.
	cur := map[string]Placement{"a": pl(ops[0], "ny", local(7, 10, 0), 0), "b": pl(ops[1], "ny", local(8, 0, 0), 0), "c": pl(ops[2], "ny", local(9, 0, 0), 0)}
	in := MoveInput{Resources: []Resource{r}, Start: local(1, 0, 0), HorizonDays: 30, Order: ops, Current: cur, Moved: "b", ResourceID: "ny",
		At: local(7, 12, 0), DailyBuckets: true}
	if plan := PlanMove(in); plan.Conflict != nil || !plan.Changes[0].Start.Equal(local(8, 0, 0)) || plan.Changes[0].Start.UTC().Hour() != 5 {
		t.Fatalf("b dropped on a's day: %+v, want Sunday 00:00 EST (05:00 UTC)", plan)
	}
	// b to Sunday 22:30 EDT ends 23:30, still Sunday: c stays at Monday 00:00 EDT (04:00 UTC).
	in.At = local(8, 22, 30)
	if plan := PlanMove(in); plan.Conflict != nil || len(plan.Changes) != 1 {
		t.Fatalf("b late on Sunday: %+v", plan)
	}
	// b to Sunday 23:30 ends Monday 00:30: c moves to Tuesday 00:00 EDT.
	in.At = local(8, 23, 30)
	plan := PlanMove(in)
	if plan.Conflict != nil || len(plan.Changes) != 2 || !plan.Changes[1].Start.Equal(local(10, 0, 0)) || plan.Changes[1].Start.UTC().Hour() != 4 {
		t.Fatalf("c pushed: %+v, want Tuesday 00:00 EDT (04:00 UTC)", plan)
	}
}
