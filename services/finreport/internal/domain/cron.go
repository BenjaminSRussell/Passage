package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CronSpec is a parsed five-field cron expression (minute hour day-of-month
// month day-of-week). Supported syntax per field: *, n, a-b, lists with commas,
// and steps (*/n, a-b/n, n/n). Day-of-week 7 means Sunday (same as 0). When both
// day fields are restricted, a day matches if either matches; a day field that
// starts with * counts as unrestricted (Vixie cron).
type CronSpec struct {
	minute, hour, dom, month, dow [64]bool
	domStar, dowStar              bool
}

// cronRanges gives the inclusive bounds of each field.
var cronRanges = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

// cronHorizonYears bounds Next. Eight years always contain a 29 February, so
// every expression that can fire at all fires within the horizon.
const cronHorizonYears = 8

// ParseCron parses a five-field cron expression. It refuses expressions that can
// never fire, such as 30 February.
func ParseCron(expr string) (CronSpec, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return CronSpec{}, fmt.Errorf("cron must have 5 fields (minute hour day month weekday), got %d", len(fields))
	}
	var sets [5][64]bool
	for i, f := range fields {
		set, err := parseCronField(f, cronRanges[i][0], cronRanges[i][1])
		if err != nil {
			return CronSpec{}, fmt.Errorf("cron field %d (%s): %w", i+1, f, err)
		}
		sets[i] = set
	}
	if sets[4][7] {
		sets[4][7] = false
		sets[4][0] = true
	}
	c := CronSpec{
		minute: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4],
		domStar: strings.HasPrefix(fields[2], "*"), dowStar: strings.HasPrefix(fields[4], "*"),
	}
	if !c.canFire() {
		return CronSpec{}, errors.New("cron expression never fires: no selected month has the selected day")
	}
	return c, nil
}

func parseCronField(f string, lo, hi int) ([64]bool, error) {
	var out [64]bool
	selected := false
	for _, part := range strings.Split(f, ",") {
		step := 1
		if base, s, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 {
				return out, fmt.Errorf("step %q must be a positive integer", s)
			}
			step, part = n, base
		}
		from, to := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			x, errA := strconv.Atoi(a)
			y, errB := strconv.Atoi(b)
			if errA != nil || errB != nil {
				return out, fmt.Errorf("range %q is not numeric", part)
			}
			from, to = x, y
		default:
			n, err := strconv.Atoi(part)
			if err != nil {
				return out, fmt.Errorf("value %q is not numeric", part)
			}
			from, to = n, n
			if step != 1 {
				to = hi
			}
		}
		if from < lo || to > hi || from > to {
			return out, fmt.Errorf("%d-%d is outside %d-%d", from, to, lo, hi)
		}
		for v := from; v <= to; v += step {
			out[v] = true
			selected = true
		}
	}
	if !selected {
		return out, errors.New("field selects no values")
	}
	return out, nil
}

// daysIn is the longest length of a month (29 for February, leap years count).
var daysIn = [13]int{0, 31, 29, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}

// canFire reports whether some calendar day matches. The weekday field matches
// some day of every month, so only a day-of-month restriction that is combined
// with AND can rule every day out.
func (c CronSpec) canFire() bool {
	for m := 1; m <= 12; m++ {
		if !c.month[m] {
			continue
		}
		if !c.domStar && !c.dowStar {
			return true // either day field may match
		}
		for d := 1; d <= daysIn[m]; d++ {
			if c.dom[d] {
				return true
			}
		}
	}
	return false
}

func (c CronSpec) dayMatches(t time.Time) bool {
	if !c.month[int(t.Month())] {
		return false
	}
	domOK, dowOK := c.dom[t.Day()], c.dow[int(t.Weekday())]
	if !c.domStar && !c.dowStar {
		return domOK || dowOK
	}
	return domOK && dowOK
}

// Matches reports whether the wall-clock minute of t fires the expression.
func (c CronSpec) Matches(t time.Time) bool {
	return c.minute[t.Minute()] && c.hour[t.Hour()] && c.dayMatches(t)
}

// Next returns the first wall-clock minute strictly after from that fires, in
// from's location. It walks day by day, so it is cheap even for rare
// expressions. A wall-clock time skipped by a daylight-saving change does not
// fire on that day.
func (c CronSpec) Next(from time.Time) (time.Time, error) {
	loc := from.Location()
	start := from.Truncate(time.Minute).Add(time.Minute)
	y, m, d := start.Date()
	day := time.Date(y, m, d, 0, 0, 0, 0, loc)
	limit := day.AddDate(cronHorizonYears, 0, 0)
	for ; day.Before(limit); day = day.AddDate(0, 0, 1) {
		if !c.dayMatches(day) {
			continue
		}
		for h := 0; h < 24; h++ {
			if !c.hour[h] {
				continue
			}
			for mi := 0; mi < 60; mi++ {
				if !c.minute[mi] {
					continue
				}
				t := time.Date(day.Year(), day.Month(), day.Day(), h, mi, 0, 0, loc)
				if t.Hour() != h || t.Minute() != mi || t.Before(start) {
					continue // skipped by a clock change, or already past
				}
				return t, nil
			}
		}
	}
	return time.Time{}, errors.New("cron expression does not fire")
}
