package domain

import (
	"testing"
	"time"
)

func TestDispatchWindow_UsesTheResourceLocalDayAndShift(t *testing.T) {
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	date := time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC)
	from, to := DispatchWindow(tokyo, date, 1, 6*60, 14*60)
	if !from.Equal(time.Date(2027, 1, 4, 21, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2027, 1, 5, 5, 0, 0, 0, time.UTC)) {
		t.Fatalf("window = %s .. %s", from, to)
	}
	// A night shift 22:00 to 06:00 passes an end beyond midnight and spans two days.
	from, to = DispatchWindow(nil, date, 1, 22*60, 30*60)
	if !from.Equal(time.Date(2027, 1, 5, 22, 0, 0, 0, time.UTC)) || !to.Equal(time.Date(2027, 1, 6, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("night window = %s .. %s", from, to)
	}
	from, to = DispatchWindow(nil, date, 3, 0, 1440)
	if to.Sub(from) != 72*time.Hour {
		t.Fatalf("three days = %s", to.Sub(from))
	}
}

func TestDispatchWindow_FollowsTheWallClockAcrossDaylightSaving(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database")
	}
	utc := func(mo time.Month, d, h, mi int) time.Time { return time.Date(2027, mo, d, h, mi, 0, 0, time.UTC) }
	day := func(mo time.Month, d int) time.Time { return time.Date(2027, mo, d, 0, 0, 0, 0, time.UTC) }
	cases := []struct {
		name         string
		date         time.Time
		start, end   int
		from, to     time.Time
		windowLength time.Duration
	}{
		// 2027-03-14 02:00 EST jumps to 03:00 EDT; 2027-11-07 02:00 EDT falls back to 01:00 EST.
		{"spring day", day(3, 14), 0, 1440, utc(3, 14, 5, 0), utc(3, 15, 4, 0), 23 * time.Hour},
		{"autumn day", day(11, 7), 0, 1440, utc(11, 7, 4, 0), utc(11, 8, 5, 0), 25 * time.Hour},
		{"spring night shift", day(3, 13), 22 * 60, 30 * 60, utc(3, 14, 3, 0), utc(3, 14, 10, 0), 7 * time.Hour},
		{"autumn night shift", day(11, 6), 22 * 60, 30 * 60, utc(11, 7, 2, 0), utc(11, 7, 11, 0), 9 * time.Hour},
		// 01:40 to 02:10 on the spring day: 02:10 never shows, the window ends at the change (03:00 EDT).
		{"end in the gap", day(3, 14), 100, 130, utc(3, 14, 6, 40), utc(3, 14, 7, 0), 20 * time.Minute},
		// 02:30 to 04:00: the shift starts when the clock jumps, not an hour early.
		{"start in the gap", day(3, 14), 150, 240, utc(3, 14, 7, 0), utc(3, 14, 8, 0), time.Hour},
		{"inside the gap", day(3, 14), 130, 150, utc(3, 14, 7, 0), utc(3, 14, 7, 0), 0},
		// 01:30 repeats on the autumn day; the window starts at its first occurrence.
		{"start in the overlap", day(11, 7), 90, 180, utc(11, 7, 5, 30), utc(11, 7, 8, 0), 150 * time.Minute},
	}
	for _, c := range cases {
		from, to := DispatchWindow(ny, c.date, 1, c.start, c.end)
		if !from.Equal(c.from) || !to.Equal(c.to) || to.Sub(from) != c.windowLength {
			t.Errorf("%s: window = %s .. %s (%s), want %s .. %s (%s)", c.name, from, to, to.Sub(from), c.from, c.to, c.windowLength)
		}
	}
	// A job running through the spring night counts the minutes that passed, not the wall-clock difference.
	from, to := DispatchWindow(ny, day(3, 13), 1, 22*60, 30*60)
	lines, total := DispatchLines([]DispatchRow{{JobID: "a", Start: utc(3, 14, 6, 0), End: utc(3, 14, 12, 0)}}, from, to)
	if lines[0].MinutesInWindow != 240 || total != 240 {
		t.Fatalf("minutes in window = %d total %d, want 240 (06:00Z to the 10:00Z shift end)", lines[0].MinutesInWindow, total)
	}
}

