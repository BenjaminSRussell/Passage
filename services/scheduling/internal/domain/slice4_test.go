package domain

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	_ "time/tzdata"

	"pgregory.net/rapid"
)

// --- BR-23.3: a changeover is never split across closed time ---

func splitLine(t *testing.T, shifts []Shift) Resource {
	r := res("R", "R", true, shifts)
	r.Matrix, r.CurrentGroup = mustMatrix(t, SetupEntry{FromGroup: "A", ToGroup: "B", Minutes: 40}), "A"
	return r
}

func TestSchedule_ChangeoverIsNeverSplitAcrossAShiftEnd(t *testing.T) {
	op := Operation{ID: "b", OrderID: "o", OperNumber: 10, Minutes: 60, SetupGroup: "B", Candidates: []string{"R"}}
	cases := []struct {
		name       string
		ready      time.Time
		wantStart  time.Time
		wantSegs   int
		wantChange int
	}{
		// Shift 08:00-12:00: from 11:40 only 20 min remain, less than the
		// 40 min changeover, so the job waits for the next day's shift.
		{"changeover that does not fit waits for the next shift", at(0, 11, 40), at(1, 8, 0), 1, 40},
		// From 11:00 the 40 min changeover fits; the process minutes may split.
		{"process minutes still split across the shift end", at(0, 11, 0), at(0, 11, 0), 2, 40},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			op := op
			op.ReadyAt = c.ready
			r := Schedule(Problem{Resources: []Resource{splitLine(t, weekdayShift(8*60, 12*60))}, Operations: []Operation{op}, Start: monday, HorizonDays: 14})
			got := placementOf(t, r, "b")
			if !got.Start.Equal(c.wantStart) || len(got.Segments) != c.wantSegs || got.ChangeoverMinutes != c.wantChange {
				t.Fatalf("placed %v with %d segments and %d min changeover; want start %v, %d segments, %d min",
					got.Start, len(got.Segments), got.ChangeoverMinutes, c.wantStart, c.wantSegs, c.wantChange)
			}
			if first := got.Segments[0]; first.End.Sub(first.Start) < 40*time.Minute {
				t.Fatalf("first segment %v..%v cannot hold the 40 min changeover", first.Start, first.End)
			}
		})
	}
}

func TestPlanMove_RefusesAStartThatWouldSplitTheChangeover(t *testing.T) {
	r := splitLine(t, weekdayShift(8*60, 12*60))
	op := Operation{ID: "b", OrderID: "o", OperNumber: 10, Minutes: 60, SetupGroup: "B", Candidates: []string{"R"}}
	current := map[string]Placement{"b": pl(op, "R", at(0, 8, 0), 40)}
	in := MoveInput{Resources: []Resource{r}, Start: monday, HorizonDays: 14, Booked: busyOf(op, current["b"]),
		Order: []Operation{op}, Current: current, Moved: "b", ResourceID: "R", At: at(1, 11, 40)}
	plan := PlanMove(in)
	if plan.Conflict == nil || plan.Conflict.Code != CodeOutsideCapacity || !strings.Contains(plan.Conflict.Detail, "BR-23.3") {
		t.Fatalf("conflict = %+v, want SCHED-0002 citing BR-23.3", plan.Conflict)
	}
	in.At = at(1, 11, 0)
	if plan := PlanMove(in); plan.Conflict != nil || !plan.Changes[0].Start.Equal(at(1, 11, 0)) {
		t.Fatalf("a start with room for the changeover: %+v", plan)
	}
}

func TestSchedule_PropertyChangeoversAreNeverSplit(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		start := rapid.IntRange(0, 20).Draw(t, "shiftStart") * 30
		length := rapid.IntRange(1, 16).Draw(t, "shiftLength") * 30
		r := res("R", "R", true, allDays(start, start+length))
		r.Matrix = mustMatrix(t,
			SetupEntry{FromGroup: "A", ToGroup: "B", Minutes: rapid.IntRange(1, length).Draw(t, "ab")},
			SetupEntry{FromGroup: "B", ToGroup: "A", Minutes: rapid.IntRange(1, length).Draw(t, "ba")})
		n := rapid.IntRange(1, 12).Draw(t, "ops")
		ops := make([]Operation, 0, n)
		for i := 0; i < n; i++ {
			ops = append(ops, Operation{ID: fmt.Sprintf("op%d", i), OrderID: fmt.Sprintf("o%d", i), OperNumber: 10,
				Minutes: rapid.IntRange(1, 600).Draw(t, "minutes"), SetupGroup: rapid.SampledFrom([]string{"A", "B"}).Draw(t, "group"),
				Candidates: []string{"R"}, ReadyAt: monday.Add(time.Duration(rapid.IntRange(0, 2000).Draw(t, "ready")) * time.Minute)})
		}
		res := Schedule(Problem{Resources: []Resource{r}, Operations: ops, Start: monday, HorizonDays: 30})
		for _, p := range res.Placements {
			if first := p.Segments[0]; p.ChangeoverMinutes > minutesBetween(first.Start, first.End) {
				t.Fatalf("%s: %d min changeover split across closed time (first segment %v..%v)", p.OperationID, p.ChangeoverMinutes, first.Start, first.End)
			}
		}
	})
}

