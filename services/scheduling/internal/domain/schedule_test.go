package domain

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "time/tzdata"

	"pgregory.net/rapid"
)

// monday is 2026-01-05 00:00 UTC, a Monday, used as the planning start.
var monday = time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)

func at(day int, hh, mm int) time.Time {
	return monday.AddDate(0, 0, day).Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
}

func allDays(start, end int) []Shift {
	out := make([]Shift, 0, 7)
	for d := time.Sunday; d <= time.Saturday; d++ {
		out = append(out, Shift{Weekday: d, StartMinute: start, EndMinute: end})
	}
	return out
}

func weekdayShift(start, end int) []Shift {
	var out []Shift
	for d := time.Monday; d <= time.Friday; d++ {
		out = append(out, Shift{Weekday: d, StartMinute: start, EndMinute: end})
	}
	return out
}

func res(id, code string, finite bool, shifts []Shift) Resource {
	return Resource{ID: id, Code: code, Finite: finite, Location: time.UTC, Shifts: shifts}
}

func TestOpenIntervals_DSTDayHasTrueLength(t *testing.T) {
	cases := []struct {
		name string
		zone string
		day  time.Time
		want int
	}{
		{"EU spring forward is 23 hours", "Europe/Berlin", time.Date(2026, 3, 29, 0, 0, 0, 0, time.UTC), 1380},
		{"US fall back is 25 hours", "America/New_York", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), 1500},
		{"ordinary day is 24 hours", "Europe/Berlin", time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC), 1440},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			loc, err := time.LoadLocation(c.zone)
			if err != nil {
				t.Fatal(err)
			}
			// The calendar day is the site-local date of the given day.
			localDay := time.Date(c.day.Year(), c.day.Month(), c.day.Day(), 0, 0, 0, 0, loc)
			iv := OpenIntervals(loc, allDays(0, 1440), localDay, 1)
			total := 0
			for _, i := range iv {
				total += i.Minutes()
			}
			if total != c.want {
				t.Fatalf("open minutes = %d, want %d", total, c.want)
			}
		})
	}
}

func TestFill_SplitsAcrossShiftButNotAroundBookedWork(t *testing.T) {
	iv := []Interval{{at(0, 8, 0), at(0, 10, 0)}, {at(1, 8, 0), at(1, 14, 0)}}
	cases := []struct {
		name    string
		busy    []Busy
		start   time.Time
		minutes int
		segs    int
		wantErr string
	}{
		{"process job splits at shift close", nil, at(0, 8, 0), 300, 2, ""},
		{"booked work inside the open run conflicts", []Busy{{Start: at(0, 9, 0), End: at(0, 9, 30), JobID: "x"}}, at(0, 8, 0), 90, 0, CodeOverlap},
		{"exact start in closed time conflicts", nil, at(0, 12, 0), 30, 0, CodeOutsideCapacity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			segs, conf := fill(iv, c.busy, c.start, c.minutes, true)
			if c.wantErr != "" {
				if conf == nil || conf.Code != c.wantErr {
					t.Fatalf("conflict = %+v, want %s", conf, c.wantErr)
				}
				return
			}
			if conf != nil || len(segs) != c.segs {
				t.Fatalf("segs = %v conflict = %+v, want %d segments", segs, conf, c.segs)
			}
			if segs[0].End.Sub(segs[0].Start) != 2*time.Hour || segs[1].End.Sub(segs[1].Start) != 3*time.Hour {
				t.Fatalf("segment lengths wrong: %v", segs)
			}
		})
	}
}

