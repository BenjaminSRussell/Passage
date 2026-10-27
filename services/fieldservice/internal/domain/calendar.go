// Package domain holds the pure rules of the field-service context: business
// time for SLA clocks, entitlement resolution, line coverage and billing, and
// the work-order status machine. Nothing here performs I/O.
package domain

import "time"

// Calendar is the business-time calendar of an SLA profile. Business minutes
// are the elapsed minutes inside [OpenMinute, CloseMinute) on Workdays in Loc.
// Holidays are local dates without business time (BR-45.2 "holidays excluded").
// AllDay makes business time equal wall-clock time (24x7 profiles), so a 24x7
// calendar ignores holidays.
type Calendar struct {
	Loc         *time.Location
	Workdays    []time.Weekday
	OpenMinute  int // minutes after local midnight
	CloseMinute int
	AllDay      bool
	holidays    map[civilDate]bool
}

type civilDate struct {
	y int
	m time.Month
	d int
}

// DateLayout is the layout of holiday dates (YYYY-MM-DD).
const DateLayout = "2006-01-02"

// WithHolidays returns a copy of c on which the given local dates (YYYY-MM-DD)
// carry no business time. It fails on a date that does not parse.
func (c Calendar) WithHolidays(dates ...string) (Calendar, error) {
	out := c
	out.holidays = make(map[civilDate]bool, len(dates))
	for _, s := range dates {
		d, err := time.Parse(DateLayout, s)
		if err != nil {
			return Calendar{}, err
		}
		out.holidays[civilDate{d.Year(), d.Month(), d.Day()}] = true
	}
	return out, nil
}

// MondayToFriday returns a Monday-to-Friday calendar in loc.
func MondayToFriday(loc *time.Location, openMinute, closeMinute int) Calendar {
	return Calendar{
		Loc:         loc,
		Workdays:    []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday},
		OpenMinute:  openMinute,
		CloseMinute: closeMinute,
	}
}

// maxDaysScanned bounds the search for the next workday. A calendar with no
// workdays is rejected by Validate before it reaches this code.
const maxDaysScanned = 370

// Validate reports whether the calendar can be used for arithmetic.
func (c Calendar) Validate() bool {
	if c.Loc == nil {
		return false
	}
	if c.AllDay {
		return true
	}
	return len(c.Workdays) > 0 && c.OpenMinute >= 0 && c.CloseMinute <= 24*60 && c.OpenMinute < c.CloseMinute
}

func (c Calendar) isWorkday(y int, m time.Month, d int) bool {
	day := time.Date(y, m, d, 12, 0, 0, 0, time.UTC)
	if c.holidays[civilDate{day.Year(), day.Month(), day.Day()}] {
		return false
	}
	wd := day.Weekday()
	for _, w := range c.Workdays {
		if w == wd {
			return true
		}
	}
	return false
}

func (c Calendar) at(y int, m time.Month, d, minute int) time.Time {
	return time.Date(y, m, d, minute/60, minute%60, 0, 0, c.Loc)
}

func (c Calendar) bounds(y int, m time.Month, d int) (time.Time, time.Time) {
	return c.at(y, m, d, c.OpenMinute), c.at(y, m, d, c.CloseMinute)
}

// nextOpenAfter returns the opening instant of the first workday after the
// given local date.
func (c Calendar) nextOpenAfter(y int, m time.Month, d int) time.Time {
	for i := 1; i <= maxDaysScanned; i++ {
		day := time.Date(y, m, d+i, 12, 0, 0, 0, time.UTC)
		if c.isWorkday(day.Year(), day.Month(), day.Day()) {
			open, _ := c.bounds(day.Year(), day.Month(), day.Day())
			return open
		}
	}
	open, _ := c.bounds(y, m, d)
	return open
}

// prevCloseBefore returns the closing instant of the last workday before the
// given local date.
func (c Calendar) prevCloseBefore(y int, m time.Month, d int) time.Time {
	for i := 1; i <= maxDaysScanned; i++ {
		day := time.Date(y, m, d-i, 12, 0, 0, 0, time.UTC)
		if c.isWorkday(day.Year(), day.Month(), day.Day()) {
			_, closeAt := c.bounds(day.Year(), day.Month(), day.Day())
			return closeAt
		}
	}
	_, closeAt := c.bounds(y, m, d)
	return closeAt
}

// normalizeForward moves t to the first business instant at or after t.
func (c Calendar) normalizeForward(t time.Time) time.Time {
	y, m, d := t.In(c.Loc).Date()
	open, closeAt := c.bounds(y, m, d)
	if c.isWorkday(y, m, d) {
		if t.Before(open) {
			return open
		}
		if t.Before(closeAt) {
			return t
		}
	}
	return c.nextOpenAfter(y, m, d)
}

// normalizeBackward moves t to the last business instant at or before t.
func (c Calendar) normalizeBackward(t time.Time) time.Time {
	y, m, d := t.In(c.Loc).Date()
	open, closeAt := c.bounds(y, m, d)
	if c.isWorkday(y, m, d) {
		if t.After(closeAt) {
			return closeAt
		}
		if !t.Before(open) {
			return t
		}
	}
	return c.prevCloseBefore(y, m, d)
}

// Add returns the instant that lies the given number of business minutes after t.
func (c Calendar) Add(t time.Time, minutes int) time.Time {
	if c.AllDay {
		return t.Add(time.Duration(minutes) * time.Minute)
	}
	t = c.normalizeForward(t)
	remaining := time.Duration(minutes) * time.Minute
	for remaining > 0 {
		y, m, d := t.In(c.Loc).Date()
		_, closeAt := c.bounds(y, m, d)
		avail := closeAt.Sub(t)
		if remaining <= avail {
			return t.Add(remaining)
		}
		remaining -= avail
		t = c.nextOpenAfter(y, m, d)
	}
	return t
}

// Sub returns the instant that lies the given number of business minutes before t.
func (c Calendar) Sub(t time.Time, minutes int) time.Time {
	if c.AllDay {
		return t.Add(-time.Duration(minutes) * time.Minute)
	}
	t = c.normalizeBackward(t)
	remaining := time.Duration(minutes) * time.Minute
	for remaining > 0 {
		y, m, d := t.In(c.Loc).Date()
		openAt, _ := c.bounds(y, m, d)
		avail := t.Sub(openAt)
		if remaining <= avail {
			return t.Add(-remaining)
		}
		remaining -= avail
		t = c.prevCloseBefore(y, m, d)
	}
	return t
}

// Between counts the business minutes in [from, to). It returns 0 when to is
// not after from.
func (c Calendar) Between(from, to time.Time) int {
	if c.AllDay {
		if !to.After(from) {
			return 0
		}
		return int(to.Sub(from) / time.Minute)
	}
	if !to.After(from) {
		return 0
	}
	var total time.Duration
	y, m, d := from.In(c.Loc).Date()
	for {
		dayStart := c.at(y, m, d, 0)
		if !dayStart.Before(to) {
			break
		}
		if c.isWorkday(y, m, d) {
			open, closeAt := c.bounds(y, m, d)
			lo, hi := from, to
			if open.After(lo) {
				lo = open
			}
			if closeAt.Before(hi) {
				hi = closeAt
			}
			if hi.After(lo) {
				total += hi.Sub(lo)
			}
		}
		next := time.Date(y, m, d+1, 0, 0, 0, 0, c.Loc)
		y, m, d = next.Date()
	}
	return int(total / time.Minute)
}