// --- FR-23.8, BR-23.10: moves propagate to pegged orders ---

func TestDependentsOf_WalksLevelsAndReportsTruncation(t *testing.T) {
	chain := func(n int) []Peg {
		var out []Peg
		for i := 0; i < n; i++ {
			out = append(out, Peg{ParentOrderID: fmt.Sprintf("o%d", i), DependentOrderID: fmt.Sprintf("o%d", i+1)})
		}
		return out
	}
	cases := []struct {
		name      string
		pegs      []Peg
		lim       PropagationLimit
		want      []string
		truncated bool
	}{
		{"no pegs", nil, PropagationLimit{}, nil, false},
		{"five levels fit the default limit", chain(5), PropagationLimit{}, []string{"o1", "o2", "o3", "o4", "o5"}, false},
		{"a sixth level is cut and reported", chain(6), PropagationLimit{}, []string{"o1", "o2", "o3", "o4", "o5"}, true},
		{"the order limit cuts a wide fan-out", []Peg{{"o0", "a"}, {"o0", "b"}, {"o0", "c"}}, PropagationLimit{Orders: 2}, []string{"a", "b"}, true},
		{"a fan-out within the limit is whole", []Peg{{"o0", "a"}, {"o0", "b"}}, PropagationLimit{Orders: 2}, []string{"a", "b"}, false},
		{"a diamond reaches its join once", []Peg{{"o0", "a"}, {"o0", "b"}, {"a", "d"}, {"b", "d"}}, PropagationLimit{}, []string{"a", "b", "d"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, truncated := DependentsOf("o0", c.pegs, c.lim)
			if !slices.Equal(got, c.want) || truncated != c.truncated {
				t.Fatalf("got %v truncated=%v, want %v truncated=%v", got, truncated, c.want, c.truncated)
			}
		})
	}
}

func TestWouldCycle(t *testing.T) {
	pegs := []Peg{{"a", "b"}, {"b", "c"}}
	cases := []struct {
		parent, dependent string
		want              bool
	}{
		{"c", "a", true},  // a -> b -> c exists; c -> a closes the loop
		{"a", "c", false}, // a skip-level peg is fine
		{"x", "x", true},  // an order cannot depend on itself
		{"d", "a", false}, // a new parent above the chain
	}
	for _, c := range cases {
		if got := WouldCycle(pegs, c.parent, c.dependent); got != c.want {
			t.Errorf("WouldCycle(%s -> %s) = %v, want %v", c.parent, c.dependent, got, c.want)
		}
	}
}

func TestPropagationOrder_ParentsComeFirst(t *testing.T) {
	// root -> a -> b -> c and root -> c: c must wait for b although it is
	// also one level below root.
	pegs := []Peg{{"root", "a"}, {"a", "b"}, {"b", "c"}, {"root", "c"}, {"x", "y"}}
	if got := PropagationOrder("root", pegs); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Fatalf("order = %v, want [a b c] (x and y are not reachable)", got)
	}
}

// pegLine: order A has two operations on R, 08:00-09:00 and 09:00-10:00.
// Order B (one operation on S at 10:00) and order C (one on S at 11:00) are
// pegged to A, and D (on S at 12:00) is pegged to both B and C.
func pegLine() MoveInput {
	shifts := allDays(8*60, 18*60)
	ops := []Operation{
		{ID: "a1", OrderID: "A", OperNumber: 10, Minutes: 60, Candidates: []string{"R"}},
		{ID: "a2", OrderID: "A", OperNumber: 20, Minutes: 60, Candidates: []string{"R"}},
		{ID: "b1", OrderID: "B", OperNumber: 10, Minutes: 60, Candidates: []string{"S"}},
		{ID: "c1", OrderID: "C", OperNumber: 10, Minutes: 60, Candidates: []string{"S"}},
		{ID: "d1", OrderID: "D", OperNumber: 10, Minutes: 30, Candidates: []string{"S"}},
		{ID: "d2", OrderID: "D", OperNumber: 20, Minutes: 30, Candidates: []string{"R"}},
	}
	byID := map[string]Operation{}
	for _, op := range ops {
		byID[op.ID] = op
	}
	current := map[string]Placement{
		"a1": pl(byID["a1"], "R", at(0, 8, 0), 0),
		"a2": pl(byID["a2"], "R", at(0, 9, 0), 0),
		"b1": pl(byID["b1"], "S", at(0, 10, 0), 0),
		"c1": pl(byID["c1"], "S", at(0, 11, 0), 0),
		"d1": pl(byID["d1"], "S", at(0, 12, 0), 0),
		"d2": pl(byID["d2"], "R", at(0, 12, 30), 0),
	}
	var booked []Busy
	for id, p := range current {
		booked = append(booked, busyOf(byID[id], p)...)
	}
	return MoveInput{Resources: []Resource{res("R", "R", true, shifts), res("S", "S", true, shifts)}, Start: monday, HorizonDays: 14,
		Booked: booked, Order: ops[:2], Current: current, Moved: "a2", ResourceID: "R", At: at(0, 14, 0),
		Dependents: ops[2:], Pegs: []Peg{{"A", "B"}, {"A", "C"}, {"B", "D"}, {"C", "D"}}}
}

