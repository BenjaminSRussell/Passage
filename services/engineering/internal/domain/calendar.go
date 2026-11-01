package domain

import (
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

const (
	MaxShiftsPerDay  = 5
	MaxHorizonMonths = 60
	WeeklyHoursWarn  = 48
	minutesPerDay    = 1440
	dateKey          = "2006-01-02"
)

var weekdayNames = [7]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

var (
	minEfficiency = decimal.NewFromInt(1)
	minutesPerH   = decimal.NewFromInt(60)
	weeklyWarnH   = decimal.NewFromInt(WeeklyHoursWarn)
)

type Shift struct {
	Start         string           `json:"start"`
	End           string           `json:"end"`
	EfficiencyPct *decimal.Decimal `json:"efficiencyPct,omitempty"`
}

type WorkCalendar struct {
	Code        string
	Name        string
	Template    [7][]Shift
	ClosedDates []time.Time
}

type CalendarDay struct {
	Date   time.Time
	Open   bool
	Shifts []Shift
	TotalH decimal.Decimal
}

func clockMinutes(s string) (int, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	var h, m int
	for i, c := range []byte(s) {
		if i == 2 {
			continue
		}
		if c < '0' || c > '9' {
			return 0, false
		}
	}
	h = int(s[0]-'0')*10 + int(s[1]-'0')
	m = int(s[3]-'0')*10 + int(s[4]-'0')
	if m > 59 || h > 24 || (h == 24 && m != 0) {
		return 0, false
	}
	return h*60 + m, true
}

func (s Shift) span() (start, end int, err error) {
	start, okStart := clockMinutes(s.Start)
	end, okEnd := clockMinutes(s.End)
	if !okStart || !okEnd || start >= minutesPerDay {
		return 0, 0, errf(CodeCalShift, "shift %q-%q must use hh:mm", s.Start, s.End)
	}
	if end == start {
		return 0, 0, errf(CodeCalShift, "shift %q-%q has no duration", s.Start, s.End)
	}
	if end < start {
		end += minutesPerDay
	}
	if s.EfficiencyPct != nil {
		eff := *s.EfficiencyPct
		if eff.LessThan(minEfficiency) || eff.GreaterThan(hundred) || !eff.Round(2).Equal(eff) {
			return 0, 0, errf(CodeCalShift, "shift %q-%q efficiency must be 1 to 100 with at most two decimals", s.Start, s.End)
		}
	}
	return start, end, nil
}

func (s Shift) hours() decimal.Decimal {
	start, end, _ := s.span()
	return decimal.NewFromInt(int64(end - start)).Div(minutesPerH).Round(4)
}

func dayHours(shifts []Shift) decimal.Decimal {
	total := decimal.Zero
	for _, s := range shifts {
		total = total.Add(s.hours())
	}
	return total
}

func validateDay(shifts []Shift) error {
	if len(shifts) > MaxShiftsPerDay {
		return errf(CodeCalShift, "a day holds at most %d shifts, got %d", MaxShiftsPerDay, len(shifts))
	}
	type interval struct{ start, end int }
	spans := make([]interval, 0, len(shifts))
	for _, s := range shifts {
		start, end, err := s.span()
		if err != nil {
			return err
		}
		spans = append(spans, interval{start, end})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i-1].end > spans[i].start {
			return errf(CodeCalShift, "shifts overlap at %02d:%02d", spans[i].start/60, spans[i].start%60)
		}
	}
	return nil
}

// spillOverlap rejects a shift that runs past midnight into a shift of the next day, which would
// count the same hours twice.
func spillOverlap(day, next []Shift, nextName string) error {
	spill := 0
	for _, s := range day {
		if _, end, err := s.span(); err == nil && end-minutesPerDay > spill {
			spill = end - minutesPerDay
		}
	}
	for _, s := range next {
		if start, _, err := s.span(); err == nil && start < spill {
			return errf(CodeCalShift, "a shift running to %02d:%02d the next morning overlaps the %s shift %s-%s", spill/60, spill%60, nextName, s.Start, s.End)
		}
	}
	return nil
}

// dayError names the weekday in a shift error so the problem detail says which pattern to fix.
func dayError(day int, err error) error {
	var de *Error
	if errors.As(err, &de) {
		return &Error{Code: de.Code, Detail: weekdayNames[day] + ": " + de.Detail, Cause: de.Cause}
	}
	return err
}

// ValidateCalendar checks every day pattern (hh:mm shifts, 24:00 only as an end, an end before the
// start running past midnight, no overlaps within a day or into the next day, at most five shifts)
// and warns when the week holds more than 48 hours.
func ValidateCalendar(cal WorkCalendar) (warnings []string, err error) {
	week := decimal.Zero
	for i, shifts := range cal.Template {
		if err := validateDay(shifts); err != nil {
			return nil, dayError(i, err)
		}
		week = week.Add(dayHours(shifts))
	}
	for i, shifts := range cal.Template {
		next := (i + 1) % len(cal.Template)
		if err := spillOverlap(shifts, cal.Template[next], weekdayNames[next]); err != nil {
			return nil, dayError(i, err)
		}
	}
	if week.GreaterThan(weeklyWarnH) {
		warnings = append(warnings, fmt.Sprintf("weekly shift hours %s exceed %d", week.String(), WeeklyHoursWarn))
	}
	return warnings, nil
}

func Generate(cal WorkCalendar, from, to time.Time) ([]CalendarDay, error) {
	if from.IsZero() || to.IsZero() || to.Before(from) {
		return nil, errf(CodeCalRange, "range must have to on or after from")
	}
	if to.After(from.AddDate(0, MaxHorizonMonths, 0)) {
		return nil, errf(CodeCalRange, "range exceeds %d months", MaxHorizonMonths)
	}
	if _, err := ValidateCalendar(cal); err != nil {
		return nil, err
	}
	var hours [7]decimal.Decimal
	for i, shifts := range cal.Template {
		hours[i] = dayHours(shifts)
	}
	closed := make(map[string]bool, len(cal.ClosedDates))
	for _, d := range cal.ClosedDates {
		closed[d.Format(dateKey)] = true
	}
	days := make([]CalendarDay, 0, int(to.Sub(from).Hours()/24)+1)
	for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
		weekday := (int(d.Weekday()) + 6) % 7
		shifts := cal.Template[weekday]
		day := CalendarDay{Date: d}
		if len(shifts) > 0 && !closed[d.Format(dateKey)] {
			day.Open, day.Shifts, day.TotalH = true, shifts, hours[weekday]
		}
		days = append(days, day)
	}
	return days, nil
}
