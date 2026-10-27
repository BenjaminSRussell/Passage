package domain

import (
	"testing"
	"time"
	_ "time/tzdata" // zone rules are embedded so tests do not depend on the host

	"pgregory.net/rapid"
)

func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestCalendar_AddSub(t *testing.T) {
	loc := chicago(t)
	cal := MondayToFriday(loc, 8*60, 18*60)
	at := func(y int, m time.Month, d, h, min int) time.Time { return time.Date(y, m, d, h, min, 0, 0, loc) }
	tests := []struct {
		name  string
		start time.Time
		add   int
		want  time.Time
	}{
		{"inside a workday", at(2026, 10, 23, 9, 0), 60, at(2026, 10, 23, 10, 0)},
		{"crosses close into Monday", at(2026, 10, 23, 16, 0), 480, at(2026, 10, 26, 14, 0)},
		{"zero minutes from closed time moves to next open", at(2026, 10, 23, 18, 0), 0, at(2026, 10, 26, 8, 0)},
		{"weekend start moves to Monday open", at(2026, 10, 24, 10, 0), 60, at(2026, 10, 26, 9, 0)},
		{"before open starts at open", at(2026, 10, 26, 6, 0), 30, at(2026, 10, 26, 8, 30)},
		{"across spring DST start (Sat 2026-03-07 to Mon 03-09)", at(2026, 3, 6, 16, 0), 480, at(2026, 3, 9, 14, 0)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := cal.Add(tc.start, tc.add)
			if !got.Equal(tc.want) {
				t.Fatalf("Add = %s, want %s", got, tc.want)
			}
		})
	}
	if got := cal.Sub(at(2026, 10, 26, 14, 0), 480); !got.Equal(at(2026, 10, 23, 16, 0)) {
		t.Fatalf("Sub = %s, want Fri 16:00", got)
	}
	if got := cal.Between(at(2026, 10, 23, 16, 0), at(2026, 10, 26, 14, 0)); got != 480 {
		t.Fatalf("Between = %d, want 480", got)
	}
}

func TestCalendar_AllDayIsWallClock(t *testing.T) {
	loc := chicago(t)
	cal := Calendar{Loc: loc, AllDay: true}
	start := time.Date(2026, 10, 24, 10, 0, 0, 0, loc)
	if got := cal.Add(start, 60); !got.Equal(start.Add(time.Hour)) {
		t.Fatalf("24x7 Add = %s", got)
	}
	if got := cal.Between(start, start.Add(90*time.Minute)); got != 90 {
		t.Fatalf("24x7 Between = %d", got)
	}
}

func TestCalendar_Validate(t *testing.T) {
	loc := chicago(t)
	tests := []struct {
		name string
		cal  Calendar
		want bool
	}{
		{"monday to friday", MondayToFriday(loc, 480, 1080), true},
		{"24x7", Calendar{Loc: loc, AllDay: true}, true},
		{"no location", Calendar{OpenMinute: 480, CloseMinute: 1080, Workdays: []time.Weekday{time.Monday}}, false},
		{"no workdays", Calendar{Loc: loc, OpenMinute: 480, CloseMinute: 1080}, false},
		{"close before open", MondayToFriday(loc, 1080, 480), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.cal.Validate(); got != tc.want {
				t.Fatalf("Validate = %v, want %v", got, tc.want)
			}
		})
	}
}