func changeOf(t *testing.T, plan MovePlan, id string) Placement {
	t.Helper()
	for _, c := range plan.Changes {
		if c.OperationID == id {
			return c
		}
	}
	t.Fatalf("operation %s did not move: %+v", id, plan.Changes)
	return Placement{}
}

func TestPlanMove_PushesPeggedOrdersBehindTheirParents(t *testing.T) {
	plan := PlanMove(pegLine())
	if plan.Conflict != nil {
		t.Fatal(plan.Conflict)
	}
	// a2 moves to 14:00-15:00; B and C need A's output and run on S in turn
	// after 15:00; D needs both and its second step follows on R.
	want := map[string]time.Time{"a2": at(0, 14, 0), "b1": at(0, 15, 0), "c1": at(0, 16, 0), "d1": at(0, 17, 0), "d2": at(0, 17, 30)}
	for id, start := range want {
		if got := changeOf(t, plan, id); !got.Start.Equal(start) {
			t.Errorf("%s starts %v, want %v", id, got.Start, start)
		}
	}
	if !slices.Equal(plan.Dependents, []string{"B", "C", "D"}) {
		t.Fatalf("dependents = %v, want [B C D]", plan.Dependents)
	}
	if plan.Changes[0].OperationID != "a2" {
		t.Fatalf("the moved operation must come first: %+v", plan.Changes[0])
	}
}

func TestPlanMove_WithoutDependentsPeggedOrdersStay(t *testing.T) {
	in := pegLine()
	in.Dependents, in.Pegs = nil, nil
	plan := PlanMove(in)
	if plan.Conflict != nil || len(plan.Changes) != 1 || len(plan.Dependents) != 0 {
		t.Fatalf("plan = %+v, want only a2 to move", plan)
	}
}

func TestPlanMove_DependentsNeverMoveEarlier(t *testing.T) {
	in := pegLine()
	in.Moved, in.At = "a1", at(0, 7, 0) // shift opens 08:00; a1 stays at 08:00 and nothing pushes
	in.ResourceID = "R"
	plan := PlanMove(in)
	if plan.Conflict == nil {
		// 07:00 is closed, so the exact move is refused before propagation.
		t.Fatalf("a move into closed time must be refused: %+v", plan)
	}
	in.Current["a1"] = pl(in.Order[0], "R", at(0, 7, 0), 0) // pretend a1 was earlier
	in.At = at(0, 8, 0)
	plan = PlanMove(in)
	if plan.Conflict != nil {
		t.Fatal(plan.Conflict)
	}
	for _, c := range plan.Changes {
		if c.OrderID != "A" {
			t.Fatalf("dependent %s moved although its parent ends no later: %+v", c.OrderID, c)
		}
	}
}

func TestPlanMove_FirmedDependentRefusesTheWholeMove(t *testing.T) {
	in := pegLine()
	in.Firmed = map[string]bool{"c1": true}
	plan := PlanMove(in)
	if plan.Conflict == nil || plan.Conflict.Code != CodeFirmed || plan.Conflict.JobID != "c1" || !strings.Contains(plan.Conflict.Detail, "pegged order C") {
		t.Fatalf("conflict = %+v, want SCHED-0008 on c1 naming pegged order C", plan.Conflict)
	}
	if len(plan.Changes) != 0 {
		t.Fatalf("a refused move changes nothing: %+v", plan.Changes)
	}
}

// --- FR-23.17: recurring capacity blocks ---

