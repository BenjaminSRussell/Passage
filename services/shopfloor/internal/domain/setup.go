package domain

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Day types a profile can define (ProfileDay.day_type, PRD 4.25.4). A day type a
// profile does not define uses DefaultProfile.
const (
	DayTypeWeekday  = "weekday"
	DayTypeSaturday = "saturday"
	DayTypeSunday   = "sunday"
)

// DayTypes lists the day types in display order.
func DayTypes() []string { return []string{DayTypeWeekday, DayTypeSaturday, DayTypeSunday} }

// DayTypeOf is the day type of a local calendar date.
func DayTypeOf(t time.Time) string {
	switch t.Weekday() {
	case time.Saturday:
		return DayTypeSaturday
	case time.Sunday:
		return DayTypeSunday
	}
	return DayTypeWeekday
}

// ProfileSet holds the day rules of one worker's profile by day type.
type ProfileSet map[string]Profile

// For returns the profile of the local day that contains t (in loc), or the
// BR-25.2 default when the set does not define that day type.
func (s ProfileSet) For(t time.Time, loc *time.Location) Profile {
	if p, ok := s[DayTypeOf(t.In(loc))]; ok {
		return p
	}
	return DefaultProfile()
}

// PaySetup is what the pay calculation needs beyond the punches: the worker's
// day rules and pay agreement. The zero value means the launch defaults.
type PaySetup struct {
	Profiles ProfileSet
	// Rules is nil when the worker has no pay agreement.
	Rules *Rules
}

// EffectiveRules is the worker's agreement rules or the FLSA launch defaults.
func (s PaySetup) EffectiveRules() Rules {
	if s.Rules != nil {
		return *s.Rules
	}
	return DefaultRules()
}

// BreakInput is one unpaid break window as "HH:MM" local times.
type BreakInput struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ProfileDayInput is one day type of a profile as the API receives and stores it.
type ProfileDayInput struct {
	DayType            string       `json:"dayType"`
	ShiftStart         *string      `json:"shiftStart"`
	ShiftEnd           *string      `json:"shiftEnd"`
	Breaks             []BreakInput `json:"breaks"`
	RoundingMinutes    int          `json:"roundingMinutes"`
	ToleranceBeforeMin int          `json:"toleranceBeforeMin"`
	ToleranceAfterMin  int          `json:"toleranceAfterMin"`
	FlexAllowed        bool         `json:"flexAllowed"`
}

// ParseClock reads "HH:MM" as an offset from local midnight.
func ParseClock(s string) (time.Duration, bool) {
	if len(s) != 5 || s[2] != ':' {
		return 0, false
	}
	h, err1 := strconv.Atoi(s[:2])
	m, err2 := strconv.Atoi(s[3:])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, false
	}
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute, true
}

// Profile validates the input (BR-25.2: tolerances 0 to 60 minutes, rounding 1 to 15
// minutes, a shift with both ends and the end after the start, breaks that do not
// overlap) and returns the day rules. Problems are keyed by field path under prefix.
func (in ProfileDayInput) Profile(prefix string) (Profile, map[string]string) {
	fields := map[string]string{}
	p := Profile{ToleranceBefore: time.Duration(in.ToleranceBeforeMin) * time.Minute, ToleranceAfter: time.Duration(in.ToleranceAfterMin) * time.Minute, RoundingMinutes: in.RoundingMinutes}
	valid := false
	for _, d := range DayTypes() {
		valid = valid || d == in.DayType
	}
	if !valid {
		fields[prefix+"dayType"] = "must be weekday, saturday or sunday"
	}
	if in.RoundingMinutes < 1 || in.RoundingMinutes > 15 {
		fields[prefix+"roundingMinutes"] = "must be 1 to 15"
	}
	if in.ToleranceBeforeMin < 0 || in.ToleranceBeforeMin > 60 {
		fields[prefix+"toleranceBeforeMin"] = "must be 0 to 60"
	}
	if in.ToleranceAfterMin < 0 || in.ToleranceAfterMin > 60 {
		fields[prefix+"toleranceAfterMin"] = "must be 0 to 60"
	}
	if (in.ShiftStart == nil) != (in.ShiftEnd == nil) {
		fields[prefix+"shiftStart"] = "give both shiftStart and shiftEnd, or neither"
	} else if in.ShiftStart != nil {
		s, okS := ParseClock(*in.ShiftStart)
		e, okE := ParseClock(*in.ShiftEnd)
		switch {
		case !okS:
			fields[prefix+"shiftStart"] = "must be HH:MM"
		case !okE:
			fields[prefix+"shiftEnd"] = "must be HH:MM"
		case e <= s:
			fields[prefix+"shiftEnd"] = "must be after shiftStart"
		default:
			p.ShiftStart, p.ShiftEnd = &s, &e
		}
	}
	if len(in.Breaks) > 6 {
		fields[prefix+"breaks"] = "at most 6 breaks"
	}
	var windows []Window
	for i, b := range in.Breaks {
		from, okF := ParseClock(b.From)
		to, okT := ParseClock(b.To)
		key := fmt.Sprintf("%sbreaks[%d]", prefix, i)
		if !okF || !okT || to <= from {
			fields[key] = "from and to must be HH:MM with to after from"
			continue
		}
		windows = append(windows, Window{From: from, To: to})
	}
	sort.Slice(windows, func(i, j int) bool { return windows[i].From < windows[j].From })
	for i := 1; i < len(windows); i++ {
		if windows[i].From < windows[i-1].To {
			fields[prefix+"breaks"] = "breaks must not overlap"
		}
	}
	p.Breaks = windows
	return p, fields
}

