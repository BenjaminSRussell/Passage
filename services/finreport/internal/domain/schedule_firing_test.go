package domain

import (
	"testing"
	"time"
)

func firingSchedule() Schedule {
	return Schedule{Code: "NIGHTLY", Cron: "0 2 1 * *", Timezone: "Europe/Berlin", FiscalYearStartMonth: 1}
}

func TestFiringPeriod_IsThePreviousCalendarMonthInTheScheduleZone(t *testing.T) {
	cases := []struct {
		name      string
		start     int
		fire      time.Time
		year, no  int
		calStarts int
	}{
		{"january statement is december", 1, time.Date(2026, 1, 1, 1, 0, 0, 0, time.UTC), 2025, 12, 2023},
		{"mid year", 1, time.Date(2026, 8, 1, 1, 0, 0, 0, time.UTC), 2026, 7, 2024},
		// 23:30 UTC on 31 July is already August in Berlin: the statement is July's, not June's.
		{"zone decides the month", 1, time.Date(2026, 7, 31, 23, 30, 0, 0, time.UTC), 2026, 7, 2024},
		{"april fiscal year, may statement is period 2", 4, time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC), 2027, 2, 2025},
		{"april fiscal year, march statement is period 12 of the year ending", 4, time.Date(2026, 4, 2, 0, 0, 0, 0, time.UTC), 2026, 12, 2024},
		{"april fiscal year, december statement", 4, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), 2026, 9, 2024},
	}
	for _, c := range cases {
		s := firingSchedule()
		s.FiscalYearStartMonth = c.start
		got, err := s.FiringPeriod(c.fire)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.Year != c.year || got.No != c.no {
			t.Errorf("%s: period %d-%d, want %d-%d", c.name, got.Year, got.No, c.year, c.no)
		}
		if got.Calendar[0].Year != c.calStarts || len(got.Calendar) != 39 {
			t.Errorf("%s: calendar starts %d with %d periods, want %d and 39", c.name, got.Calendar[0].Year, len(got.Calendar), c.calStarts)
		}
		cal, err := NewCalendar(got.Calendar)
		if err != nil {
			t.Fatalf("%s: calendar: %v", c.name, err)
		}
		if _, ok := cal.Index(got.Year, got.No); !ok {
			t.Errorf("%s: period %s is not in its own calendar", c.name, got.Label())
		}
	}
}

func TestFiringPeriod_RejectsAnUnknownZone(t *testing.T) {
	s := firingSchedule()
	s.Timezone = "Local"
	if _, err := s.FiringPeriod(time.Now()); err == nil {
		t.Fatal("Local must be refused")
	}
}

func TestDueFire(t *testing.T) {
	s := firingSchedule()
	s.Cron = "0 2 * * *"
	s.Timezone = "UTC"
	at := func(d, h int) time.Time { return time.Date(2026, 3, d, h, 0, 0, 0, time.UTC) }

	// Nothing is due before the first cron time after the baseline.
	if _, due, err := s.DueFire(at(10, 3), at(11, 1)); err != nil || due {
		t.Fatalf("not yet due: due=%v err=%v", due, err)
	}
	// The cron time at exactly now is due, and is the one after the baseline.
	got, due, err := s.DueFire(at(10, 3), at(11, 2))
	if err != nil || !due || !got.Equal(at(11, 2)) {
		t.Fatalf("due at now: %v %v %v", got, due, err)
	}
	// Several missed firings give the newest only.
	got, due, err = s.DueFire(at(1, 3), at(11, 5))
	if err != nil || !due || !got.Equal(at(11, 2)) {
		t.Fatalf("missed firings: %v %v %v", got, due, err)
	}
	// Baseline exactly on a cron time does not fire it again.
	if _, due, _ := s.DueFire(at(11, 2), at(11, 5)); due {
		t.Fatal("a fired cron time must not fire again")
	}
}

func TestDueFire_DoesNotMakeUpFiringsOlderThanTheLookback(t *testing.T) {
	s := firingSchedule()
	s.Cron = "0 2 1 * *" // monthly
	s.Timezone = "UTC"
	now := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	// The 1 April firing is 49 days old: skipped, and 1 May (19 days) is the due one.
	got, due, err := s.DueFire(time.Date(2026, 3, 1, 3, 0, 0, 0, time.UTC), now)
	if err != nil || !due || !got.Equal(time.Date(2026, 5, 1, 2, 0, 0, 0, time.UTC)) {
		t.Fatalf("got %v %v %v", got, due, err)
	}
	// With the job down for 40 days since a cron time that is itself out of window, nothing else is due.
	if _, due, _ := s.DueFire(time.Date(2026, 5, 1, 2, 0, 0, 0, time.UTC), now); due {
		t.Fatal("already fired")
	}
}

func TestScheduleValidate_FiscalYearStartMonth(t *testing.T) {
	s := validSchedule()
	s.FiscalYearStartMonth = 13
	found := false
	for _, i := range s.Validate() {
		found = found || i.Field == "fiscalYearStartMonth"
	}
	if !found {
		t.Fatal("13 must be refused")
	}
	s.FiscalYearStartMonth = 0
	if n := s.Normalize(); n.FiscalYearStartMonth != 1 {
		t.Fatalf("default %d", n.FiscalYearStartMonth)
	}
}