func TestRecurrence_Windows(t *testing.T) {
	cases := []struct {
		name  string
		rule  string
		start time.Time
		to    int // days after monday
		want  []time.Time
	}{
		{"weekly Monday over two weeks", "FREQ=WEEKLY;BYDAY=MO", monday, 14, []time.Time{at(0, 6, 0), at(7, 6, 0)}},
		{"every second day, three times", "FREQ=DAILY;INTERVAL=2;COUNT=3", monday, 30, []time.Time{at(0, 6, 0), at(2, 6, 0), at(4, 6, 0)}},
		{"every second week on Monday and Wednesday", "FREQ=WEEKLY;INTERVAL=2;BYDAY=WE,MO", monday, 21,
			[]time.Time{at(0, 6, 0), at(2, 6, 0), at(14, 6, 0), at(16, 6, 0)}},
		{"until a date, the time part ignored", "RRULE:FREQ=DAILY;UNTIL=20260107T235959Z", monday, 30, []time.Time{at(0, 6, 0), at(1, 6, 0), at(2, 6, 0)}},
		{"weekly without BYDAY follows the start weekday", "FREQ=WEEKLY", at(2, 0, 0), 14, []time.Time{at(2, 6, 0), at(9, 6, 0)}},
		{"COUNT counts from the start, not from the window", "FREQ=WEEKLY;BYDAY=MO;COUNT=2", monday.AddDate(0, 0, -7), 14, []time.Time{at(0, 6, 0)}},
		{"a range end on Monday morning cuts the second week", "FREQ=WEEKLY;BYDAY=MO", monday, 7, []time.Time{at(0, 6, 0)}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rc := Recurrence{Rule: c.rule, StartDate: c.start, StartMinute: 360, EndMinute: 840}
			got, err := rc.Windows(time.UTC, monday, at(c.to, 0, 0))
			if err != nil {
				t.Fatal(err)
			}
			starts := make([]time.Time, 0, len(got))
			for _, w := range got {
				if w.End.Sub(w.Start) != 8*time.Hour {
					t.Fatalf("window %v..%v is not 8 hours", w.Start, w.End)
				}
				starts = append(starts, w.Start)
			}
			if !slices.EqualFunc(starts, c.want, time.Time.Equal) {
				t.Fatalf("starts = %v, want %v", starts, c.want)
			}
		})
	}
}

func TestRecurrence_DSTDayKeepsItsTrueLength(t *testing.T) {
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	// Berlin springs forward on 2026-03-29: a 00:00-24:00 window is 23 hours.
	rc := Recurrence{Rule: "FREQ=DAILY;COUNT=2", StartDate: time.Date(2026, 3, 28, 0, 0, 0, 0, time.UTC), StartMinute: 0, EndMinute: 1440}
	got, err := rc.Windows(berlin, time.Date(2026, 3, 27, 0, 0, 0, 0, time.UTC), time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].End.Sub(got[0].Start) != 24*time.Hour || got[1].End.Sub(got[1].Start) != 23*time.Hour {
		t.Fatalf("windows = %v, want 24 h then 23 h", got)
	}
	if want := time.Date(2026, 3, 27, 23, 0, 0, 0, time.UTC); !got[0].Start.Equal(want) {
		t.Fatalf("first window starts %v, want local midnight %v", got[0].Start, want)
	}
}

func TestRecurrence_RejectsWhatItCannotExpand(t *testing.T) {
	cases := []struct {
		name       string
		rule       string
		start, end int
	}{
		{"monthly frequency", "FREQ=MONTHLY", 0, 60},
		{"ordinal weekday", "FREQ=WEEKLY;BYDAY=1MO", 0, 60},
		{"no frequency", "INTERVAL=2", 0, 60},
		{"zero interval", "FREQ=DAILY;INTERVAL=0", 0, 60},
		{"unsupported part", "FREQ=DAILY;BYHOUR=6", 0, 60},
		{"short until", "FREQ=DAILY;UNTIL=2026", 0, 60},
		{"empty value", "FREQ=", 0, 60},
		{"BYDAY on a daily rule", "FREQ=DAILY;BYDAY=MO", 0, 60},
		{"COUNT and UNTIL together", "FREQ=DAILY;COUNT=2;UNTIL=20260110", 0, 60},
		{"until before the start", "FREQ=DAILY;UNTIL=20260101", 0, 60},
		{"part given twice", "FREQ=DAILY;FREQ=WEEKLY", 0, 60},
		{"window across midnight", "FREQ=DAILY", 1380, 1500},
		{"empty window", "FREQ=DAILY", 600, 600},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rc := Recurrence{Rule: c.rule, StartDate: monday, StartMinute: c.start, EndMinute: c.end}
			if err := rc.Validate(); err == nil {
				t.Fatalf("rule %q window %d-%d: want an error", c.rule, c.start, c.end)
			}
		})
	}
}