// ProfileFromDays validates every day of a profile and builds its set. Each day
// type may appear once.
func ProfileFromDays(days []ProfileDayInput) (ProfileSet, map[string]string) {
	fields := map[string]string{}
	set := ProfileSet{}
	if len(days) > len(DayTypes()) {
		fields["days"] = "at most one entry per day type"
	}
	for i, d := range days {
		prefix := fmt.Sprintf("days[%d].", i)
		p, f := d.Profile(prefix)
		for k, v := range f {
			fields[k] = v
		}
		if _, dup := set[d.DayType]; dup {
			fields[prefix+"dayType"] = "each day type may appear once"
		}
		set[d.DayType] = p
	}
	return set, fields
}

// AgreementInput is a pay agreement as the API receives it: decimals travel as strings.
type AgreementInput struct {
	DailyThreshold     string `json:"dailyThreshold"`
	WeeklyThreshold    string `json:"weeklyThreshold"`
	OvertimeMultiplier string `json:"overtimeMultiplier"`
	SundayMultiplier   string `json:"sundayMultiplier"`
	// WeekStart is the first day of the pay week, 0 (Sunday) to 6 (Saturday).
	WeekStart int `json:"weekStart"`
}

// Rules validates the agreement (FR-25.7: daily threshold 0 to 24 h, weekly 0 to 168 h,
// multipliers from 1 to 5 with up to 4 decimals) and returns the pay rules.
func (in AgreementInput) Rules() (Rules, map[string]string) {
	fields := map[string]string{}
	num := func(field, raw string, lo, hi int64, scale int32) decimal.Decimal {
		d, err := decimal.NewFromString(strings.TrimSpace(raw))
		if err != nil || d.LessThan(decimal.NewFromInt(lo)) || d.GreaterThan(decimal.NewFromInt(hi)) || d.Exponent() < -scale {
			fields[field] = fmt.Sprintf("must be a decimal string from %d to %d with up to %d decimals", lo, hi, scale)
		}
		return d
	}
	r := Rules{
		DailyThreshold:     num("dailyThreshold", in.DailyThreshold, 0, 24, 2),
		WeeklyThreshold:    num("weeklyThreshold", in.WeeklyThreshold, 0, 168, 2),
		OvertimeMultiplier: num("overtimeMultiplier", in.OvertimeMultiplier, 1, 5, 4),
		SundayMultiplier:   num("sundayMultiplier", in.SundayMultiplier, 1, 5, 4),
		WeekStart:          time.Weekday(in.WeekStart),
	}
	if in.WeekStart < 0 || in.WeekStart > 6 {
		fields["weekStart"] = "must be 0 (Sunday) to 6 (Saturday)"
	}
	return r, fields
}

// FormatClock renders an offset from midnight as "HH:MM".
func FormatClock(d time.Duration) string {
	return fmt.Sprintf("%02d:%02d", int(d/time.Hour), int(d%time.Hour/time.Minute))
}
