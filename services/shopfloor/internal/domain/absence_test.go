package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func absenceCalc() Calculation {
	return Calculation{Profile: Profile{RoundingMinutes: 1}, Location: utc, Rules: DefaultRules(), Rate: dec("20"), Currency: "USD"}
}

func absence(id string, start time.Time, hours string, paid bool, mult string) Absence {
	h := dec(hours)
	return Absence{ID: id, Start: start, Stop: start.Add(time.Duration(h.Mul(dec("3600")).IntPart()) * time.Second), Hours: h, Paid: paid, Multiplier: dec(mult)}
}

// A paid absence pays hours x rate x multiplier; an unpaid one keeps the hours with amount 0.
// Neither is part of the day's worked hours.
func TestAbsence_PaidAndUnpaidLines(t *testing.T) {
	tue := time.Date(2026, 10, 20, 8, 0, 0, 0, utc)
	days := AddAbsences(nil, []Absence{absence("a", tue, "8", true, "1"), absence("b", tue.AddDate(0, 0, 1), "4.5", false, "0"), absence("c", tue.AddDate(0, 0, 2), "2", true, "1.25")}, utc)
	out, err := absenceCalc().Run(days)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 || !out[0].Hours.IsZero() {
		t.Fatalf("days = %+v", out)
	}
	paid := out[0].Lines[0]
	if paid.Type != TypeAbsencePaid || paid.Hours.String() != "8" || paid.Amount.String() != "160" || paid.Sources[0] != "a" {
		t.Fatalf("paid = %+v", paid)
	}
	unpaid := out[1].Lines[0]
	if unpaid.Type != TypeAbsenceUnpaid || unpaid.Hours.String() != "4.5" || !unpaid.Amount.IsZero() || !unpaid.Multiplier.IsZero() {
		t.Fatalf("unpaid = %+v", unpaid)
	}
	if got := out[2].Lines[0]; got.Amount.String() != "50" || got.Multiplier.String() != "1.25" {
		t.Fatalf("multiplier line = %+v", got)
	}
}

// Absence hours join the day of the clocked time without moving the overtime threshold:
// a 9 h shift is 8 h base plus 1 h overtime however much absence is booked the same day.
func TestAbsence_DoesNotCountTowardOvertime(t *testing.T) {
	in := time.Date(2026, 10, 20, 7, 0, 0, 0, utc)
	days := BuildDays([]Punch{{ID: "i", Kind: KindClockIn, At: in}, {ID: "o", Kind: KindClockOut, At: in.Add(9 * time.Hour)}}, utc, Profile{RoundingMinutes: 1})
	days = AddAbsences(days, []Absence{absence("a", in.Add(10*time.Hour), "4", true, "1")}, utc)
	out, err := absenceCalc().Run(days)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Hours.String() != "9" {
		t.Fatalf("day = %+v", out)
	}
	if lineHours(out[0].Lines, TypeBase) != "8.00" || lineHours(out[0].Lines, TypeOvertime15) != "1.00" || lineHours(out[0].Lines, TypeAbsencePaid) != "4.00" {
		t.Fatalf("lines = %+v", out[0].Lines)
	}
}

// Worked and absence hours together cannot exceed 24.00 h in a day (SF-0007).
func TestAbsence_DayCannotExceed24Hours(t *testing.T) {
	in := time.Date(2026, 10, 20, 0, 0, 0, 0, utc)
	days := BuildDays([]Punch{{ID: "i", Kind: KindClockIn, At: in}, {ID: "o", Kind: KindClockOut, At: in.Add(20 * time.Hour)}}, utc, Profile{RoundingMinutes: 1})
	days = AddAbsences(days, []Absence{absence("a", in.Add(21*time.Hour), "4.5", true, "1")}, utc)
	_, err := absenceCalc().Run(days)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeHoursExceed {
		t.Fatalf("err = %v, want SF-0007", err)
	}
}

