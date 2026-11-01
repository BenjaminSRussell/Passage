package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

const (
	KindClockIn  = "clock-in"
	KindClockOut = "clock-out"

	// FlagMissingClockOut marks a clock-in with no clock-out (BR-25.3). The day
	// cannot be approved while the flag is open.
	FlagMissingClockOut = "missing-clock-out"
	// FlagOrphanClockOut marks a clock-out that follows no open clock-in.
	FlagOrphanClockOut = "orphan-clock-out"

	// CodeHoursExceed is SF-0007: a worker-day cannot exceed 24.00 hours.
	CodeHoursExceed = "SF-0007"

	dateLayout = "2006-01-02"
)

// Punch is one clock-in or clock-out registration.
type Punch struct {
	ID   string
	Kind string
	At   time.Time
	// Unconfirmed marks a clock-out the auto clock-out sweep wrote that no supervisor
	// has confirmed or corrected yet (BR-25.3). It closes the segment, so the hours
	// show, but the day keeps the missing-clock-out flag and cannot be approved.
	Unconfirmed bool
}

// Window is a daily window expressed as offsets from local midnight.
type Window struct{ From, To time.Duration }

// Profile holds the day rules a worker's profile supplies (ProfileDay).
// Shift boundaries are offsets from local midnight; nil means no shift.
type Profile struct {
	ShiftStart      *time.Duration
	ShiftEnd        *time.Duration
	ToleranceBefore time.Duration
	ToleranceAfter  time.Duration
	RoundingMinutes int
	Breaks          []Window
}

// DefaultProfile is the BR-25.2 default: 10 min before, 5 min after, 1 min rounding.
func DefaultProfile() Profile {
	return Profile{ToleranceBefore: 10 * time.Minute, ToleranceAfter: 5 * time.Minute, RoundingMinutes: 1}
}

// Segment is one closed clock-in/clock-out pair after rounding.
type Segment struct {
	Start, End time.Time
	Sources    []string
}

// Day is one local calendar day: its segments and open flags.
type Day struct {
	Date     time.Time
	Segments []Segment
	Flags    []string
	// Absences are the absence registrations that start on this day (FR-25.2).
	Absences []Absence
}

// DateString formats the day as the local calendar date (YYYY-MM-DD).
func (d Day) DateString() string { return d.Date.Format(dateLayout) }

func localMidnight(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
}

// Round applies BR-25.2 to one punch. A clock-in within the tolerance before
// shift start snaps to shift start; a clock-out within the tolerance after
// shift end snaps to shift end. Otherwise the punch rounds half-up to the
// profile's rounding interval within its local day.
func Round(at time.Time, kind string, p Profile, loc *time.Location) time.Time {
	midnight := localMidnight(at, loc)
	offset := at.In(loc).Sub(midnight)
	if p.ShiftStart != nil && kind == KindClockIn {
		s := *p.ShiftStart
		if offset <= s && s-offset <= p.ToleranceBefore {
			return midnight.Add(s)
		}
	}
	if p.ShiftEnd != nil && kind == KindClockOut {
		e := *p.ShiftEnd
		if offset >= e && offset-e <= p.ToleranceAfter {
			return midnight.Add(e)
		}
	}
	step := time.Duration(p.RoundingMinutes) * time.Minute
	if step <= 0 {
		step = time.Minute
	}
	return midnight.Add(((offset + step/2) / step) * step)
}

// BuildDays pairs punches (in time order) into segments and groups them by the
// local date on which each segment starts. An unpaired clock-in is flagged
// missing-clock-out and contributes no hours; a segment closed by an unconfirmed
// auto clock-out counts its hours and is flagged missing-clock-out too, until a
// supervisor confirms or corrects it (BR-25.3); a clock-out with no open clock-in
// is flagged orphan-clock-out. Segments that cross midnight belong to their
// start day in this slice.
func BuildDays(punches []Punch, loc *time.Location, p Profile) []Day {
	return BuildDaysFor(punches, loc, func(time.Time) Profile { return p })
}

// BuildDaysFor is BuildDays with the profile chosen per local day: a segment is
// rounded with the profile of the day its clock-in falls on (its pay day).
func BuildDaysFor(punches []Punch, loc *time.Location, profileFor func(time.Time) Profile) []Day {
	sorted := append([]Punch(nil), punches...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].At.Before(sorted[j].At) })
	byDate := map[string]*Day{}
	dayOf := func(t time.Time) *Day {
		mid := localMidnight(t, loc)
		key := mid.Format(dateLayout)
		d, ok := byDate[key]
		if !ok {
			d = &Day{Date: mid}
			byDate[key] = d
		}
		return d
	}
	var open *Punch
	for i := range sorted {
		pu := sorted[i]
		switch pu.Kind {
		case KindClockIn:
			if open != nil {
				addFlag(dayOf(open.At), FlagMissingClockOut)
			}
			open = &sorted[i]
		case KindClockOut:
			if open == nil {
				addFlag(dayOf(pu.At), FlagOrphanClockOut)
				continue
			}
			p := profileFor(open.At)
			start := Round(open.At, KindClockIn, p, loc)
			end := Round(pu.At, KindClockOut, p, loc)
			if end.After(start) {
				d := dayOf(start)
				d.Segments = append(d.Segments, Segment{Start: start, End: end, Sources: []string{open.ID, pu.ID}})
			}
			if pu.Unconfirmed {
				addFlag(dayOf(start), FlagMissingClockOut)
			}
			open = nil
		}
	}
	if open != nil {
		addFlag(dayOf(open.At), FlagMissingClockOut)
	}
	days := make([]Day, 0, len(byDate))
	for _, d := range byDate {
		days = append(days, *d)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Date.Before(days[j].Date) })
	return days
}

func addFlag(d *Day, flag string) {
	for _, f := range d.Flags {
		if f == flag {
			return
		}
	}
	d.Flags = append(d.Flags, flag)
}

// NetHours is a segment's paid hours: its length less the profile breaks that
// overlap it, rounded to 0.01 h (BR-25.1).
func NetHours(seg Segment, p Profile, loc *time.Location) decimal.Decimal {
	span := seg.End.Sub(seg.Start)
	mid := localMidnight(seg.Start, loc)
	for _, b := range p.Breaks {
		span -= overlap(seg.Start, seg.End, mid.Add(b.From), mid.Add(b.To))
	}
	if span < 0 {
		span = 0
	}
	seconds := int64(span / time.Second)
	return decimal.NewFromInt(seconds).Div(decimal.NewFromInt(3600)).Round(2)
}

func overlap(a0, a1, b0, b1 time.Time) time.Duration {
	start, end := a0, a1
	if b0.After(start) {
		start = b0
	}
	if b1.Before(end) {
		end = b1
	}
	if end.After(start) {
		return end.Sub(start)
	}
	return 0
}

// ElapsedHours is the elapsed time of a closed registration in hours to four
// places, the precision of TimeRegistration.hours. Breaks and the 0.01 h
// job-card rounding (BR-25.1) apply when the job card is built.
func ElapsedHours(start, stop time.Time) decimal.Decimal {
	if !stop.After(start) {
		return decimal.Zero
	}
	return decimal.NewFromInt(int64(stop.Sub(start) / time.Second)).Div(decimal.NewFromInt(3600)).Round(4)
}
