package domain

import "time"

// WeekLoad is booked versus open minutes for one resource in one UTC week
// (Monday start). LoadPct is for display; the bottleneck flag uses integers.
type WeekLoad struct {
	ResourceID    string
	WeekStart     time.Time
	OpenMinutes   int
	BookedMinutes int
	LoadPct       float64
	Bottleneck    bool
}

// WeeklyLoad reports weeks load for one resource starting at the week of from.
// iv must cover every open interval of those weeks (see LoadIntervals).
// A week is a bottleneck when its load exceeds thresholdPct; episodes of at
// least `consecutive` such weeks flag every week in the episode (FR-23.9).
func WeeklyLoad(r Resource, iv []Interval, booked []Busy, from time.Time, weeks, thresholdPct, consecutive int) []WeekLoad {
	if consecutive < 1 {
		consecutive = 1
	}
	first := WeekStart(from)
	out := make([]WeekLoad, weeks)
	for w := range out {
		start := first.AddDate(0, 0, 7*w)
		end := start.AddDate(0, 0, 7)
		l := WeekLoad{ResourceID: r.ID, WeekStart: start}
		for _, i := range iv {
			l.OpenMinutes += overlapMinutes(i.Start, i.End, start, end)
		}
		for _, b := range booked {
			l.BookedMinutes += overlapMinutes(b.Start, b.End, start, end)
		}
		if l.OpenMinutes > 0 {
			l.LoadPct = float64(l.BookedMinutes) * 100 / float64(l.OpenMinutes)
		}
		out[w] = l
	}
	hot := func(l WeekLoad) bool {
		return l.OpenMinutes > 0 && l.BookedMinutes*100 > l.OpenMinutes*thresholdPct
	}
	for i := 0; i < len(out); {
		j := i
		for j < len(out) && hot(out[j]) {
			j++
		}
		if j-i >= consecutive {
			for k := i; k < j; k++ {
				out[k].Bottleneck = true
			}
		}
		if j == i {
			i++
		} else {
			i = j
		}
	}
	return out
}

// WeekStart is 00:00 UTC on the Monday of t's UTC week.
func WeekStart(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
}

// LoadIntervals materializes the open intervals WeeklyLoad needs for weeks
// starting at the week of from. It starts a day early so a site east or west
// of UTC contributes the local day that straddles Monday 00:00 UTC.
func LoadIntervals(r Resource, from time.Time, weeks int) []Interval {
	return r.Open(WeekStart(from).AddDate(0, 0, -1), weeks*7+2)
}

// DayLoad is booked versus open minutes for one resource in one local day of
// its calendar time zone (BR-23.7: calendars are defined in site time). Day is
// local midnight; [Start, End) are the UTC instants of the day, 23 or 25 hours
// long on a DST change. Days carry no bottleneck flag: FR-23.9 flags weeks only.
type DayLoad struct {
	ResourceID    string
	Day           time.Time
	Start         time.Time
	End           time.Time
	OpenMinutes   int
	BookedMinutes int
	LoadPct       float64
}

// DailyLoad reports `days` local days of load for one resource, the first
// being the calendar date of from (read as a date, whatever its zone) in the
// resource's time zone. iv must cover every open interval of those days (see
// DayIntervals).
func DailyLoad(r Resource, iv []Interval, booked []Busy, from time.Time, days int) []DayLoad {
	loc := r.Location
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := from.Date()
	out := make([]DayLoad, days)
	for n := range out {
		day := time.Date(y, m, d+n, 0, 0, 0, 0, loc)
		start, end := day.UTC(), time.Date(y, m, d+n+1, 0, 0, 0, 0, loc).UTC()
		l := DayLoad{ResourceID: r.ID, Day: day, Start: start, End: end}
		for _, i := range iv {
			l.OpenMinutes += overlapMinutes(i.Start, i.End, start, end)
		}
		for _, b := range booked {
			l.BookedMinutes += overlapMinutes(b.Start, b.End, start, end)
		}
		if l.OpenMinutes > 0 {
			l.LoadPct = float64(l.BookedMinutes) * 100 / float64(l.OpenMinutes)
		}
		out[n] = l
	}
	return out
}

// DayIntervals materializes the open intervals of the `days` local days
// DailyLoad reports for from, overrides applied.
func DayIntervals(r Resource, from time.Time, days int) []Interval {
	loc := r.Location
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := from.Date()
	return r.Open(time.Date(y, m, d, 0, 0, 0, 0, loc), days)
}

// ClippedMinutes is the part of [start, end) inside [from, to) in whole minutes.
func ClippedMinutes(start, end, from, to time.Time) int {
	return overlapMinutes(start, end, from, to)
}
