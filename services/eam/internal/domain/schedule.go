package domain

import (
	"errors"
	"fmt"
	"math"
	"time"
)

// Kind is the plan line type (FR-44.4).
type Kind string

const (
	KindTime           Kind = "TIME"
	KindCounter        Kind = "COUNTER"
	KindTimeOrCounter  Kind = "TIME_OR_COUNTER"
	KindTimeAndCounter Kind = "TIME_AND_COUNTER"
	// KindRound schedules a maintenance round on time only (BR-44.9); one work
	// order per occurrence carries every stop.
	KindRound Kind = "ROUND"
)

// ErrInvalidLine reports a plan line that cannot be scheduled.
var ErrInvalidLine = errors.New("eam: invalid plan line")

// Interval is a calendar interval such as 90 DAYS or 3 MONTHS.
type Interval struct {
	N    int
	Unit string
}

// PlanLine carries what the scheduler needs from a plan line.
type PlanLine struct {
	Kind                 Kind
	Interval             Interval
	CounterInterval      float64
	Start                time.Time
	LastCompletedAt      *time.Time
	LastCompletedReading float64
}

// CounterState is the counter's current reading and rolling usage (FR-44.3).
type CounterState struct {
	Last          float64
	AvgDailyUsage float64
}

// Schedule is the computed next due point of a line (BR-44.1 (a)-(c)).
type Schedule struct {
	NextDueAt      *time.Time
	NextDueReading *float64
	ForecastAt     *time.Time
	// Span is the time-interval length, used for the early tolerance window.
	Span time.Duration
}

// AddInterval adds a calendar interval to t. Months and years land on the same
// day of the target month, or on its last day when that month is shorter
// (Jan 31 + 1 month = Feb 28, Feb 29 + 1 year = Feb 28), as calendar
// arithmetic does in SAP, D365 and PostgreSQL; time.AddDate would roll over
// into the next month instead (Jan 31 + 1 month = Mar 3).
func AddInterval(t time.Time, iv Interval) (time.Time, error) {
	if iv.N < 1 {
		return t, ErrInvalidLine
	}
	switch iv.Unit {
	case "DAYS":
		return t.AddDate(0, 0, iv.N), nil
	case "WEEKS":
		return t.AddDate(0, 0, 7*iv.N), nil
	case "MONTHS":
		return addMonths(t, iv.N), nil
	case "YEARS":
		return addMonths(t, 12*iv.N), nil
	}
	return t, fmt.Errorf("%w: unit %q", ErrInvalidLine, iv.Unit)
}

// addMonths adds n months to t, clamping the day to the target month's length.
func addMonths(t time.Time, n int) time.Time {
	y, m, d := t.Date()
	first := time.Date(y, m+time.Month(n), 1, t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	if last := first.AddDate(0, 1, -1).Day(); d > last {
		d = last
	}
	return first.AddDate(0, 0, d-1)
}

// Compute implements BR-44.1 (a)-(c): the time due T from the last completion
// (or the start), the counter due R = last completed reading + interval and the
// forecast F = now + (R − current) ÷ average daily usage, in whole days rounded up. Without usage the
// forecast is unknown and counter-only or AND lines have no due point.
func Compute(line PlanLine, c CounterState, now time.Time) (Schedule, error) {
	var s Schedule
	hasTime := line.Kind == KindTime || line.Kind == KindRound || line.Kind == KindTimeOrCounter || line.Kind == KindTimeAndCounter
	hasCounter := line.Kind == KindCounter || line.Kind == KindTimeOrCounter || line.Kind == KindTimeAndCounter
	if !hasTime && !hasCounter {
		return s, fmt.Errorf("%w: kind %q", ErrInvalidLine, line.Kind)
	}
	var due *time.Time
	if hasTime {
		// Calendar arithmetic runs in UTC so the due date never depends on the
		// server's time zone or its daylight-saving changes.
		base := line.Start.UTC()
		if line.LastCompletedAt != nil {
			base = line.LastCompletedAt.UTC()
		}
		t, err := AddInterval(base, line.Interval)
		if err != nil {
			return s, err
		}
		s.Span = t.Sub(base)
		due = &t
	}
	var reading *float64
	var forecast *time.Time
	if hasCounter {
		if line.CounterInterval <= 0 {
			return s, fmt.Errorf("%w: counter interval must be positive", ErrInvalidLine)
		}
		r := line.LastCompletedReading + line.CounterInterval
		reading = &r
		if c.AvgDailyUsage > 0 {
			remaining := r - c.Last
			if remaining < 0 {
				remaining = 0
			}
			// The forecast is whole days ahead, rounded up (BR-44.1 example:
			// 200 h at 6.5 h/day is 30.8 days, so 31 days).
			days := int(math.Ceil(remaining/c.AvgDailyUsage - 1e-9))
			f := now.UTC().AddDate(0, 0, days)
			forecast = &f
		}
	}
	s.ForecastAt = forecast
	switch line.Kind {
	case KindTime, KindRound:
		s.NextDueAt = due
	case KindCounter:
		s.NextDueAt = forecast
		s.NextDueReading = reading
	case KindTimeOrCounter:
		s.NextDueReading = reading
		s.NextDueAt = earlier(due, forecast)
	case KindTimeAndCounter:
		s.NextDueReading = reading
		if due != nil && forecast != nil {
			s.NextDueAt = later(due, forecast)
		}
	}
	return s, nil
}

func earlier(a, b *time.Time) *time.Time {
	if a == nil {
		return b
	}
	if b == nil || a.Before(*b) {
		return a
	}
	return b
}

func later(a, b *time.Time) *time.Time {
	if a.After(*b) {
		return a
	}
	return b
}

// Generate reports whether a work order is due to be created (BR-44.1 (d)):
// next due − early tolerance (percent of the interval span) ≤ now + horizon.
func Generate(s Schedule, earlyTolerancePct, horizonDays int, now time.Time) bool {
	if s.NextDueAt == nil {
		return false
	}
	window := time.Duration(float64(s.Span) * float64(earlyTolerancePct) / 100)
	return !s.NextDueAt.Add(-window).After(now.Add(time.Duration(horizonDays) * 24 * time.Hour))
}
