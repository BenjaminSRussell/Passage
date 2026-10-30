package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Recurring journal frequencies (FR-11.10).
const (
	FreqMonthly   = "monthly"
	FreqQuarterly = "quarterly"
	FreqYearly    = "yearly"

	AmountFixed   = "fixed"
	AmountPercent = "percent"

	// LastDay as a day of month means the last calendar day of the month.
	LastDay = 0
)

// ValidFrequency reports whether f is a supported frequency.
func ValidFrequency(f string) bool {
	return f == FreqMonthly || f == FreqQuarterly || f == FreqYearly
}

func monthsPerStep(freq string) int {
	switch freq {
	case FreqQuarterly:
		return 3
	case FreqYearly:
		return 12
	}
	return 1
}

// scheduleDate is the run date inside the month of (year, month): dom 0 is the
// last day, and a day beyond the month's length is clipped to its last day.
func scheduleDate(year int, month time.Month, dom int) time.Time {
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC)
	if dom == LastDay || dom >= last.Day() {
		return last
	}
	return time.Date(year, month, dom, 0, 0, 0, 0, time.UTC)
}

// FirstRun is the first scheduled date on or after start. Quarterly and yearly
// schedules anchor on the month of start.
func FirstRun(freq string, dom int, start time.Time) time.Time {
	return RunOnOrAfter(freq, dom, start, start)
}

// RunOnOrAfter is the first date of the schedule that starts at start (steps
// of one, three or twelve months counted from the month of start) on or after
// from. Every date the schedule produces comes from here, so quarterly and
// yearly schedules keep their anchor month across runs, pauses and catch-ups.
func RunOnOrAfter(freq string, dom int, start, from time.Time) time.Time {
	if from.Before(start) {
		from = start
	}
	step := monthsPerStep(freq)
	months := (from.Year()-start.Year())*12 + int(from.Month()-start.Month())
	k := months / step
	for {
		anchor := time.Date(start.Year(), start.Month()+time.Month(k*step), 1, 0, 0, 0, 0, time.UTC)
		d := scheduleDate(anchor.Year(), anchor.Month(), dom)
		if !d.Before(from) && !d.Before(start) {
			return d
		}
		k++
	}
}

// RunAfter is the first scheduled date strictly after after.
func RunAfter(freq string, dom int, start, after time.Time) time.Time {
	return RunOnOrAfter(freq, dom, start, after.AddDate(0, 0, 1))
}

// OnSchedule reports whether d is one of the schedule's dates.
func OnSchedule(freq string, dom int, start, d time.Time) bool {
	return !d.Before(start) && RunOnOrAfter(freq, dom, start, d).Equal(d)
}

// ErrNoBase is returned when a percentage line has no base amount to apply to.
var ErrNoBase = errors.New("percentage lines need a base amount")

// RecurringLine is a template line: a fixed amount, or a percentage (0 to
// 1000) of the base amount supplied when the template is generated.
type RecurringLine struct {
	Account     string
	Dimensions  map[string]string
	DebitCredit string
	Kind        string
	Amount      string
	Currency    string
	Text        string
}

// ResolvedLine is a generated batch line amount.
type ResolvedLine struct {
	RecurringLine
	Resolved string
}

// ResolveAmounts turns template lines into amounts. Percentage lines are
// base * pct / 100 rounded half-up to the currency's minor units; money is
// decimal throughout.
func ResolveAmounts(lines []RecurringLine, base *decimal.Decimal) ([]ResolvedLine, error) {
	out := make([]ResolvedLine, 0, len(lines))
	for i, l := range lines {
		amount, err := decimal.NewFromString(l.Amount)
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid amount %q", i+1, l.Amount)
		}
		if l.Kind == AmountPercent {
			if base == nil {
				return nil, ErrNoBase
			}
			amount = base.Mul(amount).Div(decimal.NewFromInt(100))
		}
		amount = amount.Round(money.MinorUnits(l.Currency))
		out = append(out, ResolvedLine{RecurringLine: l, Resolved: amount.StringFixed(money.MinorUnits(l.Currency))})
	}
	return out, nil
}
