package domain

import (
	"fmt"
	"time"
)

// maxCalendarGap bounds how far the calendar walks looking for a working day.
const maxCalendarGap = 366

// Calendar is a working-day calendar (FR-41.2): weekdays flagged in Working, plus
// Holidays as YYYY-MM-DD dates on which nothing is worked.
type Calendar struct {
	Working  [7]bool
	Holidays map[string]bool
}

// StandardCalendar works Monday to Friday except the listed holidays.
func StandardCalendar(holidays ...string) Calendar {
	c := Calendar{Working: [7]bool{false, true, true, true, true, true, false}, Holidays: map[string]bool{}}
	for _, h := range holidays {
		c.Holidays[h] = true
	}
	return c
}

// IsWorking reports whether d is a working day.
func (c Calendar) IsWorking(d time.Time) bool {
	return c.Working[d.Weekday()] && !c.Holidays[d.Format(time.DateOnly)]
}

// NextWorking returns d when it is a working day, otherwise the next working day after it.
func (c Calendar) NextWorking(d time.Time) time.Time {
	for i := 0; i < maxCalendarGap && !c.IsWorking(d); i++ {
		d = d.AddDate(0, 0, 1)
	}
	return d
}

// PrevWorking returns d when it is a working day, otherwise the last working day before it.
func (c Calendar) PrevWorking(d time.Time) time.Time {
	for i := 0; i < maxCalendarGap && !c.IsWorking(d); i++ {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// workingDaysAfter counts the working days in (from, to]; 0 when to is not after from.
func (c Calendar) workingDaysAfter(from, to time.Time) int {
	n := 0
	for d := from.AddDate(0, 0, 1); !d.After(to); d = d.AddDate(0, 0, 1) {
		if c.IsWorking(d) {
			n++
		}
	}
	return n
}

// Shift moves the working day d by n working days; a negative n moves backwards.
func (c Calendar) Shift(d time.Time, n int) time.Time {
	step := 1
	if n < 0 {
		step, n = -1, -n
	}
	for ; n > 0; n-- {
		d = d.AddDate(0, 0, step)
		for i := 0; i < maxCalendarGap && !c.IsWorking(d); i++ {
			d = d.AddDate(0, 0, step)
		}
	}
	return d
}

// Dependency types (FR-41.2).
const (
	DepFS = "FS"
	DepSS = "SS"
	DepFF = "FF"
	DepSF = "SF"
)

// Dependency links an activity to a predecessor. LagDays is calendar days.
type Dependency struct {
	PredecessorID string
	Type          string
	LagDays       int
}

// Activity is one WBS node to schedule. DurationDays counts working days; 0 is a milestone.
type Activity struct {
	ID                 string
	DurationDays       int
	StartNoEarlierThan *time.Time
	Deps               []Dependency
}

// ScheduledActivity is the scheduler output for one activity (FR-41.2).
// SlackDays is total float in working days; Critical is SlackDays == 0.
type ScheduledActivity struct {
	ID          string
	EarlyStart  time.Time
	EarlyFinish time.Time
	LateStart   time.Time
	LateFinish  time.Time
	SlackDays   int
	Critical    bool
}

// Schedule is the scheduler result: activities in input order, the project
// finish date, and the critical activities in dependency (topological) order.
type Schedule struct {
	Activities   []ScheduledActivity
	FinishDate   time.Time
	CriticalPath []string
}

// CPM schedules a WBS with the critical path method over a working calendar.
//
// Forward pass: each dependency gives a lower bound on the successor's early
// start, and every start lands on a working day. The FS bound is the day after
// the predecessor's finish plus lag (calendar days), rolled to the next working
// day; for example a Friday finish with a 2-day lag starts the next Monday
// (AC-41.4). Backward pass: from the project finish date, each dependency gives
// the exact inverse upper bound on the predecessor's late finish, so late dates
// are the latest working days that still meet every successor. Activities are
// processed in topological order (Kahn); a cycle returns PROJ-WBS-001.
func CPM(cal Calendar, projectStart time.Time, acts []Activity) (Schedule, error) {
	index := make(map[string]int, len(acts))
	for i, a := range acts {
		if _, dup := index[a.ID]; dup {
			return Schedule{}, fmt.Errorf("%w: duplicate activity %s", ErrInvalidInput, a.ID)
		}
		if a.DurationDays < 0 {
			return Schedule{}, fmt.Errorf("%w: negative duration on %s", ErrInvalidInput, a.ID)
		}
		index[a.ID] = i
	}
	pending := make([]int, len(acts))
	succ := make([][]edge, len(acts))
	for i, a := range acts {
		for _, d := range a.Deps {
			p, ok := index[d.PredecessorID]
			if !ok || p == i {
				return Schedule{}, fmt.Errorf("%w: dependency of %s on unknown or self activity %s", ErrInvalidInput, a.ID, d.PredecessorID)
			}
			if d.Type != DepFS && d.Type != DepSS && d.Type != DepFF && d.Type != DepSF {
				return Schedule{}, fmt.Errorf("%w: dependency type %q", ErrInvalidInput, d.Type)
			}
			pending[i]++
			succ[p] = append(succ[p], edge{to: i, dep: d})
		}
	}
	queue := make([]int, 0, len(acts))
	for i := range acts {
		if pending[i] == 0 {
			queue = append(queue, i)
		}
	}
	es := make([]time.Time, len(acts))
	ef := make([]time.Time, len(acts))
	done := 0
	for ; done < len(queue); done++ {
		i := queue[done]
		a := acts[i]
		start := cal.NextWorking(projectStart)
		if a.StartNoEarlierThan != nil {
			start = maxTime(start, cal.NextWorking(*a.StartNoEarlierThan))
		}
		for _, d := range a.Deps {
			p := index[d.PredecessorID]
			start = maxTime(start, lowerBound(cal, d, a.DurationDays, es[p], ef[p]))
		}
		es[i] = start
		ef[i] = start
		if a.DurationDays > 0 {
			ef[i] = cal.Shift(start, a.DurationDays-1)
		}
		for _, e := range succ[i] {
			pending[e.to]--
			if pending[e.to] == 0 {
				queue = append(queue, e.to)
			}
		}
	}
	if done < len(acts) {
		return Schedule{}, &Error{Code: CodeWBSCycle, Detail: fmt.Sprintf("dependency cycle among %d activities", len(acts)-done)}
	}
	var finish time.Time
	for i := range acts {
		finish = maxTime(finish, ef[i])
	}
	ls := make([]time.Time, len(acts))
	lf := make([]time.Time, len(acts))
	for k := len(queue) - 1; k >= 0; k-- {
		i := queue[k]
		dur := acts[i].DurationDays
		late := finish
		for _, e := range succ[i] {
			late = minTime(late, upperBound(cal, e.dep, dur, ls[e.to], lf[e.to]))
		}
		lf[i] = late
		ls[i] = late
		if dur > 0 {
			ls[i] = cal.Shift(late, -(dur - 1))
		}
	}
	out := Schedule{Activities: make([]ScheduledActivity, len(acts)), FinishDate: finish}
	for i, a := range acts {
		slack := cal.workingDaysAfter(es[i], ls[i])
		out.Activities[i] = ScheduledActivity{ID: a.ID, EarlyStart: es[i], EarlyFinish: ef[i], LateStart: ls[i], LateFinish: lf[i],
			SlackDays: slack, Critical: slack == 0}
	}
	for _, i := range queue {
		if out.Activities[i].Critical {
			out.CriticalPath = append(out.CriticalPath, acts[i].ID)
		}
	}
	return out, nil
}

// edge is a dependency seen from its predecessor.
type edge struct {
	to  int
	dep Dependency
}

// upperBound is the latest working-day finish a dependency allows for a
// predecessor with the given duration, given the successor's late start and
// finish. It inverts lowerBound exactly: the predecessor finishing on the bound
// still lets the successor keep its late dates, and finishing one working day
// later does not.
func upperBound(cal Calendar, d Dependency, dur int, succStart, succFinish time.Time) time.Time {
	lag := d.LagDays
	startToFinish := func(start time.Time) time.Time {
		if dur == 0 {
			return start
		}
		return cal.Shift(start, dur-1)
	}
	switch d.Type {
	case DepSS:
		return startToFinish(cal.PrevWorking(succStart.AddDate(0, 0, -lag)))
	case DepFF:
		return cal.PrevWorking(succFinish.AddDate(0, 0, -lag))
	case DepSF:
		return startToFinish(cal.PrevWorking(succFinish.AddDate(0, 0, -lag)))
	default: // DepFS
		return cal.PrevWorking(succStart.AddDate(0, 0, -(1 + lag)))
	}
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// lowerBound is the earliest working-day start a dependency allows for a successor
// with the given duration, given the predecessor's early start and finish.
func lowerBound(cal Calendar, d Dependency, dur int, predStart, predFinish time.Time) time.Time {
	lag := d.LagDays
	finishToStart := func(target time.Time) time.Time {
		if dur == 0 {
			return target
		}
		return cal.Shift(target, -(dur - 1))
	}
	switch d.Type {
	case DepSS:
		return cal.NextWorking(predStart.AddDate(0, 0, lag))
	case DepFF:
		return finishToStart(cal.NextWorking(predFinish.AddDate(0, 0, lag)))
	case DepSF:
		return finishToStart(cal.NextWorking(predStart.AddDate(0, 0, lag)))
	default: // DepFS
		return cal.NextWorking(predFinish.AddDate(0, 0, 1+lag))
	}
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
