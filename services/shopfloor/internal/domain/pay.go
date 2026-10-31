package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

const (
	TypeBase       = "base"
	TypeOvertime15 = "overtime-1.5"
	TypeOvertime20 = "overtime-2.0"
	// TypeShiftPremium is a supplement line: the hours worked inside a premium window,
	// paid at the window's premium rate on top of the base or overtime lines. Its hours
	// are part of the day's hours, not additional to them.
	TypeShiftPremium = "shift-premium"

	// MaxDailyHours is the PayLine invariant: at most 24.00 hours per worker-day.
	MaxDailyHours = 24
)

// Rules are the pay agreement thresholds and multipliers (FR-25.7 defaults).
// WeekStart is the first day of the pay week the weekly threshold counts over
// (BR-25.8, profile-defined start day).
type Rules struct {
	DailyThreshold     decimal.Decimal
	WeeklyThreshold    decimal.Decimal
	OvertimeMultiplier decimal.Decimal
	SundayMultiplier   decimal.Decimal
	WeekStart          time.Weekday
	// ShiftPremiums are the premium windows of the agreement (FR-25.7); none by default.
	ShiftPremiums []ShiftPremium
}

// DefaultRules is the US FLSA launch default: over 8.00 h/day and 40.00 h/week
// at 1.5x, Sunday at 2.0x, pay week starting Monday.
func DefaultRules() Rules {
	return Rules{
		DailyThreshold:     decimal.NewFromInt(8),
		WeeklyThreshold:    decimal.NewFromInt(40),
		OvertimeMultiplier: decimal.RequireFromString("1.5"),
		SundayMultiplier:   decimal.NewFromInt(2),
		WeekStart:          time.Monday,
	}
}

// WeekStartOf returns local midnight of the first day of the pay week that
// contains date, for a week beginning on start.
func WeekStartOf(date time.Time, start time.Weekday) time.Time {
	back := (int(date.Weekday()) - int(start) + 7) % 7
	return time.Date(date.Year(), date.Month(), date.Day()-back, 0, 0, 0, 0, date.Location())
}

// PayLine is one pay type amount for a worker-day.
type PayLine struct {
	Type       string
	Hours      decimal.Decimal
	Multiplier decimal.Decimal
	Amount     decimal.Decimal
	Sources    []string
}

// DayPay is a calculated worker-day.
type DayPay struct {
	Date    time.Time
	Hours   decimal.Decimal
	Flags   []string
	Lines   []PayLine
	Sources []string
}

// Calculation holds what one calculation run needs: the profile, the local
// time zone, the pay rules and the worker's cost rate in the given currency.
type Calculation struct {
	Profile Profile
	// ProfileFor, when set, picks the profile by local day (a worker's profile
	// defines break windows per day type) and replaces Profile.
	ProfileFor func(time.Time) Profile
	Location   *time.Location
	Rules      Rules
	Rate       decimal.Decimal
	Currency   string
}

