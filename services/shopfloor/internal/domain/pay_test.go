package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

var utc = time.UTC

// day builds a local day in UTC with one closed segment of the given hours,
// starting at 09:00 on the date.
func day(y int, m time.Month, d int, hours float64) Day {
	start := time.Date(y, m, d, 9, 0, 0, 0, utc)
	end := start.Add(time.Duration(hours * float64(time.Hour)))
	return Day{Date: time.Date(y, m, d, 0, 0, 0, 0, utc), Segments: []Segment{{Start: start, End: end, Sources: []string{"r"}}}}
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func lineHours(lines []PayLine, typ string) string {
	total := decimal.Zero
	for _, l := range lines {
		if l.Type == typ {
			total = total.Add(l.Hours)
		}
	}
	return total.StringFixed(2)
}

func TestRound_BR25_2(t *testing.T) {
	seven := 7 * time.Hour
	fifteenThirty := 15*time.Hour + 30*time.Minute
	shift := DefaultProfile()
	shift.ShiftStart, shift.ShiftEnd = &seven, &fifteenThirty
	at := func(h, m int) time.Time { return time.Date(2026, 10, 20, h, m, 0, 0, utc) }
	cases := []struct {
		name string
		at   time.Time
		kind string
		p    Profile
		want time.Time
	}{
		{"AC-25.7 clock-in 06:53 snaps to 07:00", at(6, 53), KindClockIn, shift, at(7, 0)},
		{"clock-in 06:40 is outside tolerance and rounds to the minute", at(6, 40), KindClockIn, shift, at(6, 40)},
		{"clock-out 15:33 snaps to 15:30", at(15, 33), KindClockOut, shift, at(15, 30)},
		{"clock-out 15:36 is outside tolerance", at(15, 36), KindClockOut, shift, at(15, 36)},
		{"no shift: 08:00:29 rounds down to 08:00", time.Date(2026, 10, 20, 8, 0, 29, 0, utc), KindClockIn, DefaultProfile(), at(8, 0)},
		{"no shift: 08:00:31 rounds up to 08:01", time.Date(2026, 10, 20, 8, 0, 31, 0, utc), KindClockIn, DefaultProfile(), at(8, 1)},
		{"15-minute rounding: 08:07 to 08:00", at(8, 7), KindClockIn, Profile{RoundingMinutes: 15}, at(8, 0)},
		{"15-minute rounding: 08:08 to 08:15", at(8, 8), KindClockIn, Profile{RoundingMinutes: 15}, at(8, 15)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Round(c.at, c.kind, c.p, utc); !got.Equal(c.want) {
				t.Fatalf("got %s want %s", got, c.want)
			}
		})
	}
}

func TestBuildDays_PairsAndFlags(t *testing.T) {
	p := DefaultProfile()
	base := time.Date(2026, 10, 20, 0, 0, 0, 0, utc)
	at := func(d, h int) time.Time { return base.AddDate(0, 0, d).Add(time.Duration(h) * time.Hour) }
	cases := []struct {
		name    string
		punches []Punch
		days    int
		flags   []string
		hours   float64
	}{
		{"one pair", []Punch{{"a", KindClockIn, at(0, 9)}, {"b", KindClockOut, at(0, 17)}}, 1, nil, 8},
		{"unpaired clock-in is flagged", []Punch{{"a", KindClockIn, at(0, 9)}}, 1, []string{FlagMissingClockOut}, 0},
		{"orphan clock-out is flagged", []Punch{{"b", KindClockOut, at(0, 17)}}, 1, []string{FlagOrphanClockOut}, 0},
		{"two days stay separate", []Punch{{"a", KindClockIn, at(0, 9)}, {"b", KindClockOut, at(0, 17)}, {"c", KindClockIn, at(1, 9)}, {"d", KindClockOut, at(1, 12)}}, 2, nil, 11},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			days := BuildDays(c.punches, utc, p)
			if len(days) != c.days {
				t.Fatalf("days = %d want %d", len(days), c.days)
			}
			var got float64
			for _, d := range days {
				for _, s := range d.Segments {
					got += NetHours(s, p, utc).InexactFloat64()
				}
			}
			if got != c.hours {
				t.Fatalf("hours = %v want %v", got, c.hours)
			}
			if c.flags != nil && (len(days[0].Flags) != 1 || days[0].Flags[0] != c.flags[0]) {
				t.Fatalf("flags = %v want %v", days[0].Flags, c.flags)
			}
		})
	}
}

func TestNetHours_BreakIsDeducted_AC25_2(t *testing.T) {
	p := DefaultProfile()
	p.Breaks = []Window{{From: 12 * time.Hour, To: 12*time.Hour + 30*time.Minute}}
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, utc)
	seg := Segment{Start: start, End: time.Date(2026, 10, 20, 19, 30, 0, 0, utc)}
	if got := NetHours(seg, p, utc).StringFixed(2); got != "10.00" {
		t.Fatalf("net hours = %s want 10.00", got)
	}
}

