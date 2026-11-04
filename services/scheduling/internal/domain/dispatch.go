package domain

import (
	"sort"
	"time"
)

// DispatchRow is one booked segment on a resource as the store reads it for a
// dispatch list: the booking plus the operation attributes a supervisor needs
// at the machine.
type DispatchRow struct {
	JobID          string
	OrderID        string
	OperNumber     int
	Segment        int
	Role           string
	Start, End     time.Time
	SetupGroup     string
	ChangeoverMins int
	Finite         bool
	Firmed         bool
	LateMinutes    int
}

// DispatchLine is a numbered line of the dispatch list (PRD 4.23.15).
// MinutesInWindow is the part of the segment inside the asked window.
type DispatchLine struct {
	Sequence        int       `json:"sequence"`
	JobID           string    `json:"jobId"`
	OrderID         string    `json:"orderId"`
	OperNumber      int       `json:"operNumber"`
	Segment         int       `json:"segment"`
	Role            string    `json:"role"`
	Start           time.Time `json:"start"`
	End             time.Time `json:"end"`
	MinutesInWindow int       `json:"minutesInWindow"`
	SetupGroup      string    `json:"setupGroup,omitempty"`
	ChangeoverMins  int       `json:"changeoverMinutes"`
	Finite          bool      `json:"finite"`
	Firmed          bool      `json:"firmed"`
	LateMinutes     int       `json:"lateMinutes"`
}

// DispatchWindow turns a resource-local calendar date, a number of days and a
// local minute-of-day range into UTC instants: [startMinute of the first
// day, endMinute of the last day). A shift that ends after midnight passes an
// endMinute above 1440 and so runs into the next day. Both ends are wall-clock
// times of the resource, so a window across a DST change holds the hours the
// clock actually showed (a 22:00 to 06:00 shift is 7 hours on the spring night
// and 9 on the autumn night). See firstInstantAt for times in a gap or overlap.
// The window is empty (from equals to) only when both ends fall in one gap.
func DispatchWindow(loc *time.Location, date time.Time, days, startMinute, endMinute int) (time.Time, time.Time) {
	if loc == nil {
		loc = time.UTC
	}
	y, m, d := date.Date()
	return firstInstantAt(loc, y, m, d, startMinute).UTC(), firstInstantAt(loc, y, m, d+days-1, endMinute).UTC()
}

// firstInstantAt is the first instant at which the clock of loc shows the
// local date and minute or later. A wall time skipped by a spring-forward gap
// maps to the instant of the change (time.Date would move it an hour back,
// into time the asked window does not cover); a wall time repeated by an
// autumn overlap maps to its first occurrence.
func firstInstantAt(loc *time.Location, y int, m time.Month, d, minute int) time.Time {
	t := time.Date(y, m, d, 0, minute, 0, 0, loc)
	want := time.Date(y, m, d, 0, minute, 0, 0, time.UTC)
	got := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, time.UTC)
	switch start, end := t.ZoneBounds(); {
	case got.Before(want) && !end.IsZero():
		return end
	case got.After(want) && !start.IsZero():
		return start
	}
	return t
}

// DispatchLines orders the rows by start, then end, then job and segment, and
// numbers them from 1; ties never reshuffle between reads. It returns the lines
// and the minutes they hold inside [from, to).
func DispatchLines(rows []DispatchRow, from, to time.Time) ([]DispatchLine, int) {
	sorted := append([]DispatchRow(nil), rows...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		switch {
		case !a.Start.Equal(b.Start):
			return a.Start.Before(b.Start)
		case !a.End.Equal(b.End):
			return a.End.Before(b.End)
		case a.JobID != b.JobID:
			return a.JobID < b.JobID
		case a.Segment != b.Segment:
			return a.Segment < b.Segment
		}
		return a.Role < b.Role
	})
	out := make([]DispatchLine, 0, len(sorted))
	total := 0
	for i, r := range sorted {
		in := ClippedMinutes(r.Start, r.End, from, to)
		total += in
		out = append(out, DispatchLine{Sequence: i + 1, JobID: r.JobID, OrderID: r.OrderID, OperNumber: r.OperNumber,
			Segment: r.Segment, Role: r.Role, Start: r.Start.UTC(), End: r.End.UTC(), MinutesInWindow: in,
			SetupGroup: r.SetupGroup, ChangeoverMins: r.ChangeoverMins, Finite: r.Finite, Firmed: r.Firmed, LateMinutes: r.LateMinutes})
	}
	return out, total
}
