package domain

import (
	"testing"
	"time"

	"pgregory.net/rapid"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// TestCompute_BR441Example is the worked example of BR-44.1: a TIME_OR_COUNTER
// line of 90 days or 500 hours, completed 2026-08-01 at 12,000 h, counter at
// 12,300 h on 2026-10-21 with 6.5 h/day usage.
func TestCompute_BR441Example(t *testing.T) {
	completed := day(2026, 8, 1)
	now := day(2026, 10, 21)
	line := PlanLine{Kind: KindTimeOrCounter, Interval: Interval{90, "DAYS"}, CounterInterval: 500,
		Start: completed, LastCompletedAt: &completed, LastCompletedReading: 12000}
	s, err := Compute(line, CounterState{Last: 12300, AvgDailyUsage: 6.5}, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.NextDueAt.Format("2006-01-02"); got != "2026-10-30" {
		t.Fatalf("next due = %s, want 2026-10-30", got)
	}
	if *s.NextDueReading != 12500 {
		t.Fatalf("next due reading = %v, want 12500", *s.NextDueReading)
	}
	if got := s.ForecastAt.Format("2006-01-02"); got != "2026-11-21" {
		t.Fatalf("forecast = %s, want 2026-11-21 (30.8 days, rounded up)", got)
	}
	if !Generate(s, 10, 90, now) {
		t.Fatal("expected a work order on 2026-10-21 with horizon 90")
	}
}

func TestCompute_KindsTable(t *testing.T) {
	completed := day(2026, 1, 1)
	now := day(2026, 1, 1)
	cases := []struct {
		name  string
		line  PlanLine
		usage float64
		last  float64
		want  string // expected next due date, "" for none
	}{
		{"time 3 months", PlanLine{Kind: KindTime, Interval: Interval{3, "MONTHS"}, Start: completed, LastCompletedAt: &completed}, 0, 0, "2026-04-01"},
		{"time 2 weeks", PlanLine{Kind: KindTime, Interval: Interval{2, "WEEKS"}, Start: completed}, 0, 0, "2026-01-15"},
		{"counter 500 at 6h/day is 83.3 days, so 84", PlanLine{Kind: KindCounter, CounterInterval: 500, Start: completed}, 6, 0, "2026-03-26"},
		{"counter already past its due reading is due now", PlanLine{Kind: KindCounter, CounterInterval: 500, Start: completed}, 6, 700, "2026-01-01"},
		{"counter without usage has no forecast", PlanLine{Kind: KindCounter, CounterInterval: 500, Start: completed}, 0, 0, ""},
		{"time and counter needs usage", PlanLine{Kind: KindTimeAndCounter, Interval: Interval{1, "YEARS"}, CounterInterval: 100, Start: completed}, 0, 0, ""},
		{"time and counter takes later", PlanLine{Kind: KindTimeAndCounter, Interval: Interval{30, "DAYS"}, CounterInterval: 100, Start: completed}, 1, 0, "2026-04-11"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := Compute(tc.line, CounterState{AvgDailyUsage: tc.usage, Last: tc.last}, now)
			if err != nil {
				t.Fatal(err)
			}
			got := ""
			if s.NextDueAt != nil {
				got = s.NextDueAt.Format("2006-01-02")
			}
			if got != tc.want {
				t.Fatalf("next due = %q, want %q", got, tc.want)
			}
		})
	}
}

// The due date is calendar arithmetic in UTC: a completion stored in a zone
// with a daylight-saving change still gives the same instant + N days in UTC.
func TestCompute_TimeZoneIndependent(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("no tz database")
	}
	completed := time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC).In(ny)
	s, err := Compute(PlanLine{Kind: KindTime, Interval: Interval{30, "DAYS"}, Start: completed, LastCompletedAt: &completed},
		CounterState{}, completed)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 30, 10, 0, 0, 0, time.UTC); !s.NextDueAt.Equal(want) {
		t.Fatalf("next due = %v, want %v", s.NextDueAt.UTC(), want)
	}
}

