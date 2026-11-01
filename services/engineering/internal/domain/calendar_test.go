package domain_test

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func day(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

func shift(start, end string) domain.Shift { return domain.Shift{Start: start, End: end} }

func shiftEff(start, end, eff string) domain.Shift {
	e := dec(eff)
	return domain.Shift{Start: start, End: end, EfficiencyPct: &e}
}

func weekdays(shifts ...domain.Shift) [7][]domain.Shift {
	var t [7][]domain.Shift
	for i := 0; i < 5; i++ {
		t[i] = shifts
	}
	return t
}

func TestGenerate_Fixture3Years(t *testing.T) {
	var fx struct {
		From               string
		To                 string
		Template           map[string][]domain.Shift
		ClosedDates        []string
		ExpectedDays       int
		ExpectedOpenDays   int
		ExpectedTotalHours string
	}
	raw, err := os.ReadFile("../../testdata/calendars.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	cal := domain.WorkCalendar{Template: weekdays(fx.Template["mon-fri"]...)}
	for _, c := range fx.ClosedDates {
		cal.ClosedDates = append(cal.ClosedDates, day(c))
	}
	started := time.Now()
	days, err := domain.Generate(cal, day(fx.From), day(fx.To))
	if elapsed := time.Since(started); elapsed >= 2*time.Second {
		t.Fatalf("generation took %s", elapsed)
	}
	if err != nil {
		t.Fatal(err)
	}
	open, total := 0, decimal.Zero
	for _, d := range days {
		if d.Open {
			open++
		}
		total = total.Add(d.TotalH)
	}
	if len(days) != fx.ExpectedDays || open != fx.ExpectedOpenDays || !total.Equal(dec(fx.ExpectedTotalHours)) {
		t.Fatalf("days=%d open=%d total=%s, want %d %d %s", len(days), open, total, fx.ExpectedDays, fx.ExpectedOpenDays, fx.ExpectedTotalHours)
	}
}

func TestValidateCalendar_Shifts(t *testing.T) {
	tests := []struct {
		name   string
		shifts []domain.Shift
		code   string
	}{
		{"overlap", []domain.Shift{shift("06:00", "10:00"), shift("09:00", "12:00")}, domain.CodeCalShift},
		{"efficiency zero", []domain.Shift{shiftEff("06:00", "10:00", "0")}, domain.CodeCalShift},
		{"efficiency over 100", []domain.Shift{shiftEff("06:00", "10:00", "101")}, domain.CodeCalShift},
		{"efficiency three decimals", []domain.Shift{shiftEff("06:00", "10:00", "99.999")}, domain.CodeCalShift},
		{"single digit hour", []domain.Shift{shift("9:00", "10:00")}, domain.CodeCalShift},
		{"hour 25", []domain.Shift{shift("06:00", "25:00")}, domain.CodeCalShift},
		{"minute 60", []domain.Shift{shift("06:60", "10:00")}, domain.CodeCalShift},
		{"empty start", []domain.Shift{shift("", "10:00")}, domain.CodeCalShift},
		{"start equals end", []domain.Shift{shift("06:00", "06:00")}, domain.CodeCalShift},
		{"start 24:00", []domain.Shift{shift("24:00", "02:00")}, domain.CodeCalShift},
		{"six shifts", []domain.Shift{shift("00:00", "01:00"), shift("02:00", "03:00"), shift("04:00", "05:00"), shift("06:00", "07:00"), shift("08:00", "09:00"), shift("10:00", "11:00")}, domain.CodeCalShift},
		{"crossing shift overlaps later shift", []domain.Shift{shift("22:00", "06:00"), shift("23:00", "23:30")}, domain.CodeCalShift},
		{"crossing midnight accepted", []domain.Shift{shift("22:00", "06:00")}, ""},
		{"end 24:00 accepted", []domain.Shift{shift("16:00", "24:00")}, ""},
		{"efficiency 100 accepted", []domain.Shift{shiftEff("06:00", "10:00", "100")}, ""},
		{"efficiency 1.5 accepted", []domain.Shift{shiftEff("06:00", "10:00", "1.5")}, ""},
		{"empty day accepted", nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.ValidateCalendar(domain.WorkCalendar{Template: weekdays(tc.shifts...)})
			if tc.code == "" && err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if tc.code != "" && !domain.HasCode(err, tc.code) {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
		})
	}
}

func TestValidateCalendar_ShiftsAcrossMidnight(t *testing.T) {
	var spill [7][]domain.Shift
	spill[0] = []domain.Shift{shift("22:00", "06:00")}
	spill[1] = []domain.Shift{shift("05:00", "13:00")}
	_, err := domain.ValidateCalendar(domain.WorkCalendar{Template: spill})
	if !domain.HasCode(err, domain.CodeCalShift) || !strings.HasPrefix(err.(*domain.Error).Detail, "Monday: ") || !strings.Contains(err.Error(), "Tuesday shift 05:00-13:00") {
		t.Fatalf("Monday night shift into Tuesday 05:00: err = %v, want ENG-CAL-SHIFT naming Monday and the Tuesday shift", err)
	}

	var sunday [7][]domain.Shift
	sunday[6] = []domain.Shift{shift("23:00", "01:00")}
	sunday[0] = []domain.Shift{shift("00:30", "08:00")}
	if _, err := domain.ValidateCalendar(domain.WorkCalendar{Template: sunday}); !domain.HasCode(err, domain.CodeCalShift) || !strings.HasPrefix(err.(*domain.Error).Detail, "Sunday: ") {
		t.Fatalf("Sunday night shift into Monday: err = %v, want ENG-CAL-SHIFT on Sunday", err)
	}

	var handover [7][]domain.Shift
	handover[0] = []domain.Shift{shift("22:00", "06:00")}
	handover[1] = []domain.Shift{shift("06:00", "14:00")}
	if _, err := domain.ValidateCalendar(domain.WorkCalendar{Template: handover}); err != nil {
		t.Fatalf("night shift ending as the morning shift starts: %v", err)
	}
}

func TestValidateCalendar_DetailNamesTheDay(t *testing.T) {
	var template [7][]domain.Shift
	template[2] = []domain.Shift{shift("06:00", "10:00"), shift("09:00", "12:00")}
	_, err := domain.ValidateCalendar(domain.WorkCalendar{Template: template})
	var de *domain.Error
	if !errors.As(err, &de) || de.Code != domain.CodeCalShift || de.Detail != "Wednesday: shifts overlap at 09:00" {
		t.Fatalf("err = %#v, want ENG-CAL-SHIFT \"Wednesday: shifts overlap at 09:00\"", err)
	}
}

func TestValidateCalendar_WeeklyHoursWarning(t *testing.T) {
	over, err := domain.ValidateCalendar(domain.WorkCalendar{Template: weekdays(shift("06:00", "16:00"))})
	if err != nil || len(over) != 1 {
		t.Fatalf("50 h/week: warnings=%v err=%v", over, err)
	}
	exact, err := domain.ValidateCalendar(domain.WorkCalendar{Template: weekdays(shift("06:00", "15:36"))})
	if err != nil || len(exact) != 0 {
		t.Fatalf("48 h/week: warnings=%v err=%v", exact, err)
	}
}

func TestGenerate_Range(t *testing.T) {
	cal := domain.WorkCalendar{Template: weekdays(shift("08:00", "16:00"))}
	tests := []struct {
		name     string
		from, to time.Time
		ok       bool
	}{
		{"to before from", day("2026-02-01"), day("2026-01-31"), false},
		{"zero from", time.Time{}, day("2026-01-31"), false},
		{"zero to", day("2026-01-31"), time.Time{}, false},
		{"over 60 months", day("2026-01-01"), day("2031-01-02"), false},
		{"exactly 60 months", day("2026-01-01"), day("2031-01-01"), true},
		{"single day", day("2026-01-05"), day("2026-01-05"), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			days, err := domain.Generate(cal, tc.from, tc.to)
			if tc.ok && err != nil {
				t.Fatal(err)
			}
			if !tc.ok && !domain.HasCode(err, domain.CodeCalRange) {
				t.Fatalf("err = %v, want %s", err, domain.CodeCalRange)
			}
			if tc.ok && len(days) == 0 {
				t.Fatal("no days")
			}
		})
	}
}

func TestGenerate_CrossingShiftCountsOnStartDay(t *testing.T) {
	var template [7][]domain.Shift
	template[0] = []domain.Shift{shift("22:00", "06:00")}
	days, err := domain.Generate(domain.WorkCalendar{Template: template}, day("2026-01-05"), day("2026-01-06"))
	if err != nil {
		t.Fatal(err)
	}
	if !days[0].Open || !days[0].TotalH.Equal(dec("8")) || days[1].Open {
		t.Fatalf("days = %+v", days)
	}
}

func TestGenerate_Properties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var template [7][]domain.Shift
		for i := range template {
			n := rapid.IntRange(0, 2).Draw(t, "shifts")
			if n >= 1 {
				template[i] = append(template[i], shift("06:00", "10:00"))
			}
			if n == 2 {
				template[i] = append(template[i], shift("11:00", "13:30"))
			}
		}
		from := day("2026-01-01").AddDate(0, 0, rapid.IntRange(0, 2000).Draw(t, "offset"))
		to := from.AddDate(0, 0, rapid.IntRange(0, 800).Draw(t, "length"))
		cal := domain.WorkCalendar{Template: template}
		for range rapid.IntRange(0, 10).Draw(t, "closedCount") {
			cal.ClosedDates = append(cal.ClosedDates, from.AddDate(0, 0, rapid.IntRange(0, 800).Draw(t, "closed")))
		}
		days, err := domain.Generate(cal, from, to)
		if err != nil {
			t.Fatal(err)
		}
		if want := int(to.Sub(from).Hours()/24) + 1; len(days) != want {
			t.Fatalf("len = %d, want %d", len(days), want)
		}
		closed := map[string]bool{}
		for _, c := range cal.ClosedDates {
			closed[c.Format("2006-01-02")] = true
		}
		patternHours := [3]decimal.Decimal{dec("0"), dec("4"), dec("6.5")}
		sum, expected := decimal.Zero, decimal.Zero
		for _, d := range days {
			sum = sum.Add(d.TotalH)
			weekday := (int(d.Date.Weekday()) + 6) % 7
			if closed[d.Date.Format("2006-01-02")] {
				if d.Open || !d.TotalH.IsZero() {
					t.Fatalf("closed date %s is open", d.Date)
				}
				continue
			}
			expected = expected.Add(patternHours[len(template[weekday])])
		}
		if !sum.Equal(expected) {
			t.Fatalf("sum = %s, want %s", sum, expected)
		}
	})
}