// Property: Sub undoes Add from any instant, and Between measures what Add spans.
func TestCalendar_PropertyInverse(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	cal := MondayToFriday(loc, 8*60, 18*60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rapid.Check(t, func(r *rapid.T) {
		start := base.Add(time.Duration(rapid.Int64Range(0, 2*365*86400).Draw(r, "seconds")) * time.Second)
		n := rapid.IntRange(0, 5000).Draw(r, "minutes")
		anchor := cal.Add(start, 0)
		end := cal.Add(start, n)
		if got := cal.Sub(end, n); !got.Equal(anchor) {
			r.Fatalf("Sub(Add(%s,%d)) = %s, want %s", start, n, got, anchor)
		}
		if got := cal.Between(anchor, end); got != n {
			r.Fatalf("Between(%s, Add) = %d, want %d", anchor, got, n)
		}
	})
}

// Property: Add is monotone in minutes and never returns a non-business instant
// for a positive count.
func TestCalendar_PropertyMonotone(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Fatal(err)
	}
	cal := MondayToFriday(loc, 8*60, 18*60)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rapid.Check(t, func(r *rapid.T) {
		start := base.Add(time.Duration(rapid.Int64Range(0, 365*86400).Draw(r, "seconds")) * time.Second)
		a := rapid.IntRange(0, 2000).Draw(r, "a")
		b := a + rapid.IntRange(0, 2000).Draw(r, "delta")
		if cal.Add(start, b).Before(cal.Add(start, a)) {
			r.Fatalf("Add not monotone: %d then %d", a, b)
		}
		if got := cal.Add(start, a); cal.Between(start, got) != a {
			r.Fatalf("Between disagrees with Add for %d minutes", a)
		}
	})
}

// BR-45.2: holidays carry no business time, so a clock that would run into a
// holiday continues on the next workday. A 24x7 profile ignores holidays.
func TestCalendar_HolidaysAreSkipped(t *testing.T) {
	loc := chicago(t)
	cal, err := MondayToFriday(loc, 8*60, 18*60).WithHolidays("2026-10-26")
	if err != nil {
		t.Fatal(err)
	}
	at := func(m time.Month, d, h int) time.Time { return time.Date(2026, m, d, h, 0, 0, 0, loc) }
	if got, want := cal.Add(at(10, 23, 16), 480), at(10, 27, 14); !got.Equal(want) {
		t.Fatalf("Add over a Monday holiday = %s, want Tue 14:00", got)
	}
	if got := cal.Between(at(10, 26, 8), at(10, 26, 18)); got != 0 {
		t.Fatalf("Between on a holiday = %d, want 0", got)
	}
	if got, want := cal.Sub(at(10, 27, 9), 120), at(10, 23, 17); !got.Equal(want) {
		t.Fatalf("Sub back over the holiday = %s, want Fri 17:00", got)
	}
	allDay, err := Calendar{Loc: loc, AllDay: true}.WithHolidays("2026-10-26")
	if err != nil {
		t.Fatal(err)
	}
	if got := allDay.Between(at(10, 26, 8), at(10, 26, 18)); got != 600 {
		t.Fatalf("24x7 Between on a holiday = %d, want 600", got)
	}
	if _, err := cal.WithHolidays("2026-13-01"); err == nil {
		t.Fatal("a malformed holiday date must be rejected")
	}
}

// Property: the inverse laws hold with holidays in the calendar too.
func TestCalendar_PropertyInverseWithHolidays(t *testing.T) {
	loc := chicago(t)
	cal, err := MondayToFriday(loc, 8*60, 18*60).WithHolidays("2026-01-01", "2026-05-25", "2026-07-03", "2026-09-07", "2026-11-26", "2026-12-25")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	rapid.Check(t, func(r *rapid.T) {
		start := base.Add(time.Duration(rapid.Int64Range(0, 365*86400).Draw(r, "seconds")) * time.Second)
		n := rapid.IntRange(0, 5000).Draw(r, "minutes")
		anchor := cal.Add(start, 0)
		end := cal.Add(start, n)
		if got := cal.Sub(end, n); !got.Equal(anchor) {
			r.Fatalf("Sub(Add(%s,%d)) = %s, want %s", start, n, got, anchor)
		}
		if got := cal.Between(anchor, end); got != n {
			r.Fatalf("Between(%s, Add) = %d, want %d", anchor, got, n)
		}
	})
}
