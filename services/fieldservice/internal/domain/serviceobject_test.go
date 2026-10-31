package domain

import (
	"testing"
	"time"
)

// AC-45.8 (domain half): warranty starts on the ship day for the SHIP trigger.
func TestWarrantyStart_TriggersPickTheRightDay(t *testing.T) {
	ship := time.Date(2026, 10, 23, 15, 30, 0, 0, time.UTC)
	install := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	reg := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		trigger   string
		shipped   *time.Time
		installed *time.Time
		want      time.Time
		ok        bool
	}{
		{"ship uses shipment day", StartShip, &ship, nil, time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC), true},
		{"ship without a shipment date", StartShip, nil, nil, time.Time{}, false},
		{"install uses installation day", StartInstall, &ship, &install, time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC), true},
		{"install without installation", StartInstall, &ship, nil, time.Time{}, false},
		{"registration uses registration day", StartRegistration, nil, nil, time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), true},
		{"unknown trigger", "OTHER", &ship, nil, time.Time{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := WarrantyStart(tc.trigger, tc.shipped, tc.installed, reg)
			if ok != tc.ok || !got.Equal(tc.want) {
				t.Fatalf("WarrantyStart = %s, %v; want %s, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestWarrantyTerm_Valid(t *testing.T) {
	tests := []struct {
		name string
		term WarrantyTerm
		want bool
	}{
		{"ship 24 months", WarrantyTerm{StartTrigger: StartShip, Months: 24}, true},
		{"install 0 months", WarrantyTerm{StartTrigger: StartInstall, Months: 0}, true},
		{"unknown trigger", WarrantyTerm{StartTrigger: "SOLD", Months: 12}, false},
		{"negative months", WarrantyTerm{StartTrigger: StartShip, Months: -1}, false},
		{"over 20 years", WarrantyTerm{StartTrigger: StartShip, Months: MaxWarrantyMonths + 1}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.term.Valid(); got != tc.want {
				t.Fatalf("Valid() = %v, want %v", got, tc.want)
			}
		})
	}
}

// BR-45.4 (warranty half): coverage is active from the start day for the term,
// ends on the same day of the month the term runs out, carries the term's
// flags, and covers nothing before the warranty has started.
func TestWarrantyCoverOn_ActiveWithinTermOnly(t *testing.T) {
	start := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	term := WarrantyTerm{StartTrigger: StartShip, Months: 12, PartsCovered: true, LaborCovered: false}
	tests := []struct {
		name  string
		start *time.Time
		on    time.Time
		want  bool
	}{
		{"before start", &start, time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC), false},
		{"on start day", &start, time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC), true},
		{"mid term", &start, time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC), true},
		{"last covered day", &start, time.Date(2027, 10, 22, 23, 0, 0, 0, time.UTC), true},
		{"term ran out", &start, time.Date(2027, 10, 23, 0, 0, 0, 0, time.UTC), false},
		{"not started", nil, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cov := WarrantyCoverOn(term, tc.start, tc.on)
			if cov.Active != tc.want {
				t.Fatalf("Active = %v, want %v", cov.Active, tc.want)
			}
			if tc.start != nil && (!cov.Parts || cov.Labor) {
				t.Fatalf("flags parts=%v labor=%v, want parts only", cov.Parts, cov.Labor)
			}
		})
	}
}

// A zero-month term is no warranty: it never ends because it never covers.
func TestWarrantyEnd(t *testing.T) {
	start := time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC)
	end := WarrantyEnd(WarrantyTerm{StartTrigger: StartShip, Months: 24}, &start)
	if end == nil || !end.Equal(time.Date(2028, 1, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("end = %v, want 2028-01-31", end)
	}
	if WarrantyEnd(WarrantyTerm{StartTrigger: StartShip}, &start) != nil {
		t.Fatal("a zero-month term has no end day")
	}
	if WarrantyEnd(WarrantyTerm{StartTrigger: StartInstall, Months: 12}, nil) != nil {
		t.Fatal("a warranty that has not started has no end day")
	}
}

// Regression: a term that starts on a day the end month does not have ends on
// that month's last day (31 Aug + 6 months = 28 Feb, as Postgres
// date + interval and the repo's AddMonthsClamped compute it). time.AddDate
// overflowed into the next month (3 Mar), so the warranty covered work done
// on 28 Feb, 1 Mar and 2 Mar after its term.
func TestWarrantyEnd_ClampsToTheEndOfAShortMonth(t *testing.T) {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	tests := []struct {
		start  time.Time
		months int
		end    time.Time
	}{
		{day(2025, 8, 31), 6, day(2026, 2, 28)},
		{day(2027, 8, 31), 6, day(2028, 2, 29)}, // leap year
		{day(2026, 1, 31), 1, day(2026, 2, 28)},
		{day(2026, 3, 31), 1, day(2026, 4, 30)},
		{day(2028, 2, 29), 12, day(2029, 2, 28)},
		{day(2026, 1, 30), 13, day(2027, 2, 28)},
		{day(2026, 10, 23), 12, day(2027, 10, 23)}, // no clamping needed
	}
	for _, tc := range tests {
		term := WarrantyTerm{StartTrigger: StartShip, Months: tc.months, PartsCovered: true}
		start := tc.start
		if end := WarrantyEnd(term, &start); end == nil || !end.Equal(tc.end) {
			t.Errorf("%s + %d months: end = %v, want %s", tc.start.Format(DateLayout), tc.months, end, tc.end.Format(DateLayout))
		}
		lastCovered := tc.end.AddDate(0, 0, -1)
		if !WarrantyCoverOn(term, &start, lastCovered).Active {
			t.Errorf("%s + %d months: %s must still be covered", tc.start.Format(DateLayout), tc.months, lastCovered.Format(DateLayout))
		}
		if WarrantyCoverOn(term, &start, tc.end).Active || WarrantyActive(start, tc.months, tc.end, false) {
			t.Errorf("%s + %d months: %s is past the term", tc.start.Format(DateLayout), tc.months, tc.end.Format(DateLayout))
		}
	}
}
