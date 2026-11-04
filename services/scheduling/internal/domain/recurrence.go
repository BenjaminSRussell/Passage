package domain

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Recurrence is a recurring capacity block window (FR-23.17), for example
// "every Monday 06:00-14:00". Rule is a subset of the RFC 5545 RRULE:
//
//	FREQ=DAILY|WEEKLY   required
//	INTERVAL=n          optional, default 1 (days or weeks)
//	BYDAY=MO,TU,...     weekly only; default is the weekday of StartDate
//	COUNT=n             optional, counts occurrences from StartDate
//	UNTIL=YYYYMMDD      optional, the last local date (a time part is ignored)
//
// StartDate is the first local date (DTSTART). StartMinute and EndMinute give
// the window on each occurrence in site-local minutes after midnight, so a
// DST day keeps its true length (BR-23.7). A window lies within one local day.
type Recurrence struct {
	Rule        string
	StartDate   time.Time
	StartMinute int
	EndMinute   int
}

// maxRuleLength bounds the stored rule text.
const maxRuleLength = 200

// rrule is the parsed subset of an RRULE.
type rrule struct {
	freq     string
	interval int
	byDay    []time.Weekday
	count    int
	until    time.Time // the UNTIL date at UTC midnight; zero when absent
}

var weekdayCodes = map[string]time.Weekday{
	"SU": time.Sunday, "MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday,
}

// parseRRule parses the supported RRULE subset and refuses anything else, so
// a rule the expander does not understand never yields wrong capacity.
func parseRRule(s string) (rrule, error) {
	r := rrule{interval: 1}
	seen := map[string]bool{}
	for _, part := range strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "RRULE:"), ";") {
		k, v, ok := strings.Cut(part, "=")
		if !ok || v == "" {
			return rrule{}, fmt.Errorf("rule part %q needs a value", part)
		}
		if seen[k] {
			return rrule{}, fmt.Errorf("rule part %s is given twice", k)
		}
		seen[k] = true
		switch k {
		case "FREQ":
			if v != "DAILY" && v != "WEEKLY" {
				return rrule{}, fmt.Errorf("frequency %q is not supported; use DAILY or WEEKLY", v)
			}
			r.freq = v
		case "INTERVAL":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 365 {
				return rrule{}, fmt.Errorf("interval %q must be 1 to 365", v)
			}
			r.interval = n
		case "COUNT":
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 10000 {
				return rrule{}, fmt.Errorf("count %q must be 1 to 10000", v)
			}
			r.count = n
		case "UNTIL":
			if len(v) < 8 {
				return rrule{}, fmt.Errorf("until %q must start with YYYYMMDD", v)
			}
			u, err := time.Parse("20060102", v[:8])
			if err != nil {
				return rrule{}, fmt.Errorf("until %q is not a date", v)
			}
			r.until = u
		case "BYDAY":
			for _, code := range strings.Split(v, ",") {
				wd, ok := weekdayCodes[code]
				if !ok {
					return rrule{}, fmt.Errorf("byday %q is not a plain weekday code (MO, TU, ...)", code)
				}
				if !slices.Contains(r.byDay, wd) {
					r.byDay = append(r.byDay, wd)
				}
			}
		default:
			return rrule{}, fmt.Errorf("rule part %s is not supported", k)
		}
	}
	switch {
	case r.freq == "":
		return rrule{}, errors.New("rule needs FREQ")
	case r.freq == "DAILY" && len(r.byDay) > 0:
		return rrule{}, errors.New("BYDAY is only supported with FREQ=WEEKLY")
	case r.count > 0 && !r.until.IsZero():
		return rrule{}, errors.New("give COUNT or UNTIL, not both")
	}
	return r, nil
}

// Validate checks the rule and the window.
func (rc Recurrence) Validate() error {
	if len(rc.Rule) > maxRuleLength {
		return fmt.Errorf("rule is longer than %d characters", maxRuleLength)
	}
	r, err := parseRRule(rc.Rule)
	if err != nil {
		return err
	}
	if rc.StartDate.IsZero() {
		return errors.New("recurrence needs a start date")
	}
	if rc.StartMinute < 0 || rc.EndMinute > 1440 || rc.EndMinute <= rc.StartMinute {
		return errors.New("startMinute and endMinute must satisfy 0 <= start < end <= 1440")
	}
	if !r.until.IsZero() {
		y, m, d := rc.StartDate.Date()
		if r.until.Before(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)) {
			return errors.New("UNTIL is before the start date")
		}
	}
	return nil
}

// mondayOffset is the day index within a Monday-first week (WKST=MO).
func mondayOffset(wd time.Weekday) int { return (int(wd) + 6) % 7 }

// occurrences lists the local dates (as day offsets from first) of the rule
// from first up to and including the offset last, in order.
func (r rrule) occurrences(first time.Time, last int, until int) []int {
	var out []int
	// add reports whether generation may continue past day n.
	add := func(n int) bool {
		if n > last || (until >= 0 && n > until) || (r.count > 0 && len(out) >= r.count) {
			return false
		}
		if n >= 0 {
			out = append(out, n)
		}
		return true
	}
	if r.freq == "DAILY" {
		for n := 0; ; n += r.interval {
			if !add(n) {
				return out
			}
		}
	}
	days := slices.Clone(r.byDay)
	if len(days) == 0 {
		days = []time.Weekday{first.Weekday()}
	}
	slices.SortFunc(days, func(a, b time.Weekday) int { return mondayOffset(a) - mondayOffset(b) })
	// Week 0 is the Monday-first week holding the start date; INTERVAL counts weeks.
	weekStart := -mondayOffset(first.Weekday())
	for w := 0; ; w++ {
		base := weekStart + 7*w*r.interval
		if base > last || (until >= 0 && base > until) {
			return out
		}
		for _, wd := range days {
			if !add(base + mondayOffset(wd)) {
				return out
			}
		}
	}
}

// Windows returns the UTC windows of the recurrence in loc that overlap
// [from, to), in time order.
func (rc Recurrence) Windows(loc *time.Location, from, to time.Time) ([]Interval, error) {
	if err := rc.Validate(); err != nil {
		return nil, err
	}
	if !to.After(from) {
		return nil, nil
	}
	if loc == nil {
		loc = time.UTC
	}
	r, _ := parseRRule(rc.Rule)
	y, m, d := rc.StartDate.Date()
	first := time.Date(y, m, d, 0, 0, 0, 0, loc)
	dayIndex := func(t time.Time) int {
		ty, tm, td := t.Date()
		return int(time.Date(ty, tm, td, 0, 0, 0, 0, time.UTC).Sub(time.Date(y, m, d, 0, 0, 0, 0, time.UTC)) / (24 * time.Hour))
	}
	last := dayIndex(to.In(loc))
	until := -1
	if !r.until.IsZero() {
		until = dayIndex(r.until)
	}
	var out []Interval
	for _, n := range r.occurrences(first, last, until) {
		// Wall times in a DST gap or overlap resolve as the shifts of the
		// calendar do (OpenIntervals), so a recurring and a weekly window agree.
		start := firstInstantAt(loc, y, m, d+n, rc.StartMinute).UTC()
		end := firstInstantAt(loc, y, m, d+n, rc.EndMinute).UTC()
		if end.After(start) && end.After(from) && start.Before(to) {
			out = append(out, Interval{Start: start, End: end})
		}
	}
	return out, nil
}
