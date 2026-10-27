package domain

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func day(t *testing.T, s string) time.Time {
	t.Helper()
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDaysBetween_Table(t *testing.T) {
	cases := []struct {
		name       string
		start, end string
		dc         DayCount
		want       int
	}{
		{"act360 thirty days", "2026-01-01", "2026-01-31", DayCountACT360, 30},
		{"act365 thirty days", "2026-01-01", "2026-01-31", DayCountACT365, 30},
		{"act360 leap february", "2028-02-01", "2028-03-01", DayCountACT360, 29},
		{"act360 non-leap february", "2027-02-01", "2027-03-01", DayCountACT360, 28},
		{"30/360 month", "2026-01-01", "2026-02-01", DayCount30360, 30},
		{"30/360 day 31 start", "2026-01-31", "2026-03-01", DayCount30360, 31},
		{"30/360 full year", "2026-01-01", "2027-01-01", DayCount30360, 360},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := DaysBetween(day(t, tc.start), day(t, tc.end), tc.dc)
			if err != nil || got != tc.want {
				t.Fatalf("DaysBetween = %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestDaysBetween_RejectsBadInput(t *testing.T) {
	if _, err := DaysBetween(day(t, "2026-02-01"), day(t, "2026-01-01"), DayCountACT360); err == nil {
		t.Fatal("end before start accepted")
	}
	if _, err := DaysBetween(day(t, "2026-01-01"), day(t, "2026-02-01"), DayCount("ACT/ACT")); err == nil {
		t.Fatal("unknown day count accepted")
	}
}

// AC-36.15 worked example: EUR 1,000,000 at 3.00% ACT/360 for 30 days = 2,500.00.
func TestInterest_WorkedExample(t *testing.T) {
	got := Interest(decimal.NewFromInt(1000000), decimal.RequireFromString("0.03"), 30, DayCountACT360, "EUR")
	if got.StringFixed(2) != "2500.00" {
		t.Fatalf("interest = %s, want 2500.00", got.StringFixed(2))
	}
}

// AC-36.17 example: 200,000 at 4.00% ACT/360 for 31 days = 688.89.
func TestInterest_RoundsHalfUpToMinorUnit(t *testing.T) {
	got := Interest(decimal.NewFromInt(200000), decimal.RequireFromString("0.04"), 31, DayCountACT360, "EUR")
	if got.StringFixed(2) != "688.89" {
		t.Fatalf("interest = %s, want 688.89", got.StringFixed(2))
	}
}

func TestBuildSchedule_BulletWorkedExample(t *testing.T) {
	lines, err := BuildSchedule(LoanTerms{
		Principal: decimal.NewFromInt(1000000), Currency: "EUR",
		Start: day(t, "2026-01-01"), Maturity: day(t, "2026-02-01"),
		ScheduleType: ScheduleBullet, Rate: decimal.RequireFromString("0.03"),
		DayCount: DayCountACT360, PeriodMonths: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 {
		t.Fatalf("lines = %d, want 1", len(lines))
	}
	l := lines[0]
	if l.Days != 31 || !l.Interest.Equal(decimal.RequireFromString("2583.33")) || !l.PrincipalRepayment.Equal(decimal.NewFromInt(1000000)) {
		t.Fatalf("unexpected line %+v", l)
	}
}

func TestBuildSchedule_Annuity12Periods(t *testing.T) {
	lines, err := BuildSchedule(LoanTerms{
		Principal: decimal.NewFromInt(120000), Currency: "USD",
		Start: day(t, "2026-01-01"), Maturity: day(t, "2027-01-01"),
		ScheduleType: ScheduleAnnuity, Rate: decimal.RequireFromString("0.06"),
		DayCount: DayCount30360, PeriodMonths: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 12 {
		t.Fatalf("periods = %d, want 12", len(lines))
	}
	if !lines[len(lines)-1].ClosingPrincipal.IsZero() {
		t.Fatalf("final closing principal = %s, want 0", lines[len(lines)-1].ClosingPrincipal)
	}
}

// Property: for every valid term set the schedule sums to the principal to
// the cent, the balance rolls forward, and each period is non-negative.
func TestBuildSchedule_Properties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		principal := decimal.NewFromInt(rapid.Int64Range(1000, 5000000).Draw(t, "principal"))
		rateBP := rapid.Int64Range(0, 1200).Draw(t, "rateBP")
		periodMonths := rapid.SampledFrom([]int{1, 3, 6, 12}).Draw(t, "period")
		periods := rapid.IntRange(1, 24).Draw(t, "periods")
		schedule := rapid.SampledFrom([]ScheduleType{ScheduleAnnuity, ScheduleLinear, ScheduleBullet}).Draw(t, "schedule")
		dc := rapid.SampledFrom([]DayCount{DayCountACT360, DayCountACT365, DayCount30360}).Draw(t, "dc")
		// Any start day, including month ends, so the clamped period ends are exercised.
		month := time.Month(rapid.IntRange(1, 12).Draw(t, "month"))
		first := time.Date(2026, month, 1, 0, 0, 0, 0, time.UTC)
		lastDay := first.AddDate(0, 1, -1).Day()
		start := first.AddDate(0, 0, rapid.IntRange(1, lastDay).Draw(t, "day")-1)
		maturity := AddMonths(start, periods*periodMonths)

		lines, err := BuildSchedule(LoanTerms{
			Principal: principal, Currency: "EUR", Start: start, Maturity: maturity,
			ScheduleType: schedule, Rate: decimal.NewFromInt(rateBP).Div(decimal.NewFromInt(10000)),
			DayCount: dc, PeriodMonths: periodMonths,
		})
		if err != nil {
			t.Fatalf("BuildSchedule: %v", err)
		}
		if len(lines) != periods {
			t.Fatalf("lines = %d, want %d", len(lines), periods)
		}
		repaid := decimal.Zero
		opening := principal
		for _, l := range lines {
			if !l.OpeningPrincipal.Equal(opening) {
				t.Fatalf("period %d opening %s, want %s", l.PeriodNo, l.OpeningPrincipal, opening)
			}
			if l.Interest.IsNegative() || l.PrincipalRepayment.IsNegative() {
				t.Fatalf("period %d has a negative amount: %+v", l.PeriodNo, l)
			}
			if !l.OpeningPrincipal.Sub(l.PrincipalRepayment).Equal(l.ClosingPrincipal) {
				t.Fatalf("period %d does not roll forward", l.PeriodNo)
			}
			repaid = repaid.Add(l.PrincipalRepayment)
			opening = l.ClosingPrincipal
		}
		if !repaid.Equal(principal) {
			t.Fatalf("Σ principal = %s, want %s", repaid, principal)
		}
		if !opening.IsZero() {
			t.Fatalf("closing balance = %s, want 0", opening)
		}
	})
}

func TestBuildSchedule_RejectsBadTerms(t *testing.T) {
	base := LoanTerms{
		Principal: decimal.NewFromInt(1000), Currency: "EUR",
		Start: day(t, "2026-01-01"), Maturity: day(t, "2026-07-01"),
		ScheduleType: ScheduleLinear, Rate: decimal.RequireFromString("0.02"),
		DayCount: DayCountACT360, PeriodMonths: 3,
	}
	cases := []struct {
		name   string
		mutate func(*LoanTerms)
	}{
		{"zero principal", func(l *LoanTerms) { l.Principal = decimal.Zero }},
		{"negative rate", func(l *LoanTerms) { l.Rate = decimal.RequireFromString("-0.01") }},
		{"unknown schedule", func(l *LoanTerms) { l.ScheduleType = "Balloon" }},
		{"maturity before start", func(l *LoanTerms) { l.Maturity = day(t, "2025-01-01") }},
		{"partial period", func(l *LoanTerms) { l.Maturity = day(t, "2026-05-01") }},
		{"period 2 months", func(l *LoanTerms) { l.PeriodMonths = 2 }},
	}
	for _, tc := range cases {
		name, mutate := tc.name, tc.mutate
		t.Run(name, func(t *testing.T) {
			l := base
			mutate(&l)
			_, err := BuildSchedule(l)
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeLoanTerms {
				t.Fatalf("error = %v, want %s", err, CodeLoanTerms)
			}
		})
	}
}

func TestAddMonths_ClampsToMonthEnd(t *testing.T) {
	cases := []struct {
		from string
		n    int
		want string
	}{
		{"2026-01-31", 1, "2026-02-28"},
		{"2028-01-31", 1, "2028-02-29"},
		{"2026-01-31", 3, "2026-04-30"},
		{"2026-01-15", 1, "2026-02-15"},
		{"2026-11-30", 3, "2027-02-28"},
		{"2026-08-31", 12, "2027-08-31"},
	}
	for _, tc := range cases {
		if got := AddMonths(day(t, tc.from), tc.n).Format("2006-01-02"); got != tc.want {
			t.Fatalf("AddMonths(%s, %d) = %s, want %s", tc.from, tc.n, got, tc.want)
		}
	}
}

// A loan starting at month end keeps month-end periods instead of drifting into the next month.
func TestBuildSchedule_MonthEndStart(t *testing.T) {
	lines, err := BuildSchedule(LoanTerms{
		Principal: decimal.NewFromInt(300000), Currency: "EUR",
		Start: day(t, "2026-01-31"), Maturity: day(t, "2026-04-30"),
		ScheduleType: ScheduleLinear, Rate: decimal.RequireFromString("0.03"),
		DayCount: DayCountACT360, PeriodMonths: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	var ends []string
	for _, l := range lines {
		ends = append(ends, l.PeriodEnd)
	}
	if got := strings.Join(ends, ","); got != "2026-02-28,2026-03-31,2026-04-30" {
		t.Fatalf("period ends = %s", got)
	}
	// 300,000 x 3% x 28 / 360 = 700.00 for February.
	if lines[0].Days != 28 || lines[0].Interest.StringFixed(2) != "700.00" || lines[0].PrincipalRepayment.StringFixed(2) != "100000.00" {
		t.Fatalf("first period = %+v", lines[0])
	}
}

// A linear schedule on a tiny principal rounds instalments up; the balance never goes negative.
func TestBuildSchedule_LinearNeverOverpays(t *testing.T) {
	lines, err := BuildSchedule(LoanTerms{
		Principal: decimal.RequireFromString("0.18"), Currency: "EUR",
		Start: day(t, "2026-01-01"), Maturity: day(t, "2027-01-01"),
		ScheduleType: ScheduleLinear, Rate: decimal.Zero,
		DayCount: DayCountACT360, PeriodMonths: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if l.ClosingPrincipal.IsNegative() || l.PrincipalRepayment.IsNegative() {
			t.Fatalf("period %d went negative: %+v", l.PeriodNo, l)
		}
	}
	if !lines[len(lines)-1].ClosingPrincipal.IsZero() {
		t.Fatalf("final balance = %s", lines[len(lines)-1].ClosingPrincipal)
	}
}