// AC-25.2: Tuesday 09:00-19:30 with a 30-minute break is 8.00 base and 2.00 overtime-1.5.
func TestCalculate_AC25_2_TuesdayOvertime(t *testing.T) {
	p := DefaultProfile()
	p.Breaks = []Window{{From: 12 * time.Hour, To: 12*time.Hour + 30*time.Minute}}
	c := Calculation{Profile: p, Location: utc, Rules: DefaultRules(), Rate: dec("20"), Currency: "USD"}
	tuesday := Day{Date: time.Date(2026, 10, 20, 0, 0, 0, 0, utc), Segments: []Segment{{
		Start: time.Date(2026, 10, 20, 9, 0, 0, 0, utc), End: time.Date(2026, 10, 20, 19, 30, 0, 0, utc), Sources: []string{"in", "out"}}}}
	days, err := c.Run([]Day{tuesday})
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || len(days[0].Lines) != 2 {
		t.Fatalf("lines = %+v", days)
	}
	if got := lineHours(days[0].Lines, TypeBase); got != "8.00" {
		t.Fatalf("base = %s", got)
	}
	if got := lineHours(days[0].Lines, TypeOvertime15); got != "2.00" {
		t.Fatalf("overtime-1.5 = %s", got)
	}
	if amt := days[0].Lines[1].Amount.String(); amt != "60" {
		t.Fatalf("overtime amount = %s want 60 (2h x 1.5 x 20)", amt)
	}
}

// Table-driven pay rules: thresholds, weekly rollover, Sunday and the 24 h cap.
func TestCalculate_PayRules(t *testing.T) {
	c := Calculation{Profile: DefaultProfile(), Location: utc, Rules: DefaultRules(), Rate: dec("10"), Currency: "USD"}
	// 2026-10-19 is a Monday; the week runs Monday to Sunday.
	cases := []struct {
		name  string
		hours []float64 // Mon..Sun, 0 = no work
		base  []string  // per day base hours
		ot15  []string  // per day overtime-1.5 hours
		ot20  []string  // per day overtime-2.0 hours
	}{
		{"no overtime", []float64{8, 8, 8, 8, 8, 0, 0}, []string{"8.00", "8.00", "8.00", "8.00", "8.00", "0.00", "0.00"}, []string{"0.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00"}, []string{"0.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00"}},
		{"daily over 8", []float64{10, 0, 0, 0, 0, 0, 0}, []string{"8.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00"}, []string{"2.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00"}, []string{"0.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00"}},
		{"exactly 40 base hours in the week has no weekly overtime", []float64{8, 8, 8, 8, 8, 0, 0}, []string{"8.00", "8.00", "8.00", "8.00", "8.00", "0.00", "0.00"}, []string{"0.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00"}, []string{"0.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00"}},
		{"sixth day beyond 40 is overtime", []float64{8, 8, 8, 8, 8, 4, 0}, []string{"8.00", "8.00", "8.00", "8.00", "8.00", "0.00", "0.00"}, []string{"0.00", "0.00", "0.00", "0.00", "0.00", "4.00", "0.00"}, []string{"0.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00"}},
		{"sunday is double time and not base", []float64{0, 0, 0, 0, 0, 0, 6}, []string{"0.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00"}, []string{"0.00", "0.00", "0.00", "0.00", "0.00", "0.00", "0.00"}, []string{"0.00", "0.00", "0.00", "0.00", "0.00", "0.00", "6.00"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var days []Day
			for i, h := range tc.hours {
				if h == 0 {
					continue
				}
				days = append(days, day(2026, 10, 19+i, h))
			}
			out, err := c.Run(days)
			if err != nil {
				t.Fatal(err)
			}
			for i := range tc.hours {
				date := time.Date(2026, 10, 19+i, 0, 0, 0, 0, utc)
				var got *DayPay
				for j := range out {
					if out[j].Date.Equal(date) {
						got = &out[j]
					}
				}
				if got == nil {
					if tc.hours[i] != 0 {
						t.Fatalf("day %d missing", i)
					}
					continue
				}
				if s := lineHours(got.Lines, TypeBase); s != tc.base[i] {
					t.Errorf("day %d base %s want %s", i, s, tc.base[i])
				}
				if s := lineHours(got.Lines, TypeOvertime15); s != tc.ot15[i] {
					t.Errorf("day %d ot15 %s want %s", i, s, tc.ot15[i])
				}
				if s := lineHours(got.Lines, TypeOvertime20); s != tc.ot20[i] {
					t.Errorf("day %d ot20 %s want %s", i, s, tc.ot20[i])
				}
			}
		})
	}
}

