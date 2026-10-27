package domain

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func day(s string) time.Time { return mustDate(s) }

func mustDate(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func scheduled(s Schedule, id string) ScheduledActivity {
	for _, a := range s.Activities {
		if a.ID == id {
			return a
		}
	}
	return ScheduledActivity{}
}

// TestCPM_FSLagSkipsWeekend is AC-41.4: a successor with an FS lag of 2 days
// on a weekend-closed calendar starts the next working day after the lag.
func TestCPM_FSLagSkipsWeekend(t *testing.T) {
	cal := StandardCalendar()
	// 2026-10-23 is a Friday. Predecessor 1.1 starts Thursday 2026-10-22 and runs 2 working days.
	s, err := CPM(cal, day("2026-10-22"), []Activity{
		{ID: "1.1", DurationDays: 2},
		{ID: "1.2", DurationDays: 1, Deps: []Dependency{{PredecessorID: "1.1", Type: DepFS, LagDays: 2}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pred := scheduled(s, "1.1")
	succ := scheduled(s, "1.2")
	if pred.EarlyFinish != day("2026-10-23") {
		t.Fatalf("predecessor finish = %s, want Friday 2026-10-23", pred.EarlyFinish.Format(time.DateOnly))
	}
	if want := day("2026-10-26"); !succ.EarlyStart.Equal(want) {
		t.Fatalf("successor start = %s, want Monday 2026-10-26", succ.EarlyStart.Format(time.DateOnly))
	}
}

func TestCPM_DependencyTypes(t *testing.T) {
	cal := StandardCalendar()
	base := day("2026-10-05") // Monday
	// A runs Mon 10-05 through Fri 10-09.
	acts := []Activity{
		{ID: "A", DurationDays: 5},
		{ID: "SS", DurationDays: 2, Deps: []Dependency{{PredecessorID: "A", Type: DepSS}}},
		{ID: "FF", DurationDays: 3, Deps: []Dependency{{PredecessorID: "A", Type: DepFF}}},
		{ID: "SF", DurationDays: 1, Deps: []Dependency{{PredecessorID: "A", Type: DepSF}}},
	}
	s, err := CPM(cal, base, acts)
	if err != nil {
		t.Fatal(err)
	}
	if got := scheduled(s, "A").EarlyFinish; got != day("2026-10-09") {
		t.Fatalf("A finish = %s", got.Format(time.DateOnly))
	}
	if got := scheduled(s, "SS").EarlyStart; got != day("2026-10-05") {
		t.Errorf("SS start = %s, want same day as A's start", got.Format(time.DateOnly))
	}
	// FF: successor finishes with A (Fri 10-09), 3 days long, so it starts Wed 10-07.
	if got := scheduled(s, "FF"); got.EarlyStart != day("2026-10-07") || got.EarlyFinish != day("2026-10-09") {
		t.Errorf("FF = %s..%s, want 10-07..10-09", got.EarlyStart.Format(time.DateOnly), got.EarlyFinish.Format(time.DateOnly))
	}
	// SF: successor finishes no earlier than A starts (Mon 10-05), 1 day long.
	if got := scheduled(s, "SF"); got.EarlyStart != day("2026-10-05") || got.EarlyFinish != day("2026-10-05") {
		t.Errorf("SF = %s..%s, want 10-05..10-05", got.EarlyStart.Format(time.DateOnly), got.EarlyFinish.Format(time.DateOnly))
	}
}

func TestCPM_HolidaysAndConstraints(t *testing.T) {
	// Tuesday 10-06 is a holiday, so H (2 days from Mon 10-05) finishes Wed 10-07.
	cal := StandardCalendar("2026-10-06")
	snet := day("2026-10-07")
	s, err := CPM(cal, day("2026-10-05"), []Activity{
		{ID: "H", DurationDays: 2},
		{ID: "C", DurationDays: 0, StartNoEarlierThan: &snet},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := scheduled(s, "H").EarlyFinish; got != day("2026-10-07") {
		t.Errorf("H finish = %s, want 2026-10-07 (holiday skipped)", got.Format(time.DateOnly))
	}
	if got := scheduled(s, "C").EarlyStart; got != day("2026-10-07") {
		t.Errorf("SNET milestone start = %s", got.Format(time.DateOnly))
	}
}

func TestCPM_CycleIsRejected(t *testing.T) {
	_, err := CPM(StandardCalendar(), day("2026-10-05"), []Activity{
		{ID: "a", DurationDays: 1, Deps: []Dependency{{PredecessorID: "b", Type: DepFS}}},
		{ID: "b", DurationDays: 1, Deps: []Dependency{{PredecessorID: "a", Type: DepFS}}},
	})
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeWBSCycle {
		t.Fatalf("error = %v, want %s", err, CodeWBSCycle)
	}
}

func TestCPM_RejectsMalformedInput(t *testing.T) {
	cal := StandardCalendar()
	start := day("2026-10-05")
	cases := map[string][]Activity{
		"duplicate id":      {{ID: "a"}, {ID: "a"}},
		"negative duration": {{ID: "a", DurationDays: -1}},
		"unknown pred":      {{ID: "a", Deps: []Dependency{{PredecessorID: "zz", Type: DepFS}}}},
		"self dependency":   {{ID: "a", Deps: []Dependency{{PredecessorID: "a", Type: DepFS}}}},
		"bad type":          {{ID: "a"}, {ID: "b", Deps: []Dependency{{PredecessorID: "a", Type: "XX"}}}},
	}
	for name, acts := range cases {
		if _, err := CPM(cal, start, acts); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: error = %v, want ErrInvalidInput", name, err)
		}
	}
}

// TestCPM_PropertyRespectsDependenciesAndCalendar generates random DAGs and
// checks that every start and finish is a working day and every FS bound holds.
func TestCPM_PropertyRespectsDependenciesAndCalendar(t *testing.T) {
	cal := StandardCalendar("2026-10-12")
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 60).Draw(t, "n")
		acts := make([]Activity, n)
		for i := range acts {
			acts[i] = Activity{ID: fmt.Sprintf("a%d", i), DurationDays: rapid.IntRange(0, 9).Draw(t, "dur")}
			if i > 0 && rapid.Bool().Draw(t, "dep") {
				pred := rapid.IntRange(0, i-1).Draw(t, "pred")
				acts[i].Deps = []Dependency{{PredecessorID: acts[pred].ID, Type: DepFS, LagDays: rapid.IntRange(0, 5).Draw(t, "lag")}}
			}
		}
		s, err := CPM(cal, day("2026-10-05"), acts)
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]ScheduledActivity{}
		for _, a := range s.Activities {
			byID[a.ID] = a
			if !cal.IsWorking(a.EarlyStart) {
				t.Fatalf("%s starts on a non-working day %s", a.ID, a.EarlyStart.Format(time.DateOnly))
			}
			if a.EarlyFinish.Before(a.EarlyStart) {
				t.Fatalf("%s finishes before it starts", a.ID)
			}
		}
		for _, a := range acts {
			for _, d := range a.Deps {
				pred := byID[d.PredecessorID]
				bound := cal.NextWorking(pred.EarlyFinish.AddDate(0, 0, 1+d.LagDays))
				if byID[a.ID].EarlyStart.Before(bound) {
					t.Fatalf("%s starts %s before FS bound %s", a.ID, byID[a.ID].EarlyStart.Format(time.DateOnly), bound.Format(time.DateOnly))
				}
			}
		}
	})
}

// TestCPM_TwoThousandNodesUnderBudget is the 2,000-node CPM budget (< 500 ms)
// from the PRD test plan, on a chain-and-fan graph with about 3,500 dependencies.
func TestCPM_TwoThousandNodesUnderBudget(t *testing.T) {
	const n = 2000
	acts := make([]Activity, n)
	for i := range acts {
		acts[i] = Activity{ID: fmt.Sprintf("1.%d", i), DurationDays: 1 + i%5}
		if i > 0 {
			acts[i].Deps = append(acts[i].Deps, Dependency{PredecessorID: acts[i-1].ID, Type: DepFS, LagDays: i % 3})
		}
		if i > 1 {
			acts[i].Deps = append(acts[i].Deps, Dependency{PredecessorID: acts[i/2].ID, Type: DepSS})
		}
	}
	start := time.Now()
	s, err := CPM(StandardCalendar(), day("2026-01-05"), acts)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Activities) != n {
		t.Fatalf("scheduled %d activities, want %d", len(s.Activities), n)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("scheduling 2,000 nodes took %s, budget 500ms", elapsed)
	}
}

// TestCPM_BackwardPassSlackAndCriticalPath checks late dates, float and the
// critical path on a diamond: A → B → D and A → C → D, where C is the longer branch.
func TestCPM_BackwardPassSlackAndCriticalPath(t *testing.T) {
	fs := func(pred string) []Dependency { return []Dependency{{PredecessorID: pred, Type: DepFS}} }
	s, err := CPM(StandardCalendar(), day("2026-10-05"), []Activity{
		{ID: "A", DurationDays: 3},
		{ID: "B", DurationDays: 2, Deps: fs("A")},
		{ID: "C", DurationDays: 5, Deps: fs("A")},
		{ID: "D", DurationDays: 1, Deps: append(fs("B"), fs("C")...)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if s.FinishDate != day("2026-10-15") {
		t.Fatalf("finish = %s, want Thursday 2026-10-15", s.FinishDate.Format(time.DateOnly))
	}
	b := scheduled(s, "B")
	if b.EarlyStart != day("2026-10-08") || b.LateStart != day("2026-10-13") || b.LateFinish != day("2026-10-14") || b.SlackDays != 3 || b.Critical {
		t.Fatalf("B = ES %s LS %s LF %s slack %d critical %v; want ES 10-08, LS 10-13, LF 10-14, slack 3, not critical",
			b.EarlyStart.Format(time.DateOnly), b.LateStart.Format(time.DateOnly), b.LateFinish.Format(time.DateOnly), b.SlackDays, b.Critical)
	}
	if got := fmt.Sprint(s.CriticalPath); got != "[A C D]" {
		t.Fatalf("critical path = %s, want [A C D]", got)
	}
}

// TestCPM_PropertyLateScheduleIsFeasibleAndTight generates random DAGs over all
// four dependency types and checks the backward pass: late dates are working
// days no earlier than early dates, running every activity at its late dates
// still satisfies every dependency and the finish date, no late finish could be
// one working day later without delaying a successor or the project, and at
// least one activity is critical.
func TestCPM_PropertyLateScheduleIsFeasibleAndTight(t *testing.T) {
	cal := StandardCalendar("2026-10-12", "2026-11-26")
	types := []string{DepFS, DepSS, DepFF, DepSF}
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 40).Draw(t, "n")
		acts := make([]Activity, n)
		for i := range acts {
			acts[i] = Activity{ID: fmt.Sprintf("a%d", i), DurationDays: rapid.IntRange(0, 8).Draw(t, "dur")}
			for j := 0; i > 0 && j < rapid.IntRange(0, 2).Draw(t, "deps"); j++ {
				acts[i].Deps = append(acts[i].Deps, Dependency{
					PredecessorID: acts[rapid.IntRange(0, i-1).Draw(t, "pred")].ID,
					Type:          types[rapid.IntRange(0, 3).Draw(t, "type")],
					LagDays:       rapid.IntRange(0, 4).Draw(t, "lag"),
				})
			}
		}
		s, err := CPM(cal, day("2026-10-05"), acts)
		if err != nil {
			t.Fatal(err)
		}
		byID := map[string]ScheduledActivity{}
		critical := 0
		for _, a := range s.Activities {
			byID[a.ID] = a
			if !cal.IsWorking(a.LateStart) || !cal.IsWorking(a.LateFinish) {
				t.Fatalf("%s has a late date on a non-working day", a.ID)
			}
			if a.LateStart.Before(a.EarlyStart) || a.SlackDays < 0 || a.LateFinish.After(s.FinishDate) {
				t.Fatalf("%s: ES %s LS %s LF %s slack %d", a.ID, a.EarlyStart.Format(time.DateOnly), a.LateStart.Format(time.DateOnly), a.LateFinish.Format(time.DateOnly), a.SlackDays)
			}
			if a.Critical {
				critical++
			}
		}
		if critical == 0 {
			t.Fatal("no critical activity")
		}
		// limiting[id] is set when delaying that activity by one working day past
		// its late dates would push some successor past its own late start.
		limiting := map[string]bool{}
		for _, a := range acts {
			for _, d := range a.Deps {
				pred, succ := byID[d.PredecessorID], byID[a.ID]
				if bound := lowerBound(cal, d, a.DurationDays, pred.LateStart, pred.LateFinish); succ.LateStart.Before(bound) {
					t.Fatalf("late schedule breaks %s %s→%s lag %d: successor LS %s before %s", d.Type, d.PredecessorID, a.ID, d.LagDays,
						succ.LateStart.Format(time.DateOnly), bound.Format(time.DateOnly))
				}
				later := lowerBound(cal, d, a.DurationDays, cal.Shift(pred.LateStart, 1), cal.Shift(pred.LateFinish, 1))
				if succ.LateStart.Before(later) {
					limiting[d.PredecessorID] = true
				}
			}
		}
		for _, a := range s.Activities {
			if a.LateFinish.Before(s.FinishDate) && !limiting[a.ID] {
				t.Fatalf("%s late finish %s is not tight: it could finish later without delaying anything", a.ID, a.LateFinish.Format(time.DateOnly))
			}
		}
	})
}
