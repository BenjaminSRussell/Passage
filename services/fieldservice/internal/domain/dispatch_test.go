package domain

import (
	"reflect"
	"testing"
	"time"
	_ "time/tzdata"
)

func tech() Technician {
	return Technician{ID: "t1", LegalEntityID: "le", TerritoryIDs: []string{"north", "south"}, Skills: []string{"HVAC", "ELECTRICAL"},
		TimeZone: "America/Chicago", ShiftDays: []int{1, 2, 3, 4, 5}, ShiftStart: 480, ShiftEnd: 1020, MaxDailyMinutes: 480, Active: true}
}

func job() Job {
	return Job{ID: "j1", LegalEntityID: "le", TerritoryID: "north", RequiredSkills: []string{"HVAC"}, DurationMinutes: 120}
}

func codes(vs []Violation) []string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Code)
	}
	return out
}

// Monday 2026-10-26 in Chicago.
func monday(t *testing.T, hour, minute int) time.Time {
	return time.Date(2026, 10, 26, hour, minute, 0, 0, chicago(t))
}

func TestCheckAssignment_FeasibleVisitHasNoViolation(t *testing.T) {
	if vs := CheckAssignment(tech(), job(), monday(t, 9, 0), nil); len(vs) != 0 {
		t.Fatalf("violations %v", vs)
	}
}

func TestCheckAssignment_EachHardConstraint(t *testing.T) {
	win := func(h1, h2 int) (*time.Time, *time.Time) {
		a, b := monday(t, h1, 0), monday(t, h2, 0)
		return &a, &b
	}
	ws, we := win(13, 14)
	cases := []struct {
		name   string
		mutate func(*Technician, *Job)
		start  time.Time
		others []Slot
		want   []string
	}{
		{"missing skill", func(_ *Technician, j *Job) { j.RequiredSkills = []string{"HVAC", "PLUMBING", "CRANE"} }, monday(t, 9, 0), nil, []string{CodeDspSkill}},
		{"inactive", func(x *Technician, _ *Job) { x.Active = false }, monday(t, 9, 0), nil, []string{CodeDspEligible}},
		{"other legal entity", func(x *Technician, _ *Job) { x.LegalEntityID = "other" }, monday(t, 9, 0), nil, []string{CodeDspEligible}},
		{"territory not covered", func(_ *Technician, j *Job) { j.TerritoryID = "east" }, monday(t, 9, 0), nil, []string{CodeDspEligible}},
		{"before the shift", nil, monday(t, 7, 0), nil, []string{CodeDspShift}},
		{"ends after the shift", nil, monday(t, 16, 0), nil, []string{CodeDspShift}},
		{"weekend", nil, monday(t, 9, 0).AddDate(0, 0, 5), nil, []string{CodeDspShift}},
		{"outside the customer window", func(_ *Technician, j *Job) { j.WindowStart, j.WindowEnd = ws, we }, monday(t, 9, 0), nil, []string{CodeDspWindow}},
		{"overlap", nil, monday(t, 9, 0), []Slot{{monday(t, 10, 0), monday(t, 11, 0)}}, []string{CodeDspOverlap}},
		{"inside the buffer after another job", nil, monday(t, 11, 3), []Slot{{monday(t, 9, 0), monday(t, 11, 0)}}, []string{CodeDspOverlap}},
		{"daily load", func(x *Technician, _ *Job) { x.MaxDailyMinutes = 200 }, monday(t, 14, 0), []Slot{{monday(t, 9, 0), monday(t, 11, 0)}}, []string{CodeDspDailyLoad}},
		{"several at once", func(x *Technician, j *Job) { x.Active = false; j.RequiredSkills = []string{"WELDING"} }, monday(t, 6, 0), nil,
			[]string{CodeDspEligible, CodeDspSkill, CodeDspShift}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tc, jc := tech(), job()
			if c.mutate != nil {
				c.mutate(&tc, &jc)
			}
			if got := codes(CheckAssignment(tc, jc, c.start, c.others)); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("violations %v, want %v", got, c.want)
			}
		})
	}
}

// A visit that starts exactly one buffer after another ends is allowed, as is one that
// ends exactly one buffer before the next starts.
func TestCheckAssignment_BufferBoundaryIsFeasible(t *testing.T) {
	others := []Slot{{monday(t, 9, 0), monday(t, 10, 0)}, {monday(t, 12, 10), monday(t, 13, 0)}}
	if vs := CheckAssignment(tech(), job(), monday(t, 10, 5), others); len(vs) != 0 {
		t.Fatalf("violations %v", vs)
	}
}

