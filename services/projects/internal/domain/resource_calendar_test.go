package domain

import (
	"fmt"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// Wave 41 proof, AC3: "WBS scheduling respects dependencies and resource
// calendars" (FR-41.2: a task cannot be scheduled on a non-working day of its
// assigned role calendar).

// TestCPM_ResourceCalendarsHandDerived schedules six activities on three
// calendars. Every expected date below was derived by hand on a paper calendar
// of October 2026 (the 5th is a Monday), not by running the scheduler:
//
//	office  Mon-Fri (project calendar)
//	FIELD   Tue-Sat, Saturday 10-10 a holiday
//	LAB     Mon, Wed, Fri
//
//	A office 3d                 ES Mon 10-05  EF Wed 10-07
//	B FIELD  3d  FS A           ES Thu 10-08  EF Tue 10-13 (Fri 10-09, holiday Sat, Sun/Mon closed)
//	C LAB    2d  SS A +1        A.ES+1 = Tue 10-06 is closed for LAB -> ES Wed 10-07, EF Fri 10-09
//	D office 1d  FS B +2, FF C  10-13+1+2 = Fri 10-16 -> ES = EF Fri 10-16 (finish)
//	E LAB    0d  FS C           Sat 10-10 is closed for LAB -> Mon 10-12
//	F FIELD  2d  SF E +3        finish >= Thu 10-15 -> ES Wed 10-14, EF Thu 10-15
//
// Backward pass from Fri 10-16: F (FIELD) LF Fri 10-16, LS Thu 10-15, slack 1;
// E LF = LAB day on or before 10-16-3 = Tue 10-13 -> Mon 10-12, slack 0 (the
// next LAB day, Wed 10-14, would push F's finish to Sat 10-17); C LF = LAB day
// on or before E.LS-1 = Sun 10-11 -> Fri 10-09, LS Wed 10-07; B LF = FIELD day
// on or before D.LS-1-2 = Tue 10-13, LS Thu 10-08 (back over the holiday);
// A LF = min(B.LS-1 = Wed 10-07, office finish of a start on C.LS-1 = Tue 10-06
// = Thu 10-08) = Wed 10-07, LS Mon 10-05.
func TestCPM_ResourceCalendarsHandDerived(t *testing.T) {
	office := StandardCalendar()
	field := Calendar{Working: [7]bool{false, false, true, true, true, true, true}, Holidays: map[string]bool{"2026-10-10": true}}
	lab := Calendar{Working: [7]bool{false, true, false, true, false, true, false}}
	s, err := CPM(office, day("2026-10-05"), []Activity{
		{ID: "A", DurationDays: 3},
		{ID: "B", DurationDays: 3, Calendar: &field, Deps: []Dependency{{PredecessorID: "A", Type: DepFS}}},
		{ID: "C", DurationDays: 2, Calendar: &lab, Deps: []Dependency{{PredecessorID: "A", Type: DepSS, LagDays: 1}}},
		{ID: "D", DurationDays: 1, Deps: []Dependency{{PredecessorID: "B", Type: DepFS, LagDays: 2}, {PredecessorID: "C", Type: DepFF}}},
		{ID: "E", DurationDays: 0, Calendar: &lab, Deps: []Dependency{{PredecessorID: "C", Type: DepFS}}},
		{ID: "F", DurationDays: 2, Calendar: &field, Deps: []Dependency{{PredecessorID: "E", Type: DepSF, LagDays: 3}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string][5]string{ // ES, EF, LS, LF, slack
		"A": {"2026-10-05", "2026-10-07", "2026-10-05", "2026-10-07", "0"},
		"B": {"2026-10-08", "2026-10-13", "2026-10-08", "2026-10-13", "0"},
		"C": {"2026-10-07", "2026-10-09", "2026-10-07", "2026-10-09", "0"},
		"D": {"2026-10-16", "2026-10-16", "2026-10-16", "2026-10-16", "0"},
		"E": {"2026-10-12", "2026-10-12", "2026-10-12", "2026-10-12", "0"},
		"F": {"2026-10-14", "2026-10-15", "2026-10-15", "2026-10-16", "1"},
	}
	for id, w := range want {
		a := scheduled(s, id)
		got := [5]string{a.EarlyStart.Format(time.DateOnly), a.EarlyFinish.Format(time.DateOnly),
			a.LateStart.Format(time.DateOnly), a.LateFinish.Format(time.DateOnly), fmt.Sprint(a.SlackDays)}
		if got != w {
			t.Errorf("%s = ES/EF/LS/LF/slack %v, want %v", id, got, w)
		}
	}
	if s.FinishDate != day("2026-10-16") {
		t.Errorf("finish = %s, want 2026-10-16", s.FinishDate.Format(time.DateOnly))
	}
	if got := fmt.Sprint(s.CriticalPath); got != "[A B C D E]" {
		t.Errorf("critical path = %s, want [A B C D E]", got)
	}
}

// refCal is the test's own reading of a calendar: weekday mask plus holidays.
// The property below checks the scheduler with these helpers only, never with
// Calendar's methods or the scheduler's bound functions.
type refCal struct {
	mask     [7]bool
	holidays map[string]bool
}

func (c refCal) works(d time.Time) bool {
	return c.mask[d.Weekday()] && !c.holidays[d.Format(time.DateOnly)]
}

// runEnd is the last day of a run of dur working days starting on the working day start.
func (c refCal) runEnd(start time.Time, dur int) time.Time {
	d := start
	for n := 1; n < dur; {
		d = d.AddDate(0, 0, 1)
		if c.works(d) {
			n++
		}
	}
	return d
}

// countIn counts the working days in (from, to].
func (c refCal) countIn(from, to time.Time) int {
	n := 0
	for d := from.AddDate(0, 0, 1); !d.After(to); d = d.AddDate(0, 0, 1) {
		if c.works(d) {
			n++
		}
	}
	return n
}

func (c refCal) step(d time.Time, dir int) time.Time {
	for d = d.AddDate(0, 0, dir); !c.works(d); d = d.AddDate(0, 0, dir) {
	}
	return d
}

// depHolds reads a dependency as calendar-day inequalities between dates: FS
// successor start after predecessor finish + lag, SS start >= start + lag,
// FF finish >= finish + lag, SF finish >= start + lag.
func depHolds(typ string, lag int, ps, pf, ss, sf time.Time) bool {
	switch typ {
	case DepSS:
		return !ss.Before(ps.AddDate(0, 0, lag))
	case DepFF:
		return !sf.Before(pf.AddDate(0, 0, lag))
	case DepSF:
		return !sf.Before(ps.AddDate(0, 0, lag))
	default:
		return ss.After(pf.AddDate(0, 0, lag))
	}
}

// TestCPM_PropertyResourceCalendarsAndDependencies generates random DAGs over
// all four dependency types (lags -3..6 calendar days, the validator allows
// negatives) whose activities run on random resource calendars (any non-empty
// weekday mask, up to eight holidays) or the project calendar, with random SNET
// constraints. It checks, independently of the scheduler's code:
//   - every early and late date is a working day of the activity's own calendar
//     and the run between them is exactly its duration in that calendar's days;
//   - early and late schedules each satisfy every dependency, the project start
//     and the SNET constraint, and no late finish passes the project finish;
//   - early starts are minimal (one working day earlier breaks a constraint)
//     and late finishes maximal (one working day later breaks a successor or
//     the finish), so calendars are honoured without padding;
//   - slack counts the activity's working days from early to late start, and
//     critical means zero slack.
func TestCPM_PropertyResourceCalendarsAndDependencies(t *testing.T) {
	types := []string{DepFS, DepSS, DepFF, DepSF}
	projectStart := day("2026-10-01")
	rapid.Check(t, func(t *rapid.T) {
		project := StandardCalendar()
		cals := []*Calendar{nil}
		for k := rapid.IntRange(1, 4).Draw(t, "calendars"); k > 0; k-- {
			c := Calendar{Holidays: map[string]bool{}}
			for c.Working == [7]bool{} {
				for w := range c.Working {
					c.Working[w] = rapid.Bool().Draw(t, "weekday")
				}
			}
			for h := rapid.IntRange(0, 8).Draw(t, "holidays"); h > 0; h-- {
				c.Holidays[projectStart.AddDate(0, 0, rapid.IntRange(0, 90).Draw(t, "holiday")).Format(time.DateOnly)] = true
			}
			cals = append(cals, &c)
		}
		n := rapid.IntRange(1, 30).Draw(t, "n")
		acts := make([]Activity, n)
		for i := range acts {
			acts[i] = Activity{ID: fmt.Sprintf("a%d", i), DurationDays: rapid.IntRange(0, 7).Draw(t, "dur"),
				Calendar: cals[rapid.IntRange(0, len(cals)-1).Draw(t, "cal")]}
			if rapid.IntRange(0, 4).Draw(t, "snet") == 0 {
				d := projectStart.AddDate(0, 0, rapid.IntRange(-5, 20).Draw(t, "snetDay"))
				acts[i].StartNoEarlierThan = &d
			}
			for j := 0; i > 0 && j < rapid.IntRange(0, 3).Draw(t, "deps"); j++ {
				acts[i].Deps = append(acts[i].Deps, Dependency{
					PredecessorID: acts[rapid.IntRange(0, i-1).Draw(t, "pred")].ID,
					Type:          types[rapid.IntRange(0, 3).Draw(t, "type")],
					LagDays:       rapid.IntRange(-3, 6).Draw(t, "lag"),
				})
			}
		}
		s, err := CPM(project, projectStart, acts)
		if err != nil {
			t.Fatal(err)
		}
		ref := func(a Activity) refCal {
			c := project
			if a.Calendar != nil {
				c = *a.Calendar
			}
			return refCal{mask: c.Working, holidays: c.Holidays}
		}
		at := map[string]ScheduledActivity{}
		var finish time.Time
		for _, sa := range s.Activities {
			at[sa.ID] = sa
			if sa.EarlyFinish.After(finish) {
				finish = sa.EarlyFinish
			}
		}
		if !s.FinishDate.Equal(finish) {
			t.Fatalf("finish %s, latest early finish %s", s.FinishDate.Format(time.DateOnly), finish.Format(time.DateOnly))
		}
		f := func(d time.Time) string { return d.Format(time.DateOnly) }
		// starts reports whether starting a on start meets the project start, its
		// SNET and every dependency on its predecessors' dates (early or late).
		starts := func(a Activity, start time.Time, late bool) bool {
			end := ref(a).runEnd(start, a.DurationDays)
			if start.Before(projectStart) || (a.StartNoEarlierThan != nil && start.Before(*a.StartNoEarlierThan)) {
				return false
			}
			for _, d := range a.Deps {
				p := at[d.PredecessorID]
				ps, pf := p.EarlyStart, p.EarlyFinish
				if late {
					ps, pf = p.LateStart, p.LateFinish
				}
				if !depHolds(d.Type, d.LagDays, ps, pf, start, end) {
					return false
				}
			}
			return true
		}
		succs := map[string][]int{}
		for i, a := range acts {
			for _, d := range a.Deps {
				succs[d.PredecessorID] = append(succs[d.PredecessorID], i)
			}
		}
		critical := 0
		for _, a := range acts {
			c, sa := ref(a), at[a.ID]
			for _, d := range []time.Time{sa.EarlyStart, sa.EarlyFinish, sa.LateStart, sa.LateFinish} {
				if !c.works(d) {
					t.Fatalf("%s has date %s, a non-working day of its calendar %v", a.ID, f(d), c.mask)
				}
			}
			if c.runEnd(sa.EarlyStart, a.DurationDays) != sa.EarlyFinish || c.runEnd(sa.LateStart, a.DurationDays) != sa.LateFinish {
				t.Fatalf("%s runs %s..%s / %s..%s, not %d working days of its calendar", a.ID,
					f(sa.EarlyStart), f(sa.EarlyFinish), f(sa.LateStart), f(sa.LateFinish), a.DurationDays)
			}
			if !starts(a, sa.EarlyStart, false) || !starts(a, sa.LateStart, true) || sa.LateFinish.After(finish) {
				t.Fatalf("%s: early %s or late %s schedule breaks a constraint", a.ID, f(sa.EarlyStart), f(sa.LateStart))
			}
			if earlier := c.step(sa.EarlyStart, -1); starts(a, earlier, false) {
				t.Fatalf("%s early start %s is not minimal: %s also meets every constraint", a.ID, f(sa.EarlyStart), f(earlier))
			}
			laterStart := c.step(sa.LateStart, 1)
			laterEnd := c.runEnd(laterStart, a.DurationDays)
			fits := !laterEnd.After(finish)
			for _, k := range succs[a.ID] {
				sb := at[acts[k].ID]
				for _, d := range acts[k].Deps {
					if d.PredecessorID == a.ID && !depHolds(d.Type, d.LagDays, laterStart, laterEnd, sb.LateStart, sb.LateFinish) {
						fits = false
					}
				}
			}
			if fits {
				t.Fatalf("%s late finish %s is not maximal: %s..%s still fits", a.ID, f(sa.LateFinish), f(laterStart), f(laterEnd))
			}
			if slack := c.countIn(sa.EarlyStart, sa.LateStart); sa.SlackDays != slack || sa.Critical != (slack == 0) {
				t.Fatalf("%s slack %d critical %v, want %d", a.ID, sa.SlackDays, sa.Critical, slack)
			}
			if sa.Critical {
				critical++
			}
		}
		if critical == 0 {
			t.Fatal("no critical activity")
		}
	})
}