// Run turns worker-days into pay lines. Days may span several pay weeks; the
// weekly threshold restarts at each WeekStart. To pay the first days of a range
// correctly the caller passes every earlier day of the same pay week too.
//
// Hours above the daily threshold are overtime-1.5. Base hours beyond the
// weekly threshold move to overtime-1.5 (BR-25.8): only base hours count toward
// the weekly threshold, so hours already paid at 1.5x daily are not counted
// twice. Sunday hours are overtime-2.0 and do not count toward the weekly
// threshold. The base and overtime lines of a day always sum to the day's hours;
// shift-premium lines are supplements on hours those lines already pay, so they are
// not added to the day's hours.
func (c Calculation) Run(days []Day) ([]DayPay, error) {
	ordered := append([]Day(nil), days...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Date.Before(ordered[j].Date) })
	out := make([]DayPay, 0, len(ordered))
	weekBase := decimal.Zero
	var week time.Time
	for _, d := range ordered {
		if ws := WeekStartOf(d.Date, c.Rules.WeekStart); !ws.Equal(week) {
			week, weekBase = ws, decimal.Zero
		}
		hours := decimal.Zero
		var sources []string
		for _, s := range d.Segments {
			hours = hours.Add(NetHours(s, c.profileAt(s.Start), c.Location))
			sources = append(sources, s.Sources...)
		}
		if hours.GreaterThan(decimal.NewFromInt(MaxDailyHours)) {
			return nil, &Error{Code: CodeHoursExceed, Detail: fmt.Sprintf("hours exceed 24.00 on %s", d.DateString())}
		}
		dp := DayPay{Date: d.Date, Hours: hours, Flags: d.Flags, Sources: sources}
		switch {
		case hours.IsZero():
		case d.Date.Weekday() == time.Sunday:
			dp.Lines = append(dp.Lines, c.line(TypeOvertime20, hours, c.Rules.SundayMultiplier, sources))
		default:
			base := decimal.Min(hours, c.Rules.DailyThreshold)
			daily := hours.Sub(base)
			room := decimal.Max(decimal.Zero, c.Rules.WeeklyThreshold.Sub(weekBase))
			keptBase := decimal.Min(base, room)
			weekBase = weekBase.Add(keptBase)
			if keptBase.IsPositive() {
				dp.Lines = append(dp.Lines, c.line(TypeBase, keptBase, decimal.NewFromInt(1), sources))
			}
			if over := daily.Add(base.Sub(keptBase)); over.IsPositive() {
				dp.Lines = append(dp.Lines, c.line(TypeOvertime15, over, c.Rules.OvertimeMultiplier, sources))
			}
		}
		dp.Lines = append(dp.Lines, c.premiumLines(d, sources)...)
		out = append(out, dp)
	}
	return out, nil
}

func (c Calculation) profileAt(t time.Time) Profile {
	if c.ProfileFor != nil {
		return c.ProfileFor(t)
	}
	return c.Profile
}

func (c Calculation) line(typ string, hours, mult decimal.Decimal, sources []string) PayLine {
	amount := money.Amount{Value: c.Rate, Currency: c.Currency}.Mul(hours.Mul(mult))
	return PayLine{Type: typ, Hours: hours, Multiplier: mult, Amount: amount.Value, Sources: sources}
}

// premiumLines adds one shift-premium line per premium window with hours in the
// day: the paid hours of the day's segments (breaks taken out) that fall inside the
// window, to 0.01 h. The line's multiplier is the premium rate, so the amount is
// rate x hours x premium.
func (c Calculation) premiumLines(d Day, sources []string) []PayLine {
	var out []PayLine
	for _, w := range c.Rules.ShiftPremiums {
		var span time.Duration
		for _, s := range d.Segments {
			span += c.windowSpan(s, w)
		}
		hours := decimal.NewFromInt(int64(span / time.Second)).Div(decimal.NewFromInt(3600)).Round(2)
		if hours.IsPositive() {
			out = append(out, c.line(TypeShiftPremium, hours, w.Premium, sources))
		}
	}
	return out
}

// windowSpan is the time of a segment inside a premium window, less the profile
// breaks. A segment may run past midnight, and a window whose end is not after its
// start crosses midnight, so the windows that begin the day before, the day of and
// the day after the segment's start all count. Window times are local wall-clock
// times, so a window keeps its clock hours on a daylight saving change.
func (c Calculation) windowSpan(s Segment, w ShiftPremium) time.Duration {
	local := s.Start.In(c.Location)
	mid := localMidnight(s.Start, c.Location)
	at := func(dayOffset int, offset time.Duration) time.Time {
		return time.Date(local.Year(), local.Month(), local.Day()+dayOffset, int(offset/time.Hour), int(offset%time.Hour/time.Minute), 0, 0, c.Location)
	}
	prof := c.profileAt(s.Start)
	var total time.Duration
	for k := -1; k <= 1; k++ {
		endDay := k
		if w.To <= w.From {
			endDay++
		}
		from, to := at(k, w.From), at(endDay, w.To)
		start, end := s.Start, s.End
		if from.After(start) {
			start = from
		}
		if to.Before(end) {
			end = to
		}
		if !end.After(start) {
			continue
		}
		span := end.Sub(start)
		for _, b := range prof.Breaks {
			span -= overlap(start, end, mid.Add(b.From), mid.Add(b.To))
		}
		if span > 0 {
			total += span
		}
	}
	return total
}
