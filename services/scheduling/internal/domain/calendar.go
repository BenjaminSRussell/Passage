// Package domain holds the pure scheduling model: open capacity, the
// priority-list heuristic, move planning and weekly load. No I/O lives here.
package domain

import (
	"sort"
	"time"
)

// Shift is one open span of a resource's weekly calendar, in site-local
// minutes after midnight. EndMinute 1440 means end of day.
type Shift struct {
	Weekday     time.Weekday `json:"weekday"`
	StartMinute int          `json:"startMinute"`
	EndMinute   int          `json:"endMinute"`
}

// Interval is an open span of capacity in UTC.
type Interval struct {
	Start time.Time
	End   time.Time
}

// OpenIntervals materializes the open UTC intervals of shifts for `days`
// local days beginning with the local day of from. Calendar days are built in
// the site time zone so a DST day yields 23 or 25 hours of capacity (BR-23.7).
func OpenIntervals(loc *time.Location, shifts []Shift, from time.Time, days int) []Interval {
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := from.In(loc).Date()
	var out []Interval
	for n := 0; n < days; n++ {
		weekday := time.Date(y, m, d+n, 0, 0, 0, 0, loc).Weekday()
		for _, s := range shifts {
			if s.Weekday != weekday || s.StartMinute < 0 || s.EndMinute > 1440 || s.EndMinute <= s.StartMinute {
				continue
			}
			start := time.Date(y, m, d+n, 0, s.StartMinute, 0, 0, loc)
			end := time.Date(y, m, d+n, 0, s.EndMinute, 0, 0, loc)
			out = append(out, Interval{Start: start.UTC(), End: end.UTC()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out
}

// Minutes is the whole-minute length of the interval.
func (i Interval) Minutes() int { return minutesBetween(i.Start, i.End) }

func minutesBetween(a, b time.Time) int { return int(b.Sub(a) / time.Minute) }

// overlapMinutes is the whole-minute overlap of [a1,a2) and [b1,b2).
func overlapMinutes(a1, a2, b1, b2 time.Time) int {
	s, e := a1, a2
	if b1.After(s) {
		s = b1
	}
	if b2.Before(e) {
		e = b2
	}
	if !e.After(s) {
		return 0
	}
	return minutesBetween(s, e)
}
