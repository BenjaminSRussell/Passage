package domain

import (
	"strings"
	"testing"
	"time"
)

func premiumRules(t *testing.T, in ...ShiftPremiumInput) Rules {
	t.Helper()
	r, f := AgreementInput{DailyThreshold: "8", WeeklyThreshold: "40", OvertimeMultiplier: "1.5", SundayMultiplier: "2", WeekStart: 1, ShiftPremiums: in}.Rules()
	if len(f) > 0 {
		t.Fatal(f)
	}
	return r
}

func runPremium(t *testing.T, rules Rules, p Profile, in, out time.Time) []DayPay {
	t.Helper()
	pf := func(time.Time) Profile { return p }
	c := Calculation{ProfileFor: pf, Location: utc, Rules: rules, Rate: dec("20"), Currency: "USD"}
	days, err := c.Run(BuildDaysFor([]Punch{{ID: "i", Kind: KindClockIn, At: in}, {ID: "o", Kind: KindClockOut, At: out}}, utc, pf))
	if err != nil {
		t.Fatal(err)
	}
	return days
}

// An evening window 18:00-22:00 at 25%: a shift 14:00-22:00 has four hours inside it.
// The premium line carries the premium rate, and the base line is unchanged.
func TestShiftPremium_EveningWindow(t *testing.T) {
	rules := premiumRules(t, ShiftPremiumInput{Name: "Evening", From: "18:00", To: "22:00", Premium: "0.25"})
	days := runPremium(t, rules, Profile{RoundingMinutes: 1}, time.Date(2026, 10, 20, 14, 0, 0, 0, utc), time.Date(2026, 10, 20, 22, 0, 0, 0, utc))
	if lineHours(days[0].Lines, TypeBase) != "8.00" || lineHours(days[0].Lines, TypeShiftPremium) != "4.00" {
		t.Fatalf("lines = %+v", days[0].Lines)
	}
	for _, l := range days[0].Lines {
		if l.Type == TypeShiftPremium && (l.Multiplier.String() != "0.25" || l.Amount.String() != "20") {
			t.Fatalf("premium line = %+v", l)
		}
	}
}

// A night window 22:00-06:00 crosses midnight: a shift 20:00-04:00 starting Tuesday has
// six hours inside it, and a shift 04:00-12:00 on Wednesday has two (the morning part).
func TestShiftPremium_CrossesMidnight(t *testing.T) {
	rules := premiumRules(t, ShiftPremiumInput{Name: "Night", From: "22:00", To: "06:00", Premium: "0.1"})
	night := runPremium(t, rules, Profile{RoundingMinutes: 1}, time.Date(2026, 10, 20, 20, 0, 0, 0, utc), time.Date(2026, 10, 21, 4, 0, 0, 0, utc))
	if got := lineHours(night[0].Lines, TypeShiftPremium); got != "6.00" {
		t.Fatalf("night shift premium hours = %s (%+v)", got, night[0].Lines)
	}
	early := runPremium(t, rules, Profile{RoundingMinutes: 1}, time.Date(2026, 10, 21, 4, 0, 0, 0, utc), time.Date(2026, 10, 21, 12, 0, 0, 0, utc))
	if got := lineHours(early[0].Lines, TypeShiftPremium); got != "2.00" {
		t.Fatalf("early shift premium hours = %s", got)
	}
}

// Unpaid breaks inside the window come off the premium hours, and a shift outside the
// window earns no premium line.
func TestShiftPremium_BreakAndOutside(t *testing.T) {
	rules := premiumRules(t, ShiftPremiumInput{Name: "Evening", From: "18:00", To: "22:00", Premium: "0.25"})
	p := Profile{RoundingMinutes: 1, Breaks: []Window{{From: 18 * time.Hour, To: 18*time.Hour + 30*time.Minute}}}
	days := runPremium(t, rules, p, time.Date(2026, 10, 20, 14, 0, 0, 0, utc), time.Date(2026, 10, 20, 22, 0, 0, 0, utc))
	if got := lineHours(days[0].Lines, TypeShiftPremium); got != "3.50" {
		t.Fatalf("premium hours with break = %s", got)
	}
	day := runPremium(t, rules, Profile{RoundingMinutes: 1}, time.Date(2026, 10, 20, 7, 0, 0, 0, utc), time.Date(2026, 10, 20, 15, 0, 0, 0, utc))
	if got := lineHours(day[0].Lines, TypeShiftPremium); got != "0.00" {
		t.Fatalf("day shift got premium hours %s", got)
	}
}

