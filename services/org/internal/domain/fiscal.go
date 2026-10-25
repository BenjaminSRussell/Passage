package domain

import (
	"fmt"
	"time"
)

const (
	CodeCalPattern = "ORG-CAL-PATTERN"
	CodeCalGap     = "ORG-CAL-GAP"
	CodeCalOverlap = "ORG-CAL-OVERLAP"
)

const (
	dateLayout    = "2006-01-02"
	minFiscalYear = 1900
	maxFiscalYear = 2999
	maxCustom     = 24
)

type Pattern string

const (
	PatternCalendar Pattern = "calendar"
	Pattern445      Pattern = "4-4-5"
	Pattern454      Pattern = "4-5-4"
	Pattern544      Pattern = "5-4-4"
	Pattern13       Pattern = "13-period"
	PatternCustom   Pattern = "custom"
)

func ParsePattern(s string) (Pattern, error) {
	switch p := Pattern(s); p {
	case PatternCalendar, Pattern445, Pattern454, Pattern544, Pattern13, PatternCustom:
		return p, nil
	}
	return "", &Error{CodeCalPattern, fmt.Sprintf("unknown fiscal pattern %q", s)}
}

type PeriodType string

const (
	PeriodOpening    PeriodType = "opening"
	PeriodOperating  PeriodType = "operating"
	PeriodClosing    PeriodType = "closing"
	PeriodAdjustment PeriodType = "adjustment"
)

type DateRange struct{ Start, End time.Time }

type CalendarSpec struct {
	Pattern        Pattern
	StartMonth     time.Month
	WeekStart      time.Weekday
	OpeningClosing bool
	SpecialPeriods int
	Custom         []DateRange
}

type FiscalPeriod struct {
	FiscalYear, PeriodNo int
	Name                 string
	Start, End           time.Time
	Type                 PeriodType
	Week53               bool
}

var weekSplits = map[Pattern][]int{
	Pattern445: {4, 4, 5, 4, 4, 5, 4, 4, 5, 4, 4, 5},
	Pattern454: {4, 5, 4, 4, 5, 4, 4, 5, 4, 4, 5, 4},
	Pattern544: {5, 4, 4, 5, 4, 4, 5, 4, 4, 5, 4, 4},
	Pattern13:  {4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4, 4},
}

func badSpec(format string, args ...any) error {
	return &Error{CodeCalPattern, fmt.Sprintf(format, args...)}
}