// Overtime widens the shift end and the daily maximum by 120 minutes, and no more.
func TestCheckAssignment_Overtime(t *testing.T) {
	x := tech()
	late := monday(t, 16, 0) // ends 18:00, an hour after the 17:00 shift end
	if got := codes(CheckAssignment(x, job(), late, nil)); !reflect.DeepEqual(got, []string{CodeDspShift}) {
		t.Fatalf("without overtime: %v", got)
	}
	x.OvertimeAllowed = true
	if vs := CheckAssignment(x, job(), late, nil); len(vs) != 0 {
		t.Fatalf("with overtime: %v", vs)
	}
	if got := codes(CheckAssignment(x, job(), monday(t, 18, 0), nil)); !reflect.DeepEqual(got, []string{CodeDspShift}) {
		t.Fatalf("overtime ends at 19:00, a visit to 20:00 must fail: %v", got)
	}
}

// The shift is read in the technician's zone: 14:00 UTC is 09:00 in Chicago (CDT) and
// inside the shift, while 02:00 UTC is 21:00 the day before and outside it.
func TestCheckAssignment_ShiftIsInTheTechniciansZone(t *testing.T) {
	if vs := CheckAssignment(tech(), job(), time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC), nil); len(vs) != 0 {
		t.Fatalf("09:00 local: %v", vs)
	}
	if got := codes(CheckAssignment(tech(), job(), time.Date(2026, 10, 27, 2, 0, 0, 0, time.UTC), nil)); !reflect.DeepEqual(got, []string{CodeDspShift}) {
		t.Fatalf("21:00 local: %v", got)
	}
}

// Bookings of another local day do not count toward the daily load.
func TestCheckAssignment_DailyLoadIsPerLocalDay(t *testing.T) {
	x := tech()
	x.MaxDailyMinutes = 200
	yesterday := []Slot{{monday(t, 9, 0).AddDate(0, 0, -1), monday(t, 17, 0).AddDate(0, 0, -1)}}
	if vs := CheckAssignment(x, job(), monday(t, 9, 0), yesterday); len(vs) != 0 {
		t.Fatalf("violations %v", vs)
	}
}

func TestDayBounds_DSTDay(t *testing.T) {
	// Chicago leaves daylight time on 2026-11-01: the local day is 25 hours long.
	from, to, err := DayBounds("2026-11-01", "America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	if got := to.Sub(from); got != 25*time.Hour {
		t.Fatalf("day length %s, want 25h", got)
	}
	if _, _, err := DayBounds("2026-11-01", "Nowhere/Land"); err == nil {
		t.Fatal("unknown zone accepted")
	}
	if _, _, err := DayBounds("2026-13-01", "UTC"); err == nil {
		t.Fatal("bad date accepted")
	}
}

// A shift that ends at midnight takes a visit that ends at 24:00, and overtime may run
// past midnight; a visit that would end the day after next never fits.
func TestCheckAssignment_ShiftEndingAtMidnight(t *testing.T) {
	x := tech()
	x.ShiftStart, x.ShiftEnd, x.MaxDailyMinutes = 960, 1440, 600
	if vs := CheckAssignment(x, job(), monday(t, 22, 0), nil); len(vs) != 0 {
		t.Fatalf("22:00 to 24:00 inside a 16:00 to 24:00 shift: %v", vs)
	}
	if got := codes(CheckAssignment(x, job(), monday(t, 23, 0), nil)); !reflect.DeepEqual(got, []string{CodeDspShift}) {
		t.Fatalf("23:00 to 01:00 without overtime: %v", got)
	}
	x.OvertimeAllowed = true
	if vs := CheckAssignment(x, job(), monday(t, 23, 0), nil); len(vs) != 0 {
		t.Fatalf("23:00 to 01:00 with overtime: %v", vs)
	}
	long := job()
	long.DurationMinutes = 1500
	if got := codes(CheckAssignment(x, long, monday(t, 23, 0), nil)); !reflect.DeepEqual(got, []string{CodeDspShift, CodeDspDailyLoad}) {
		t.Fatalf("a visit into the day after next: %v", got)
	}
}