func TestShiftPremiums_Validation(t *testing.T) {
	_, f := ShiftPremiums([]ShiftPremiumInput{
		{Name: "", From: "25:00", To: "06:00", Premium: "0.1"},
		{Name: "x", From: "08:00", To: "08:00", Premium: "0"},
		{Name: "y", From: "08:00", To: "09:00", Premium: "2.5"},
		{Name: "z", From: "08:00", To: "09:00", Premium: "0.12345"},
	})
	for _, k := range []string{"shiftPremiums[0].name", "shiftPremiums[0].from", "shiftPremiums[1].to", "shiftPremiums[1].premium", "shiftPremiums[2].premium", "shiftPremiums[3].premium"} {
		if f[k] == "" {
			t.Errorf("no problem for %s: %v", k, f)
		}
	}
	_, f = ShiftPremiums([]ShiftPremiumInput{
		{Name: "a", From: "22:00", To: "06:00", Premium: "0.1"},
		{Name: "b", From: "05:00", To: "07:00", Premium: "0.1"},
	})
	if !strings.Contains(f["shiftPremiums[1].from"], "overlap") {
		t.Errorf("overlap across midnight: %v", f)
	}
	if out, f := ShiftPremiums([]ShiftPremiumInput{{Name: "a", From: "22:00", To: "06:00", Premium: "0.1"}, {Name: "b", From: "06:00", To: "22:00", Premium: "0.05"}}); len(f) > 0 || len(out) != 2 {
		t.Errorf("adjacent windows: %v", f)
	}
	tooMany := make([]ShiftPremiumInput, MaxShiftPremiums+1)
	if _, f := ShiftPremiums(tooMany); f["shiftPremiums"] == "" {
		t.Errorf("too many: %v", f)
	}
}

// A window after midnight that does not cross it (00:00-06:00) still counts for a shift
// that starts the evening before: a 20:00-04:00 shift has four hours in it, all on the
// day the shift started.
func TestShiftPremium_WindowAfterMidnightOfEveningShift(t *testing.T) {
	rules := premiumRules(t, ShiftPremiumInput{Name: "Graveyard", From: "00:00", To: "06:00", Premium: "0.2"})
	days := runPremium(t, rules, Profile{RoundingMinutes: 1}, time.Date(2026, 10, 20, 20, 0, 0, 0, utc), time.Date(2026, 10, 21, 4, 0, 0, 0, utc))
	if len(days) != 1 || lineHours(days[0].Lines, TypeShiftPremium) != "4.00" || lineHours(days[0].Lines, TypeBase) != "8.00" {
		t.Fatalf("lines = %+v", days)
	}
}

// Two windows produce one line each, and each line pays rate x hours x its own premium.
func TestShiftPremium_TwoWindowsTwoLines(t *testing.T) {
	rules := premiumRules(t,
		ShiftPremiumInput{Name: "Evening", From: "18:00", To: "22:00", Premium: "0.1"},
		ShiftPremiumInput{Name: "Night", From: "22:00", To: "06:00", Premium: "0.25"})
	days := runPremium(t, rules, Profile{RoundingMinutes: 1}, time.Date(2026, 10, 20, 16, 0, 0, 0, utc), time.Date(2026, 10, 21, 0, 30, 0, 0, utc))
	var got []string
	for _, l := range days[0].Lines {
		if l.Type == TypeShiftPremium {
			got = append(got, l.Hours.StringFixed(2)+"@"+l.Multiplier.String()+"="+l.Amount.StringFixed(2))
		}
	}
	if strings.Join(got, ",") != "4.00@0.1=8.00,2.50@0.25=12.50" {
		t.Fatalf("premium lines = %v", got)
	}
}

// A name counts characters, not bytes: 60 accented letters are accepted.
func TestShiftPremiums_NameCountsCharacters(t *testing.T) {
	if _, f := ShiftPremiums([]ShiftPremiumInput{{Name: strings.Repeat("é", 60), From: "22:00", To: "06:00", Premium: "0.1"}}); len(f) > 0 {
		t.Fatalf("60-character name refused: %v", f)
	}
	if _, f := ShiftPremiums([]ShiftPremiumInput{{Name: strings.Repeat("é", 61), From: "22:00", To: "06:00", Premium: "0.1"}}); f["shiftPremiums[0].name"] == "" {
		t.Fatalf("61-character name accepted")
	}
}
