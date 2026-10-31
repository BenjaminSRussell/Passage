package domain

import (
	"strings"
	"testing"
	"time"
)

func sp(s string) *string { return &s }

func TestParseClock(t *testing.T) {
	for in, want := range map[string]time.Duration{"00:00": 0, "07:30": 7*time.Hour + 30*time.Minute, "23:59": 23*time.Hour + 59*time.Minute} {
		if got, ok := ParseClock(in); !ok || got != want {
			t.Errorf("ParseClock(%q) = %v, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "7:30", "24:00", "12:60", "ab:cd", "12-30", "12:300"} {
		if _, ok := ParseClock(in); ok {
			t.Errorf("ParseClock(%q) accepted", in)
		}
	}
	if FormatClock(7*time.Hour+5*time.Minute) != "07:05" {
		t.Error("FormatClock")
	}
}

func TestProfileDayInput_Validation(t *testing.T) {
	good := ProfileDayInput{DayType: DayTypeWeekday, ShiftStart: sp("07:00"), ShiftEnd: sp("15:30"), RoundingMinutes: 5, ToleranceBeforeMin: 10, ToleranceAfterMin: 5,
		Breaks: []BreakInput{{From: "12:00", To: "12:30"}, {From: "09:30", To: "09:45"}}}
	p, fields := good.Profile("")
	if len(fields) != 0 || p.ShiftStart == nil || *p.ShiftStart != 7*time.Hour || len(p.Breaks) != 2 || p.Breaks[0].From != 9*time.Hour+30*time.Minute {
		t.Fatalf("good = %+v %v", p, fields)
	}
	cases := map[string]func(in *ProfileDayInput){
		"dayType":            func(in *ProfileDayInput) { in.DayType = "holiday" },
		"roundingMinutes":    func(in *ProfileDayInput) { in.RoundingMinutes = 16 },
		"toleranceBeforeMin": func(in *ProfileDayInput) { in.ToleranceBeforeMin = 61 },
		"toleranceAfterMin":  func(in *ProfileDayInput) { in.ToleranceAfterMin = -1 },
		"shiftStart":         func(in *ProfileDayInput) { in.ShiftEnd = nil },
		"shiftEnd":           func(in *ProfileDayInput) { in.ShiftEnd = sp("06:00") },
		"breaks[0]":          func(in *ProfileDayInput) { in.Breaks = []BreakInput{{From: "13:00", To: "12:00"}} },
		"breaks": func(in *ProfileDayInput) {
			in.Breaks = []BreakInput{{From: "12:00", To: "13:00"}, {From: "12:30", To: "13:30"}}
		},
	}
	for field, mutate := range cases {
		in := good
		in.Breaks = append([]BreakInput(nil), good.Breaks...)
		mutate(&in)
		if _, f := in.Profile(""); f[field] == "" {
			t.Errorf("%s: not refused, fields = %v", field, f)
		}
	}
}

func TestProfileFromDays_DuplicateDayType(t *testing.T) {
	d := ProfileDayInput{DayType: DayTypeSunday, RoundingMinutes: 1}
	if _, f := ProfileFromDays([]ProfileDayInput{d, d}); !strings.Contains(f["days[1].dayType"], "once") {
		t.Fatalf("fields = %v", f)
	}
}

func TestProfileSet_FallsBackToDefaultForUndefinedDayType(t *testing.T) {
	loc := time.UTC
	weekday := Profile{RoundingMinutes: 15}
	set := ProfileSet{DayTypeWeekday: weekday}
	tue := time.Date(2026, 10, 20, 9, 0, 0, 0, loc)
	sun := time.Date(2026, 10, 25, 9, 0, 0, 0, loc)
	if set.For(tue, loc).RoundingMinutes != 15 || set.For(sun, loc).RoundingMinutes != 1 {
		t.Fatal("weekday must use the profile and sunday the default")
	}
	if DayTypeOf(time.Date(2026, 10, 24, 0, 0, 0, 0, loc)) != DayTypeSaturday {
		t.Fatal("saturday")
	}
	// A late evening UTC time is the next local day in Tokyo.
	tokyo, _ := time.LoadLocation("Asia/Tokyo")
	if got := set.For(time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC), tokyo); got.RoundingMinutes != 1 {
		t.Fatalf("local day not used: %+v", got)
	}
}

func TestAgreementInput_Rules(t *testing.T) {
	r, f := AgreementInput{DailyThreshold: "10", WeeklyThreshold: "45.5", OvertimeMultiplier: "1.25", SundayMultiplier: "2", WeekStart: 0}.Rules()
	if len(f) != 0 || r.DailyThreshold.String() != "10" || r.WeeklyThreshold.String() != "45.5" || r.WeekStart != time.Sunday {
		t.Fatalf("rules = %+v %v", r, f)
	}
	_, f = AgreementInput{DailyThreshold: "25", WeeklyThreshold: "169", OvertimeMultiplier: "0.9", SundayMultiplier: "x", WeekStart: 7}.Rules()
	for _, k := range []string{"dailyThreshold", "weeklyThreshold", "overtimeMultiplier", "sundayMultiplier", "weekStart"} {
		if f[k] == "" {
			t.Errorf("%s not refused: %v", k, f)
		}
	}
	if _, f := (AgreementInput{DailyThreshold: "8.001", WeeklyThreshold: "40", OvertimeMultiplier: "1.5", SundayMultiplier: "2"}).Rules(); f["dailyThreshold"] == "" {
		t.Error("three decimals on a threshold must be refused")
	}
}

// A worker's profile supplies the break per day type and the agreement the thresholds:
// 09:00-19:30 with the 30-minute weekday break is 8.00 base and 2.00 overtime (AC-25.2),
// and with a 10 h daily threshold it is all base. Sunday has no profile day, so it pays
// the default 2.0x with no break taken out.
func TestCalculation_UsesProfileAndAgreement(t *testing.T) {
	set := ProfileSet{DayTypeWeekday: {RoundingMinutes: 1, Breaks: []Window{{From: 12 * time.Hour, To: 12*time.Hour + 30*time.Minute}}}}
	profileFor := func(at time.Time) Profile { return set.For(at, utc) }
	punches := func(day int) []Punch {
		return []Punch{{ID: "i", Kind: KindClockIn, At: time.Date(2026, 10, day, 9, 0, 0, 0, utc)}, {ID: "o", Kind: KindClockOut, At: time.Date(2026, 10, day, 19, 30, 0, 0, utc)}}
	}
	run := func(rules Rules, day int) []DayPay {
		c := Calculation{ProfileFor: profileFor, Location: utc, Rules: rules, Rate: dec("20"), Currency: "USD"}
		out, err := c.Run(BuildDaysFor(punches(day), utc, profileFor))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	tue := run(DefaultRules(), 20)
	if lineHours(tue[0].Lines, TypeBase) != "8.00" || lineHours(tue[0].Lines, TypeOvertime15) != "2.00" {
		t.Fatalf("default agreement: %+v", tue[0].Lines)
	}
	rules, _ := AgreementInput{DailyThreshold: "10", WeeklyThreshold: "40", OvertimeMultiplier: "1.5", SundayMultiplier: "2", WeekStart: 1}.Rules()
	tue = run(rules, 20)
	if len(tue[0].Lines) != 1 || tue[0].Lines[0].Type != TypeBase || tue[0].Lines[0].Hours.String() != "10" {
		t.Fatalf("10 h daily threshold: %+v", tue[0].Lines)
	}
	sun := run(DefaultRules(), 25)
	if len(sun[0].Lines) != 1 || sun[0].Lines[0].Type != TypeOvertime20 || sun[0].Lines[0].Hours.String() != "10.5" {
		t.Fatalf("sunday (no profile day, no break): %+v", sun[0].Lines)
	}
}

func TestPaySetup_DefaultsWithoutAgreement(t *testing.T) {
	if r := (PaySetup{}).EffectiveRules(); !r.DailyThreshold.Equal(DefaultRules().DailyThreshold) {
		t.Fatal("zero setup must use the default rules")
	}
	rules, _ := AgreementInput{DailyThreshold: "9", WeeklyThreshold: "40", OvertimeMultiplier: "1.5", SundayMultiplier: "2"}.Rules()
	if (PaySetup{Rules: &rules}).EffectiveRules().DailyThreshold.String() != "9" {
		t.Fatal("agreement rules not used")
	}
}
