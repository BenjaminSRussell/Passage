package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func monthlyLines(n int) []Line {
	lines := make([]Line, 0, n)
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		p := start.AddDate(0, i, 0)
		lines = append(lines, Line{Index: i + 1, Period: p.Format("2006-01"), Amount: decimal.NewFromInt(100)})
	}
	return lines
}

func TestDecideRun_Table(t *testing.T) {
	lines := monthlyLines(3) // 2026-01, 2026-02, 2026-03
	cases := []struct {
		name, last, period string
		want               RunOutcome
		code               string
	}{
		{name: "first period due", last: "", period: "2026-01", want: RunDue},
		{name: "next period due", last: "2026-01", period: "2026-02", want: RunDue},
		{name: "already posted", last: "2026-02", period: "2026-02", want: RunException, code: CodeAlreadyPosted},
		{name: "earlier than last posted", last: "2026-02", period: "2026-01", want: RunException, code: CodeAlreadyPosted},
		{name: "gap blocks run", last: "", period: "2026-03", want: RunException, code: CodePreviousUnposted},
		{name: "gap after posting", last: "2026-01", period: "2026-03", want: RunException, code: CodePreviousUnposted},
		{name: "fully depreciated", last: "2026-03", period: "2026-04", want: RunNone},
		{name: "not yet due", last: "", period: "2025-12", want: RunNone},
		{name: "yearly run skips a monthly schedule", last: "", period: "2026", want: RunNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := DecideRun(lines, c.last, c.period)
			if got.Outcome != c.want || got.Code != c.code {
				t.Fatalf("got %+v, want outcome %s code %q", got, c.want, c.code)
			}
			if got.Outcome == RunDue && got.Line.Period != c.period {
				t.Fatalf("due line period = %s, want %s", got.Line.Period, c.period)
			}
		})
	}
}

func TestPeriodStart(t *testing.T) {
	cases := map[string]string{"2026-03": "2026-03-01", "2026": "2026-01-01"}
	for in, want := range cases {
		got, err := PeriodStart(in)
		if err != nil || got.Format("2006-01-02") != want {
			t.Errorf("PeriodStart(%q) = %v, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "2026-3", "March", "2026-13x", "20260"} {
		if _, err := PeriodStart(bad); err == nil {
			t.Errorf("PeriodStart(%q) accepted", bad)
		}
	}
}

func TestDecideRun_MonthlyRunSkipsYearlySchedule(t *testing.T) {
	lines := []Line{{Index: 1, Period: "2026", Amount: d("5000")}, {Index: 2, Period: "2027", Amount: d("8000")}}
	if got := DecideRun(lines, "", "2026-03"); got.Outcome != RunNone {
		t.Fatalf("monthly run on a yearly schedule = %+v, want None", got)
	}
	if got := DecideRun(lines, "", "2026"); got.Outcome != RunDue || !got.Line.Amount.Equal(d("5000")) {
		t.Fatalf("yearly run = %+v, want the 2026 line due", got)
	}
}

func straightLineSchedule(t *testing.T) []Line {
	t.Helper()
	lines, err := Schedule(Input{Cost: d("12000"), Life: 60, Method: MethodStraightLine,
		InService: time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC), Convention: ConventionMidMonth15, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

// Without adjustments the run charges exactly the scheduled amount (AC-37.1
// example: March 2026 is a half month of 100.00, then 200.00 a month).
func TestRunAmount_UnadjustedFollowsSchedule(t *testing.T) {
	lines := straightLineSchedule(t)
	acc := decimal.Zero
	for _, l := range lines {
		got := RunAmount(lines, l, d("12000"), decimal.Zero, acc, "USD")
		if !got.Equal(l.Amount) {
			t.Fatalf("period %s: run %s, schedule %s", l.Period, got, l.Amount)
		}
		acc = acc.Add(got)
	}
	if !acc.Equal(d("12000")) {
		t.Fatalf("total %s, want 12000", acc)
	}
}

// An acquisition adjustment of 1,180 after 2 periods (300 depreciated) is spread
// over the remaining periods prospectively (BR-37.4) in the schedule's own
// proportions (the last month is still a half month).
func TestRunAmount_AdjustmentAppliesProspectively(t *testing.T) {
	lines := straightLineSchedule(t)
	got := RunAmount(lines, lines[2], d("13180"), decimal.Zero, d("300"), "USD")
	// remaining scheduled from line 3 = 11,700; 12,880 × 200 / 11,700 = 220.17
	if !got.Equal(d("220.17")) {
		t.Fatalf("amount = %s, want 220.17", got)
	}
}

// Property: whatever adjustments happen along the way, posting every remaining
// line clears the depreciable basis exactly and never charges a negative amount.
func TestRunAmount_ClearsBasisExactly(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		life := rapid.IntRange(1, 120).Draw(rt, "life")
		cost := decimal.New(rapid.Int64Range(100, 10_000_000).Draw(rt, "cost"), -2)
		lines, err := Schedule(Input{Cost: cost, Life: life, Method: MethodStraightLine,
			InService: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Convention: ConventionFullMonth, Currency: "USD"})
		if err != nil {
			rt.Fatal(err)
		}
		acc := decimal.Zero
		for i, l := range lines {
			if rapid.Bool().Draw(rt, "adjust") {
				cost = cost.Add(decimal.New(rapid.Int64Range(0, 100_000).Draw(rt, "add"), -2))
			}
			amount := RunAmount(lines, l, cost, decimal.Zero, acc, "USD")
			if amount.IsNegative() || amount.Exponent() < -2 {
				rt.Fatalf("line %d: bad amount %s", i, amount)
			}
			acc = acc.Add(amount)
		}
		if !acc.Equal(cost) {
			rt.Fatalf("accumulated %s, want cost %s", acc, cost)
		}
	})
}

func TestRunAmount_NothingLeft(t *testing.T) {
	lines := straightLineSchedule(t)
	if got := RunAmount(lines, lines[5], d("1000"), d("200"), d("800"), "USD"); !got.IsZero() {
		t.Fatalf("amount = %s, want 0", got)
	}
}

func TestScheduledThrough(t *testing.T) {
	lines := straightLineSchedule(t)
	if got := ScheduledThrough(lines, ""); !got.IsZero() {
		t.Fatalf("nothing depreciated = %s", got)
	}
	if got := ScheduledThrough(lines, "2026-05"); !got.Equal(d("500")) {
		t.Fatalf("through 2026-05 = %s, want 500", got)
	}
}

func TestCheckDisposalDate_BR37_6(t *testing.T) {
	day := func(s string) time.Time { v, _ := time.Parse("2006-01-02", s); return v }
	if err := CheckDisposalDate(day("2026-03-10"), ""); err != nil {
		t.Fatalf("never depreciated: %v", err)
	}
	if err := CheckDisposalDate(day("2026-03-31"), "2026-03"); err != nil {
		t.Fatalf("last day of the depreciated month: %v", err)
	}
	if ErrorCode(CheckDisposalDate(day("2026-03-30"), "2026-03")) != CodeBackdated {
		t.Fatal("disposal inside a depreciated month accepted")
	}
	if ErrorCode(CheckDisposalDate(day("2026-06-30"), "2026")) != CodeBackdated {
		t.Fatal("disposal inside a depreciated year accepted")
	}
}