func TestSchedule_ChoosesLeastFinishThenLowerCode(t *testing.T) {
	shifts := weekdayShift(8*60, 16*60)
	cases := []struct {
		name    string
		booked  []Busy
		wantRID string
	}{
		{"least finish wins over code order", []Busy{{ResourceID: "r1", Start: at(0, 8, 0), End: at(0, 12, 0), JobID: "x"}}, "r2"},
		{"equal finish falls to lower code", nil, "r2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := Problem{
				Resources: []Resource{res("r1", "B", true, shifts), res("r2", "A", true, shifts)},
				Operations: []Operation{{ID: "op", OrderID: "o", OperNumber: 10, Minutes: 60,
					Candidates: []string{"r1", "r2"}}},
				Booked: c.booked, Start: monday, HorizonDays: 14,
			}
			r := Schedule(p)
			if len(r.Placements) != 1 || r.Placements[0].ResourceID != c.wantRID {
				t.Fatalf("result = %+v, want resource %s", r, c.wantRID)
			}
		})
	}
}

func TestSchedule_BlocksSuccessorsOfUnplacedOperation(t *testing.T) {
	p := Problem{
		Resources: []Resource{res("dead", "D", true, nil), res("ok", "O", true, weekdayShift(8*60, 16*60))},
		Operations: []Operation{
			{ID: "a", OrderID: "o", OperNumber: 10, Minutes: 30, Candidates: []string{"dead"}},
			{ID: "b", OrderID: "o", OperNumber: 20, Minutes: 30, Candidates: []string{"ok"}},
		},
		Start: monday, HorizonDays: 14,
	}
	r := Schedule(p)
	if len(r.Placements) != 0 || len(r.Unplaced) != 2 || r.Unplaced[1].Reason != "predecessor operation is unplaced" {
		t.Fatalf("result = %+v", r)
	}
}

// TestSchedule_FT06IsFeasibleAndNotBelowOptimum drives the OR-Library ft06
// instance (Fisher & Thompson 6x6, optimum makespan 55). The heuristic must be
// feasible and cannot beat the optimum.
func TestSchedule_FT06IsFeasibleAndNotBelowOptimum(t *testing.T) {
	const ft06 = `2 1 0 3 1 6 3 7 5 3 4 6
1 8 2 5 4 10 5 10 0 10 3 4
2 5 3 4 5 8 0 9 1 1 4 7
1 5 0 5 2 5 3 3 4 8 5 9
2 9 1 3 4 5 5 4 0 3 3 1
1 3 3 3 5 9 0 10 4 4 2 1`
	var ops []Operation
	var resources []Resource
	seen := map[int]bool{}
	for j, line := range strings.Split(ft06, "\n") {
		f := strings.Fields(line)
		for k := 0; k < len(f); k += 2 {
			m, _ := strconv.Atoi(f[k])
			d, _ := strconv.Atoi(f[k+1])
			if !seen[m] {
				seen[m] = true
				resources = append(resources, res(fmt.Sprintf("m%d", m), fmt.Sprintf("M%d", m), true, allDays(0, 1440)))
			}
			ops = append(ops, Operation{ID: fmt.Sprintf("j%d-%d", j, k/2), OrderID: fmt.Sprintf("j%d", j), OperNumber: (k/2 + 1) * 10,
				Minutes: d, Candidates: []string{fmt.Sprintf("m%d", m)}})
		}
	}
	r := Schedule(Problem{Resources: resources, Operations: ops, Start: monday, HorizonDays: 60})
	if len(r.Unplaced) != 0 || len(r.Placements) != len(ops) {
		t.Fatalf("unplaced = %v", r.Unplaced)
	}
	if ms := Makespan(r.Placements); ms < 55 || ms > 400 {
		t.Fatalf("makespan = %d, want 55 <= makespan <= 400", ms)
	}
	checkFeasible(t, Problem{Resources: resources, Operations: ops, Start: monday, HorizonDays: 60}, r)
}

// checkFeasible asserts the invariants: no double booking on a finite resource,
// segments inside open capacity, full durations (process plus changeover), and
// precedence within orders.
// failer is satisfied by both *testing.T and *rapid.T.
type failer interface {
	Helper()
	Fatalf(format string, args ...any)
}

