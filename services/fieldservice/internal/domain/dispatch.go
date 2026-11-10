package domain

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Dispatch constants of BR-45.3.
const (
	// BufferMinutes separates two jobs of one technician.
	BufferMinutes = 5
	// OvertimeMinutes is the overtime a technician with overtime_allowed may add to the
	// shift end and to the daily maximum.
	OvertimeMinutes = 120
)

// Message codes of the assignment hard constraints (8.45.18 reserves SVC-DSP-001 to 003
// for the solver; SVC-DSP-003 is the skill code of the catalog).
const (
	CodeDspSkill     = "SVC-DSP-003"
	CodeDspEligible  = "SVC-DSP-004"
	CodeDspWindow    = "SVC-DSP-005"
	CodeDspShift     = "SVC-DSP-006"
	CodeDspOverlap   = "SVC-DSP-007"
	CodeDspDailyLoad = "SVC-DSP-008"
)

// Technician is the dispatchable part of a field technician (PRD 8.45.4).
type Technician struct {
	ID              string
	LegalEntityID   string
	TerritoryIDs    []string
	Skills          []string
	TimeZone        string
	ShiftDays       []int // ISO weekdays, Monday = 1
	ShiftStart      int   // minute of the local day
	ShiftEnd        int
	MaxDailyMinutes int
	OvertimeAllowed bool
	Active          bool
}

// Job is the schedulable part of a work order.
type Job struct {
	ID              string
	LegalEntityID   string
	TerritoryID     string
	RequiredSkills  []string
	WindowStart     *time.Time
	WindowEnd       *time.Time
	DurationMinutes int
}

// Slot is an interval already booked on a technician.
type Slot struct {
	Start, End time.Time
}

// Violation is one broken hard constraint.
type Violation struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// CheckAssignment returns every hard constraint of BR-45.3 that placing job at start on
// technician t breaks (nil when none): the technician is active, of the job's legal
// entity and territory (SVC-DSP-004); has every required skill (SVC-DSP-003); the job
// lies inside its time window (SVC-DSP-005) and inside one shift of the technician's
// local day (SVC-DSP-006); keeps the buffer to every other booked job (SVC-DSP-007);
// and the day's work plus buffers stays within the daily maximum (SVC-DSP-008). Overtime
// widens the shift end and the daily maximum by OvertimeMinutes. Travel time between
// jobs waits for the matrix client and is the fixed buffer for now. others are the
// technician's other bookings; the job's own current booking must not be among them.
func CheckAssignment(t Technician, j Job, start time.Time, others []Slot) []Violation {
	end := start.Add(time.Duration(j.DurationMinutes) * time.Minute)
	var out []Violation
	add := func(code, format string, args ...any) {
		out = append(out, Violation{code, fmt.Sprintf(format, args...)})
	}

	switch {
	case !t.Active:
		add(CodeDspEligible, "technician is not active")
	case t.LegalEntityID != j.LegalEntityID:
		add(CodeDspEligible, "technician belongs to another legal entity than the work order")
	case !slices.Contains(t.TerritoryIDs, j.TerritoryID):
		add(CodeDspEligible, "technician does not cover the work order's territory")
	}
	var missing []string
	for _, s := range j.RequiredSkills {
		if !slices.Contains(t.Skills, s) {
			missing = append(missing, s)
		}
	}
	if len(missing) > 0 {
		add(CodeDspSkill, "technician lacks skill %s", strings.Join(missing, ", "))
	}
	if j.WindowStart != nil && j.WindowEnd != nil && (start.Before(*j.WindowStart) || end.After(*j.WindowEnd)) {
		add(CodeDspWindow, "the visit %s to %s is outside the customer's window %s to %s",
			start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), j.WindowStart.UTC().Format(time.RFC3339), j.WindowEnd.UTC().Format(time.RFC3339))
	}

	loc, err := time.LoadLocation(t.TimeZone)
	if err != nil {
		loc = time.UTC
	}
	ls, le := start.In(loc), end.In(loc)
	overtime := 0
	if t.OvertimeAllowed {
		overtime = OvertimeMinutes
	}
	// Wall-clock minutes of the start's local day; an end on the next local day counts
	// past 1440, so a shift ending at midnight (1440) or overtime past it still fits.
	startMin, endMin := ls.Hour()*60+ls.Minute(), le.Hour()*60+le.Minute()
	if next := ls.AddDate(0, 0, 1); le.Year() == next.Year() && le.YearDay() == next.YearDay() {
		endMin += 1440
	} else if le.Year() != ls.Year() || le.YearDay() != ls.YearDay() {
		endMin = 1 << 30
	}
	if !slices.Contains(t.ShiftDays, isoWeekday(ls)) || startMin < t.ShiftStart || endMin > t.ShiftEnd+overtime {
		add(CodeDspShift, "the visit is outside the technician's shift (%s to %s local, days %v)", clockOf(t.ShiftStart), clockOf(t.ShiftEnd+overtime), t.ShiftDays)
	}

	buffer := time.Duration(BufferMinutes) * time.Minute
	load := j.DurationMinutes + BufferMinutes
	for _, o := range others {
		if start.Before(o.End.Add(buffer)) && o.Start.Before(end.Add(buffer)) {
			add(CodeDspOverlap, "the visit overlaps another booking of the technician (%s to %s), which needs a %d minute buffer",
				o.Start.UTC().Format(time.RFC3339), o.End.UTC().Format(time.RFC3339), BufferMinutes)
			break
		}
	}
	for _, o := range others {
		if os := o.Start.In(loc); os.Year() == ls.Year() && os.YearDay() == ls.YearDay() {
			load += int(o.End.Sub(o.Start)/time.Minute) + BufferMinutes
		}
	}
	if load > t.MaxDailyMinutes+overtime {
		add(CodeDspDailyLoad, "the day would hold %d booked minutes with buffers, over the technician's %d", load, t.MaxDailyMinutes+overtime)
	}
	return out
}

// Travel between two coordinates for the optimizer's matrix: the great-circle distance
// times RoadFactor, driven at TravelKmh, rounded up to whole minutes. A road routing
// engine replaces it later.
const (
	RoadFactor = 1.3
	TravelKmh  = 40.0

	earthRadiusKm = 6371.0
)

// TravelMinutes is the assumed driving time between two points.
func TravelMinutes(lat1, lon1, lat2, lon2 float64) int {
	rad := math.Pi / 180
	dLat, dLon := (lat2-lat1)*rad, (lon2-lon1)*rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*rad)*math.Cos(lat2*rad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	km := 2 * earthRadiusKm * math.Asin(math.Min(1, math.Sqrt(h)))
	return int(math.Ceil(km * RoadFactor / TravelKmh * 60))
}

func isoWeekday(t time.Time) int {
	if d := int(t.Weekday()); d != 0 {
		return d
	}
	return 7
}

func clockOf(minute int) string { return fmt.Sprintf("%02d:%02d", minute/60, minute%60) }

// DayBounds returns the instants a local calendar day (YYYY-MM-DD) starts and ends in a
// time zone, DST-safe: the day is the half-open interval [start, end).
func DayBounds(date, timeZone string) (time.Time, time.Time, error) {
	loc, err := time.LoadLocation(timeZone)
	if err != nil || timeZone == "" || timeZone == "Local" {
		return time.Time{}, time.Time{}, fmt.Errorf("unknown time zone %q", timeZone)
	}
	d, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	return d, time.Date(d.Year(), d.Month(), d.Day()+1, 0, 0, 0, 0, loc), nil
}