// An absence that runs past midnight is paid on the day it starts, like a worked segment
// (a sick night shift 22:00 to 06:00 stays on the shift's day); the next day gets nothing.
func TestAbsence_CrossMidnightStaysOnStartDay(t *testing.T) {
	start := time.Date(2026, 10, 20, 22, 0, 0, 0, utc)
	out, err := absenceCalc().Run(AddAbsences(nil, []Absence{absence("a", start, "8", true, "1")}, utc))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Date.Format(dateLayout) != "2026-10-20" || lineHours(out[0].Lines, TypeAbsencePaid) != "8.00" {
		t.Fatalf("days = %+v", out)
	}
}

// An absence that shares time with clocked time (also a night shift from the day before) or
// with another absence flags its day, so the hours cannot be approved and paid twice.
func TestAbsence_OverlapFlagsTheDay(t *testing.T) {
	in := time.Date(2026, 10, 20, 7, 0, 0, 0, utc)
	worked := BuildDays([]Punch{{ID: "i", Kind: KindClockIn, At: in}, {ID: "o", Kind: KindClockOut, At: in.Add(8 * time.Hour)}}, utc, Profile{RoundingMinutes: 1})
	night := BuildDays([]Punch{{ID: "i", Kind: KindClockIn, At: in.Add(15 * time.Hour)}, {ID: "o", Kind: KindClockOut, At: in.Add(23 * time.Hour)}}, utc, Profile{RoundingMinutes: 1})
	cases := []struct {
		name     string
		days     []Day
		absences []Absence
		flagged  string
	}{
		{"inside the shift", worked, []Absence{absence("a", in.Add(2*time.Hour), "2", true, "1")}, "2026-10-20"},
		{"after the shift", worked, []Absence{absence("a", in.Add(8*time.Hour), "2", true, "1")}, ""},
		{"next morning under last night's shift", night, []Absence{absence("a", in.Add(17*time.Hour+30*time.Minute), "4", true, "1")}, "2026-10-21"},
		{"two absences", nil, []Absence{absence("a", in, "4", true, "1"), absence("b", in.Add(3*time.Hour), "4", false, "0")}, "2026-10-20"},
		{"a zero-length absence inside another", nil, []Absence{absence("a", in, "4", true, "1"), absence("b", in.Add(time.Hour), "0", false, "0")}, ""},
	}
	for _, c := range cases {
		got := ""
		for _, d := range AddAbsences(c.days, c.absences, utc) {
			for _, f := range d.Flags {
				if f == FlagAbsenceOverlap {
					got = d.DateString()
				}
			}
		}
		if got != c.flagged {
			t.Errorf("%s: flagged day %q, want %q", c.name, got, c.flagged)
		}
	}
}

// An absence belongs to the local day it starts on, not the UTC day.
func TestAbsence_LocalDay(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database")
	}
	start := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC) // 23:00 on the 20th in New York
	days := AddAbsences(nil, []Absence{absence("a", start, "1", true, "1")}, ny)
	if len(days) != 1 || days[0].DateString() != "2026-10-20" {
		t.Fatalf("days = %+v", days)
	}
}

func TestAbsenceMultiplier(t *testing.T) {
	d := func(s string) *decimal.Decimal { v := dec(s); return &v }
	cases := []struct {
		paid bool
		in   *decimal.Decimal
		want string
		bad  bool
	}{
		{true, nil, "1", false}, {true, d("1.5"), "1.5", false}, {true, d("5"), "5", false},
		{false, nil, "0", false}, {false, d("0"), "0", false}, {false, d("3"), "", true},
		{true, d("0"), "", true}, {true, d("5.0001"), "", true}, {true, d("1.00001"), "", true}, {true, d("-1"), "", true},
	}
	for _, c := range cases {
		got, msg := AbsenceMultiplier(c.paid, c.in)
		if (msg != "") != c.bad || (!c.bad && got.String() != c.want) {
			t.Errorf("AbsenceMultiplier(%v, %v) = %s, %q", c.paid, c.in, got, msg)
		}
	}
}