func checkFeasible(t failer, p Problem, r Result) {
	t.Helper()
	caps := capacities(p.Resources, p.Start, p.HorizonDays)
	byRes := map[string][]Segment{}
	ops := map[string]Operation{}
	for _, op := range p.Operations {
		ops[op.ID] = op
	}
	for _, pl := range r.Placements {
		op := ops[pl.OperationID]
		total := 0
		for _, s := range pl.Segments {
			total += minutesBetween(s.Start, s.End)
			if !slices.Contains(op.Candidates, pl.ResourceID) {
				t.Fatalf("%s placed on non-candidate %s", op.ID, pl.ResourceID)
			}
			covered := 0
			for _, i := range caps[pl.ResourceID].iv {
				covered += overlapMinutes(i.Start, i.End, s.Start, s.End)
			}
			if covered != minutesBetween(s.Start, s.End) {
				t.Fatalf("%s segment %v leaves open capacity", op.ID, s)
			}
		}
		if want := op.Minutes + pl.ChangeoverMinutes; total != want {
			t.Fatalf("%s has %d minutes, want %d (process plus changeover)", op.ID, total, want)
		}
		if caps[pl.ResourceID].res.Finite {
			byRes[pl.ResourceID] = append(byRes[pl.ResourceID], pl.Segments...)
		}
	}
	for rid, segs := range byRes {
		for i := range segs {
			for j := i + 1; j < len(segs); j++ {
				if segs[i].Start.Before(segs[j].End) && segs[j].Start.Before(segs[i].End) {
					t.Fatalf("double booking on %s: %v and %v", rid, segs[i], segs[j])
				}
			}
		}
	}
	byOrder := map[string][]Placement{}
	for _, pl := range r.Placements {
		byOrder[pl.OrderID] = append(byOrder[pl.OrderID], pl)
	}
	for _, pls := range byOrder {
		for i := 1; i < len(pls); i++ {
			prev, cur := pls[i-1], pls[i]
			if prev.OperNumber < cur.OperNumber && cur.Start.Before(prev.End) {
				t.Fatalf("precedence broken: %s starts %v before %s ends %v", cur.OperationID, cur.Start, prev.OperationID, prev.End)
			}
		}
	}
}

func TestSchedule_PropertyNoDoubleBookingAndInsideCapacity(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var rs []Resource
		nRes := rapid.IntRange(1, 4).Draw(t, "resources")
		for i := 0; i < nRes; i++ {
			start := rapid.IntRange(0, 600).Draw(t, "shiftStart")
			end := start + rapid.IntRange(60, 800).Draw(t, "shiftLen")
			if end > 1440 {
				end = 1440
			}
			rs = append(rs, res(fmt.Sprintf("r%d", i), fmt.Sprintf("C%d", i), true, allDays(start, end)))
		}
		var ops []Operation
		nOrders := rapid.IntRange(1, 6).Draw(t, "orders")
		for o := 0; o < nOrders; o++ {
			due := time.Time{}
			if rapid.Bool().Draw(t, "hasDue") {
				due = at(rapid.IntRange(1, 30).Draw(t, "dueDay"), 17, 0)
			}
			nOps := rapid.IntRange(1, 4).Draw(t, "ops")
			for k := 0; k < nOps; k++ {
				var cands []string
				for _, r := range rs {
					if rapid.Bool().Draw(t, "candidate") {
						cands = append(cands, r.ID)
					}
				}
				if len(cands) == 0 {
					cands = []string{rs[0].ID}
				}
				ops = append(ops, Operation{ID: fmt.Sprintf("o%d-%d", o, k), OrderID: fmt.Sprintf("o%d", o),
					OperNumber: (k + 1) * 10, Minutes: rapid.IntRange(1, 300).Draw(t, "minutes"), DueAt: due,
					Priority: rapid.IntRange(0, 3).Draw(t, "prio"), Candidates: cands})
			}
		}
		p := Problem{Resources: rs, Operations: ops, Start: monday, HorizonDays: 60}
		r := Schedule(p)
		if len(r.Placements)+len(r.Unplaced) != len(ops) {
			t.Fatalf("%d placed + %d unplaced != %d operations", len(r.Placements), len(r.Unplaced), len(ops))
		}
		checkFeasible(t, p, r)
	})
}