func utcDate(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func (s CalendarSpec) validate(year int) error {
	if _, err := ParsePattern(string(s.Pattern)); err != nil {
		return err
	}
	switch {
	case s.StartMonth < time.January || s.StartMonth > time.December:
		return badSpec("start month %d is outside 1-12", int(s.StartMonth))
	case s.SpecialPeriods < 0 || s.SpecialPeriods > 4:
		return badSpec("special periods %d is outside 0-4", s.SpecialPeriods)
	case year < minFiscalYear || year > maxFiscalYear:
		return badSpec("year %d is outside %d-%d", year, minFiscalYear, maxFiscalYear)
	}
	if s.Pattern != PatternCustom {
		return nil
	}
	if len(s.Custom) == 0 || len(s.Custom) > maxCustom {
		return badSpec("custom calendar needs 1-%d ranges, got %d", maxCustom, len(s.Custom))
	}
	for i, r := range s.Custom {
		if r.End.Before(r.Start) {
			return badSpec("custom range %d ends before it starts", i+1)
		}
	}
	return nil
}

func yearBounds(spec CalendarSpec, year int) (start, end time.Time) {
	switch spec.Pattern {
	case PatternCustom:
		return spec.Custom[0].Start, spec.Custom[len(spec.Custom)-1].End
	case PatternCalendar:
		return utcDate(year, spec.StartMonth, 1), utcDate(year+1, spec.StartMonth, 1).AddDate(0, 0, -1)
	}
	return weekYearEnd(spec, year-1).AddDate(0, 0, 1), weekYearEnd(spec, year)
}

func weekYearEnd(spec CalendarSpec, year int) time.Time {
	nominalEnd := utcDate(year+1, spec.StartMonth, 1).AddDate(0, 0, -1)
	lastDay := (int(spec.WeekStart)%7 + 6) % 7
	back := (int(nominalEnd.Weekday()) - lastDay + 7) % 7
	if back <= 3 {
		return nominalEnd.AddDate(0, 0, -back)
	}
	return nominalEnd.AddDate(0, 0, 7-back)
}

func singleDay(year, no int, name string, day time.Time, t PeriodType) FiscalPeriod {
	return FiscalPeriod{FiscalYear: year, PeriodNo: no, Name: name, Start: day, End: day, Type: t}
}

func operatingPeriods(spec CalendarSpec, year int, start, end time.Time) []FiscalPeriod {
	var ps []FiscalPeriod
	add := func(s, e time.Time) {
		no := len(ps) + 1
		ps = append(ps, FiscalPeriod{FiscalYear: year, PeriodNo: no, Name: fmt.Sprintf("P%02d", no), Start: s, End: e, Type: PeriodOperating})
	}
	switch spec.Pattern {
	case PatternCustom:
		for _, r := range spec.Custom {
			add(r.Start, r.End)
		}
	case PatternCalendar:
		for i := 0; i < 12; i++ {
			s := utcDate(year, spec.StartMonth+time.Month(i), 1)
			add(s, s.AddDate(0, 1, -1))
		}
	default:
		weeks := weekSplits[spec.Pattern]
		longYear := int(end.Sub(start).Hours()/24)+1 == 371
		cursor := start
		for i, w := range weeks {
			days := w * 7
			last := i == len(weeks)-1
			if last && longYear {
				days += 7
			}
			add(cursor, cursor.AddDate(0, 0, days-1))
			ps[i].Week53 = last && longYear
			cursor = cursor.AddDate(0, 0, days)
		}
	}
	return ps
}

func GenerateYear(spec CalendarSpec, year int) ([]FiscalPeriod, error) {
	if err := spec.validate(year); err != nil {
		return nil, err
	}
	start, end := yearBounds(spec, year)
	operating := operatingPeriods(spec, year, start, end)
	var periods []FiscalPeriod
	if spec.OpeningClosing {
		periods = append(periods, singleDay(year, 0, "Opening", start, PeriodOpening))
	}
	periods = append(periods, operating...)
	for i := 1; i <= spec.SpecialPeriods; i++ {
		no := len(operating) + i
		periods = append(periods, singleDay(year, no, fmt.Sprintf("A%d", no), end, PeriodAdjustment))
	}
	if spec.OpeningClosing {
		periods = append(periods, singleDay(year, 99, "Closing", end, PeriodClosing))
	}
	if err := ProveContiguous(periods, DateRange{start, end}); err != nil {
		return nil, err
	}
	return periods, nil
}

func ProveContiguous(periods []FiscalPeriod, year DateRange) error {
	for _, p := range periods {
		if p.End.Before(p.Start) {
			return badSpec("period %s ends before it starts", p.Name)
		}
		var anchor time.Time
		switch p.Type {
		case PeriodOpening:
			anchor = year.Start
		case PeriodClosing, PeriodAdjustment:
			anchor = year.End
		default:
			continue
		}
		if !p.Start.Equal(anchor) || !p.End.Equal(anchor) {
			return badSpec("period %s must be the single day %s", p.Name, anchor.Format(dateLayout))
		}
	}
	next := year.Start
	for _, p := range periods {
		if p.Type != PeriodOperating {
			continue
		}
		if p.Start.After(next) {
			return &Error{CodeCalGap, "periods leave a gap on " + next.Format(dateLayout)}
		}
		if p.Start.Before(next) {
			return &Error{CodeCalOverlap, "periods overlap on " + p.Start.Format(dateLayout)}
		}
		next = p.End.AddDate(0, 0, 1)
	}
	after := year.End.AddDate(0, 0, 1)
	if next.Before(after) {
		return &Error{CodeCalGap, "periods leave a gap on " + next.Format(dateLayout)}
	}
	if next.After(after) {
		return &Error{CodeCalOverlap, "periods overlap on " + after.Format(dateLayout)}
	}
	return nil
}
