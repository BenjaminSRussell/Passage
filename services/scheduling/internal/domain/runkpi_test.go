package domain

import (
	"testing"
	"time"
)

func TestRunUtilization_CountsOnlyOpenTimeInSpanAndRunSegments(t *testing.T) {
	r := Resource{ID: "r1", Finite: true, Location: time.UTC, Shifts: []Shift{
		{Weekday: time.Monday, StartMinute: 8 * 60, EndMinute: 16 * 60},
		{Weekday: time.Tuesday, StartMinute: 8 * 60, EndMinute: 16 * 60},
	}}
	mon := time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC) // a Monday
	segs := []Busy{
		{ResourceID: "r1", JobID: "a", Start: mon.Add(8 * time.Hour), End: mon.Add(10 * time.Hour)},
		{ResourceID: "r1", JobID: "a", Start: mon.Add(12 * time.Hour), End: mon.Add(13 * time.Hour)},
		{ResourceID: "r1", JobID: "b", Start: mon.Add(24 * time.Hour).Add(8 * time.Hour), End: mon.Add(24 * time.Hour).Add(9 * time.Hour)},
	}
	k := RunUtilization(r, segs, mon.Add(8*time.Hour), mon.Add(24*time.Hour).Add(9*time.Hour))
	// Open: Monday 8-16 (480) + Tuesday 8-9 (60) = 540. Booked 120 + 60 + 60 = 240.
	if k.OpenMinutes != 540 || k.BookedMinutes != 240 || k.Jobs != 2 {
		t.Fatalf("kpi = %+v", k)
	}
	if k.UtilizationPct < 44.4 || k.UtilizationPct > 44.5 {
		t.Fatalf("pct = %v", k.UtilizationPct)
	}
}

func TestRunUtilization_NoOpenTimeIsZeroPercent(t *testing.T) {
	r := Resource{ID: "r1", Location: time.UTC, Shifts: []Shift{{Weekday: time.Sunday, StartMinute: 0, EndMinute: 60}}}
	mon := time.Date(2027, 1, 4, 8, 0, 0, 0, time.UTC)
	k := RunUtilization(r, []Busy{{JobID: "x", Start: mon, End: mon.Add(time.Hour)}}, mon, mon.Add(2*time.Hour))
	if k.OpenMinutes != 0 || k.UtilizationPct != 0 || k.BookedMinutes != 60 {
		t.Fatalf("kpi = %+v", k)
	}
}

// The open minutes come from the resource's own calendar in its time zone,
// not from UTC days: an 08:00-16:00 Chicago shift is 14:00-22:00 UTC in
// January, and the US fall-back day has 25 open hours (BR-23.7).
func TestRunUtilization_ReadsTheCalendarInTheResourceTimeZone(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skip(err)
	}
	r := Resource{ID: "r1", Location: chicago, Shifts: []Shift{
		{Weekday: time.Monday, StartMinute: 8 * 60, EndMinute: 16 * 60},
		{Weekday: time.Tuesday, StartMinute: 8 * 60, EndMinute: 16 * 60},
	}}
	start := time.Date(2027, 1, 4, 20, 0, 0, 0, time.UTC) // Monday 14:00 in Chicago
	end := time.Date(2027, 1, 5, 15, 0, 0, 0, time.UTC)   // Tuesday 09:00 in Chicago
	k := RunUtilization(r, []Busy{{JobID: "a", Start: start, End: start.Add(90 * time.Minute)}}, start, end)
	// Monday 14:00-16:00 local (120) and Tuesday 08:00-09:00 local (60); a UTC
	// calendar would answer Tuesday 08:00-15:00 UTC (420).
	if k.OpenMinutes != 180 || k.BookedMinutes != 90 || k.UtilizationPct != 50 {
		t.Fatalf("kpi = %+v, want 180 open minutes of the Chicago calendar", k)
	}

	allDay := Resource{ID: "r2", Location: chicago, Shifts: []Shift{{Weekday: time.Sunday, StartMinute: 0, EndMinute: 1440}}}
	dayStart := time.Date(2026, 11, 1, 0, 0, 0, 0, chicago)
	dayEnd := time.Date(2026, 11, 2, 0, 0, 0, 0, chicago)
	if k := RunUtilization(allDay, nil, dayStart, dayEnd); k.OpenMinutes != 25*60 {
		t.Fatalf("fall-back Sunday open minutes = %d, want 1500", k.OpenMinutes)
	}
}