func TestPlanMove_ExactPlacementAndSuccessorPropagation(t *testing.T) {
	shifts := weekdayShift(8*60, 16*60)
	rs := []Resource{res("r", "R", true, shifts), res("s", "S", true, shifts)}
	order := []Operation{
		{ID: "op1", OrderID: "o", OperNumber: 10, Minutes: 60, Candidates: []string{"r", "s"}},
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
	base := MoveInput{Resources: rs, Start: monday, HorizonDays: 14, Booked: booked, Order: order, Current: current, Moved: "op1"}

	t.Run("move onto a booked job conflicts", func(t *testing.T) {
		in := base
		in.ResourceID, in.At = "r", at(0, 9, 30)
		if plan := PlanMove(in); plan.Conflict == nil || plan.Conflict.Code != CodeOverlap {
			t.Fatalf("plan = %+v, want overlap", plan)
		}
	})
	t.Run("move outside open capacity conflicts", func(t *testing.T) {
		in := base
		in.ResourceID, in.At = "r", at(0, 18, 0)
		if plan := PlanMove(in); plan.Conflict == nil || plan.Conflict.Code != CodeOutsideCapacity {
			t.Fatalf("plan = %+v, want outside capacity", plan)
		}
	})
	t.Run("non-candidate resource is refused", func(t *testing.T) {
		in := base
		in.ResourceID, in.At = "s", at(0, 11, 0)
		in.Order = []Operation{{ID: "op1", OrderID: "o", OperNumber: 10, Minutes: 60, Candidates: []string{"r"}}, order[1]}
		if plan := PlanMove(in); plan.Conflict == nil || plan.Conflict.Code != CodeNotCandidate {
			t.Fatalf("plan = %+v, want not candidate", plan)
		}
	})
	t.Run("successor moves forward after the moved job", func(t *testing.T) {
		in := base
		in.ResourceID, in.At = "r", at(0, 10, 0)
		plan := PlanMove(in)
		if plan.Conflict != nil || len(plan.Changes) != 2 {
			t.Fatalf("plan = %+v", plan)
		}
		if !plan.Changes[0].Start.Equal(at(0, 10, 0)) || !plan.Changes[1].Start.Equal(at(0, 11, 0)) {
			t.Fatalf("changes = %+v", plan.Changes)
		}
	})
}

func TestWeeklyLoad_BottleneckNeedsConsecutiveHotWeeks(t *testing.T) {
	r := res("r", "R", true, allDays(0, 1440))
	iv := OpenIntervals(time.UTC, r.Shifts, monday, 21)
	hot := func(weeks ...int) []Busy {
		var out []Busy
		for _, w := range weeks {
			start := monday.AddDate(0, 0, 7*w)
			out = append(out, Busy{ResourceID: "r", Start: start, End: start.Add(160 * time.Hour), JobID: "j"})
		}
		return out
	}
	cases := []struct {
		name    string
		booked  []Busy
		flagged []bool
	}{
		{"three hot weeks flag all three", hot(0, 1, 2), []bool{true, true, true}},
		{"two hot weeks flag nothing", hot(0, 1), []bool{false, false, false}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			loads := WeeklyLoad(r, iv, c.booked, monday, 3, 90, 3)
			for i, l := range loads {
				if l.Bottleneck != c.flagged[i] {
					t.Fatalf("week %d bottleneck = %v, want %v (load %.1f%%)", i, l.Bottleneck, c.flagged[i], l.LoadPct)
				}
			}
		})
	}
}