func TestCalculate_RejectsMoreThan24Hours(t *testing.T) {
	c := Calculation{Profile: DefaultProfile(), Location: utc, Rules: DefaultRules(), Rate: dec("1"), Currency: "USD"}
	d := Day{Date: time.Date(2026, 10, 20, 0, 0, 0, 0, utc), Segments: []Segment{
		{Start: time.Date(2026, 10, 20, 0, 0, 0, 0, utc), End: time.Date(2026, 10, 20, 13, 0, 0, 0, utc)},
		{Start: time.Date(2026, 10, 20, 13, 0, 0, 0, utc), End: time.Date(2026, 10, 21, 0, 0, 0, 0, utc).Add(12 * time.Hour)},
	}}
	_, err := c.Run([]Day{d})
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeHoursExceed {
		t.Fatalf("err = %v, want %s", err, CodeHoursExceed)
	}
}

// Property: for any week of worked days, each day's lines sum to its hours,
// base hours never exceed the daily cap, and weekly base never exceeds 40.
func TestCalculate_Property_LinesSumAndCaps(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		c := Calculation{Profile: DefaultProfile(), Location: utc, Rules: DefaultRules(), Rate: dec("12.5"), Currency: "EUR"}
		var days []Day
		for i := 0; i < 7; i++ {
			h := rapid.IntRange(0, 16).Draw(t, "hours")
			if h == 0 {
				continue
			}
			days = append(days, day(2026, 10, 19+i, float64(h)))
		}
		out, err := c.Run(days)
		if err != nil {
			t.Fatal(err)
		}
		weekBase := decimal.Zero
		for _, d := range out {
			sum := decimal.Zero
			for _, l := range d.Lines {
				if l.Hours.IsNegative() {
					t.Fatalf("negative line %s", l.Hours)
				}
				sum = sum.Add(l.Hours)
				if l.Type == TypeBase {
					if l.Hours.GreaterThan(DefaultRules().DailyThreshold) {
						t.Fatalf("base %s exceeds daily threshold", l.Hours)
					}
					weekBase = weekBase.Add(l.Hours)
				}
			}
			if !sum.Equal(d.Hours) {
				t.Fatalf("lines sum %s != hours %s", sum, d.Hours)
			}
		}
		if weekBase.GreaterThan(DefaultRules().WeeklyThreshold) {
			t.Fatalf("weekly base %s exceeds 40", weekBase)
		}
	})
}

// BR-25.8: the weekly threshold restarts each pay week. Two weeks of 6 x 8 h
// (Monday to Saturday) give 8.00 h weekly overtime in each week, not 16.00 h in
// the second.
func TestCalculate_WeeklyThresholdRestartsEachPayWeek(t *testing.T) {
	c := Calculation{Profile: DefaultProfile(), Location: utc, Rules: DefaultRules(), Rate: dec("10"), Currency: "USD"}
	var days []Day
	for w := 0; w < 2; w++ {
		for i := 0; i < 6; i++ {
			days = append(days, day(2026, 10, 19+7*w+i, 8))
		}
	}
	out, err := c.Run(days)
	if err != nil {
		t.Fatal(err)
	}
	for w := 0; w < 2; w++ {
		base, ot := decimal.Zero, decimal.Zero
		for _, d := range out[w*6 : w*6+6] {
			for _, l := range d.Lines {
				switch l.Type {
				case TypeBase:
					base = base.Add(l.Hours)
				case TypeOvertime15:
					ot = ot.Add(l.Hours)
				}
			}
		}
		if base.StringFixed(2) != "40.00" || ot.StringFixed(2) != "8.00" {
			t.Fatalf("week %d: base %s overtime %s, want 40.00 and 8.00", w+1, base.StringFixed(2), ot.StringFixed(2))
		}
	}
}

// A pay week can start on another day (profile-defined start day, BR-25.8).
func TestWeekStartOf(t *testing.T) {
	wed := time.Date(2026, 10, 21, 0, 0, 0, 0, utc)
	cases := []struct {
		start time.Weekday
		want  int // day of month
	}{{time.Monday, 19}, {time.Sunday, 18}, {time.Wednesday, 21}, {time.Thursday, 15}}
	for _, c := range cases {
		if got := WeekStartOf(wed, c.start); got.Day() != c.want {
			t.Errorf("week start %s of 2026-10-21 = %s, want day %d", c.start, got.Format("2006-01-02"), c.want)
		}
	}
}

func TestElapsedHours(t *testing.T) {
	start := time.Date(2026, 10, 20, 9, 0, 0, 0, utc)
	cases := []struct {
		stop time.Time
		want string
	}{
		{start.Add(90 * time.Minute), "1.5000"},
		{start.Add(20 * time.Minute), "0.3333"},
		{start, "0.0000"},
		{start.Add(-time.Hour), "0.0000"},
	}
	for _, c := range cases {
		if got := ElapsedHours(start, c.stop).StringFixed(4); got != c.want {
			t.Errorf("ElapsedHours(%s) = %s, want %s", c.stop.Sub(start), got, c.want)
		}
	}
}
