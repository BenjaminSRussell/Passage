package domain

import (
	"errors"
	"fmt"
)

// ErrInvalidCalendar is returned for a calendar that has no open weekday.
var ErrInvalidCalendar = errors.New("planning: invalid calendar")

// Calendar is the site work calendar (FR-22.5, BR-22.8): closed weekdays and
// closed individual days. Days are the plan's day indices; Weekday0 is the
// weekday of day 0 (0 Sunday .. 6 Saturday).
type Calendar struct {
	Weekday0       int
	ClosedWeekdays [7]bool
	ClosedDays     map[int]bool
}

// Validate rejects a calendar whose weekday or closure data cannot be used. A
// calendar needs at least one open weekday so that every day search terminates.
func (c *Calendar) Validate() error {
	if c.Weekday0 < 0 || c.Weekday0 > 6 {
		return fmt.Errorf("%w: weekday %d out of range", ErrInvalidCalendar, c.Weekday0)
	}
	for _, closed := range c.ClosedWeekdays {
		if !closed {
			return nil
		}
	}
	return fmt.Errorf("%w: every weekday is closed", ErrInvalidCalendar)
}

// IsOpen reports whether day is a working day.
func (c *Calendar) IsOpen(day int) bool {
	wd := ((c.Weekday0+day)%7 + 7) % 7
	return !c.ClosedWeekdays[wd] && !c.ClosedDays[day]
}

// OpenBefore returns day when it is open, else the nearest earlier open day.
func (c *Calendar) OpenBefore(day int) int {
	for !c.IsOpen(day) {
		day--
	}
	return day
}

// OpenAfter returns day when it is open, else the nearest later open day.
func (c *Calendar) OpenAfter(day int) int {
	for !c.IsOpen(day) {
		day++
	}
	return day
}

// Back moves n working days before day (supply side, BR-22.8). A day that is
// closed first moves backward to the nearest open day, then n open days are
// stepped back from there.
func (c *Calendar) Back(day, n int) int {
	day = c.OpenBefore(day)
	for i := 0; i < n; i++ {
		day = c.OpenBefore(day - 1)
	}
	return day
}

// Forward moves n working days after day (demand side, BR-22.8). A closed day
// first moves forward to the nearest open day, then n open days are stepped on.
func (c *Calendar) Forward(day, n int) int {
	day = c.OpenAfter(day)
	for i := 0; i < n; i++ {
		day = c.OpenAfter(day + 1)
	}
	return day
}