// TestAddInterval_MonthEnd: N months or years from a day the target month does
// not have lands on that month's last day, keeping the time of day (BR-44.1 (a)
// calendar.add). Go's AddDate normalises Jan 31 + 1 month to Mar 3, which
// skipped February for a monthly plan and gave the Jan 31 completion a later
// due date than the Feb 1 completion (Mar 1).
func TestAddInterval_MonthEnd(t *testing.T) {
	noon := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 12, 0, 0, 0, time.UTC) }
	cases := []struct {
		from time.Time
		iv   Interval
		want time.Time
	}{
		{noon(2026, 1, 31), Interval{1, "MONTHS"}, noon(2026, 2, 28)},
		{noon(2026, 1, 29), Interval{1, "MONTHS"}, noon(2026, 2, 28)},
		{noon(2028, 1, 31), Interval{1, "MONTHS"}, noon(2028, 2, 29)},
		{noon(2025, 11, 30), Interval{3, "MONTHS"}, noon(2026, 2, 28)},
		{noon(2025, 8, 31), Interval{6, "MONTHS"}, noon(2026, 2, 28)},
		{noon(2026, 3, 31), Interval{1, "MONTHS"}, noon(2026, 4, 30)},
		{noon(2025, 12, 31), Interval{2, "MONTHS"}, noon(2026, 2, 28)},
		{noon(2026, 1, 31), Interval{13, "MONTHS"}, noon(2027, 2, 28)},
		{noon(2024, 2, 29), Interval{2, "YEARS"}, noon(2026, 2, 28)},
		{noon(2024, 2, 29), Interval{4, "YEARS"}, noon(2028, 2, 29)},
		{noon(2026, 1, 15), Interval{1, "MONTHS"}, noon(2026, 2, 15)},
		{noon(2026, 1, 31), Interval{30, "DAYS"}, noon(2026, 3, 2)},
	}
	for _, tc := range cases {
		got, err := AddInterval(tc.from, tc.iv)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(tc.want) {
			t.Errorf("%s + %d %s = %s, want %s", tc.from.Format("2006-01-02"), tc.iv.N, tc.iv.Unit,
				got.Format(time.RFC3339), tc.want.Format(time.RFC3339))
		}
	}
	// A later completion never gives an earlier due date across a month end.
	jan31, feb1 := day(2026, 1, 31), day(2026, 2, 1)
	a, _ := AddInterval(jan31, Interval{1, "MONTHS"})
	b, _ := AddInterval(feb1, Interval{1, "MONTHS"})
	if b.Before(a) {
		t.Fatalf("completion %s gave due %s, earlier than %s gave %s", feb1, b, jan31, a)
	}
}

func TestCompute_InvalidLines(t *testing.T) {
	now := day(2026, 1, 1)
	for name, line := range map[string]PlanLine{
		"unknown kind":             {Kind: "WEEKLY"},
		"time without unit":        {Kind: KindTime, Interval: Interval{1, "FORTNIGHTS"}},
		"counter without interval": {Kind: KindCounter},
	} {
		if _, err := Compute(line, CounterState{}, now); err == nil {
			t.Errorf("%s: expected ErrInvalidLine", name)
		}
	}
}

func TestGenerate_Horizon(t *testing.T) {
	due := day(2026, 10, 30)
	s := Schedule{NextDueAt: &due, Span: 90 * 24 * time.Hour}
	cases := []struct {
		now     time.Time
		horizon int
		want    bool
	}{
		{day(2026, 10, 21), 90, true},
		{day(2026, 7, 1), 90, false},
		{day(2026, 10, 30), 0, true},
	}
	for _, tc := range cases {
		if got := Generate(s, 0, tc.horizon, tc.now); got != tc.want {
			t.Errorf("now=%s horizon=%d: got %v want %v", tc.now.Format("2006-01-02"), tc.horizon, got, tc.want)
		}
	}
	if Generate(Schedule{}, 10, 90, day(2026, 1, 1)) {
		t.Error("a line without a due date must not generate")
	}
}

// TestCompute_MonotonicUnderCompletion: a later completion never yields an
// earlier next due date for a TIME line (property, rapid).
func TestCompute_MonotonicUnderCompletion(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		a := rapid.IntRange(0, 2000).Draw(rt, "a")
		b := rapid.IntRange(a, 2500).Draw(rt, "b")
		start := day(2020, 1, 1)
		now := day(2026, 1, 1)
		unit := rapid.SampledFrom([]string{"DAYS", "WEEKS", "MONTHS"}).Draw(rt, "unit")
		iv := Interval{rapid.IntRange(1, 12).Draw(rt, "n"), unit}
		ca := start.AddDate(0, 0, a)
		cb := start.AddDate(0, 0, b)
		sa, err := Compute(PlanLine{Kind: KindTime, Interval: iv, Start: start, LastCompletedAt: &ca}, CounterState{}, now)
		if err != nil {
			rt.Fatal(err)
		}
		sb, err := Compute(PlanLine{Kind: KindTime, Interval: iv, Start: start, LastCompletedAt: &cb}, CounterState{}, now)
		if err != nil {
			rt.Fatal(err)
		}
		if sb.NextDueAt.Before(*sa.NextDueAt) {
			rt.Fatalf("completion on %s gave due %s, earlier than %s gave %s", cb, sb.NextDueAt, ca, sa.NextDueAt)
		}
	})
}