func TestDispatchLines_NumbersByStartAndClipsToTheWindow(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2027, 1, 5, h, m, 0, 0, time.UTC) }
	rows := []DispatchRow{
		{JobID: "b", OrderID: "o2", OperNumber: 10, Segment: 1, Role: "primary", Start: at(10, 0), End: at(12, 0), Finite: true},
		{JobID: "a", OrderID: "o1", OperNumber: 20, Segment: 2, Role: "primary", Start: at(7, 0), End: at(9, 30), SetupGroup: "RED", ChangeoverMins: 20, Firmed: true},
		{JobID: "a", OrderID: "o1", OperNumber: 20, Segment: 1, Role: "secondary", Start: at(7, 0), End: at(9, 30)},
	}
	lines, total := DispatchLines(rows, at(8, 0), at(11, 0))
	if len(lines) != 3 || lines[0].Segment != 1 || lines[1].Segment != 2 || lines[2].JobID != "b" {
		t.Fatalf("order = %+v", lines)
	}
	for i, l := range lines {
		if l.Sequence != i+1 {
			t.Fatalf("sequence %d on line %d", l.Sequence, i)
		}
	}
	if lines[1].MinutesInWindow != 90 || lines[2].MinutesInWindow != 60 || total != 240 {
		t.Fatalf("minutes = %d %d total %d, want 90, 60 and 240", lines[1].MinutesInWindow, lines[2].MinutesInWindow, total)
	}
	if lines[1].SetupGroup != "RED" || lines[1].ChangeoverMins != 20 || !lines[1].Firmed {
		t.Fatalf("line = %+v", lines[1])
	}
	if none, sum := DispatchLines(nil, at(0, 0), at(1, 0)); len(none) != 0 || sum != 0 || none == nil {
		t.Fatalf("empty = %+v %d", none, sum)
	}
}

// The resource calendar resolves shift ends in a DST gap or overlap the way the
// dispatch list window does, so a dispatch window and the open capacity agree.
func TestOpenIntervals_ShiftsInADaylightSavingGapMatchTheDispatchWindow(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database")
	}
	utc := func(mo time.Month, d, h, mi int) time.Time { return time.Date(2027, mo, d, h, mi, 0, 0, time.UTC) }
	cases := []struct {
		name       string
		day        time.Time
		start, end int
		want       []Interval
	}{
		{"start in the gap", utc(3, 14, 12, 0), 150, 240, []Interval{{utc(3, 14, 7, 0), utc(3, 14, 8, 0)}}},
		{"end in the gap", utc(3, 14, 12, 0), 100, 130, []Interval{{utc(3, 14, 6, 40), utc(3, 14, 7, 0)}}},
		{"wholly in the gap", utc(3, 14, 12, 0), 130, 150, nil},
		{"start in the overlap", utc(11, 7, 12, 0), 90, 180, []Interval{{utc(11, 7, 5, 30), utc(11, 7, 8, 0)}}},
	}
	for _, c := range cases {
		got := OpenIntervals(ny, []Shift{{Weekday: time.Sunday, StartMinute: c.start, EndMinute: c.end}}, c.day, 1)
		if len(got) != len(c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if !got[i].Start.Equal(c.want[i].Start) || !got[i].End.Equal(c.want[i].End) {
				t.Errorf("%s: %v, want %v", c.name, got, c.want)
			}
		}
		from, to := DispatchWindow(ny, time.Date(c.day.Year(), c.day.Month(), c.day.Day(), 0, 0, 0, 0, time.UTC), 1, c.start, c.end)
		if len(got) == 1 && (!got[0].Start.Equal(from) || !got[0].End.Equal(to)) {
			t.Errorf("%s: calendar %v differs from dispatch window %s..%s", c.name, got[0], from, to)
		}
	}
}

// An RRULE block window whose start falls in a spring-forward gap starts at the
// change, as a weekly window (OpenIntervals) does, not an hour earlier.
func TestRecurrenceWindows_ResolveADaylightSavingGapLikeTheCalendar(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database")
	}
	day := time.Date(2027, 3, 14, 0, 0, 0, 0, time.UTC)
	rc := Recurrence{Rule: "FREQ=DAILY;COUNT=1", StartDate: day, StartMinute: 150, EndMinute: 240}
	got, err := rc.Windows(ny, day.Add(-24*time.Hour), day.Add(48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	weekly := OpenIntervals(ny, []Shift{{Weekday: time.Sunday, StartMinute: 150, EndMinute: 240}}, day.Add(12*time.Hour), 1)
	if len(got) != 1 || len(weekly) != 1 || !got[0].Start.Equal(weekly[0].Start) || !got[0].End.Equal(weekly[0].End) {
		t.Fatalf("recurring %v, weekly %v", got, weekly)
	}
	// Wholly inside the gap: no window.
	rc.StartMinute, rc.EndMinute = 130, 150
	if got, _ = rc.Windows(ny, day.Add(-24*time.Hour), day.Add(48*time.Hour)); len(got) != 0 {
		t.Fatalf("window in the gap = %v", got)
	}
}
