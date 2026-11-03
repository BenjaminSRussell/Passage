package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestSchedule(t *testing.T) {
	cases := []struct {
		name  string
		freq  string
		dom   int
		start string
		first string
		next  string
	}{
		{"monthly mid", FreqMonthly, 15, "2026-01-10", "2026-01-15", "2026-02-15"},
		{"monthly start after day", FreqMonthly, 15, "2026-01-20", "2026-02-15", "2026-03-15"},
		{"monthly last day", FreqMonthly, LastDay, "2026-01-31", "2026-01-31", "2026-02-28"},
		{"day 31 clips", FreqMonthly, 31, "2026-04-01", "2026-04-30", "2026-05-31"},
		{"leap february", FreqMonthly, 30, "2028-02-01", "2028-02-29", "2028-03-30"},
		{"quarterly", FreqQuarterly, LastDay, "2026-11-05", "2026-11-30", "2027-02-28"},
		{"yearly", FreqYearly, 1, "2026-03-01", "2026-03-01", "2027-03-01"},
		{"december rolls", FreqMonthly, 10, "2026-12-11", "2027-01-10", "2027-02-10"},
	}
	for _, c := range cases {
		first := FirstRun(c.freq, c.dom, day(c.start))
		if first.Format(time.DateOnly) != c.first {
			t.Errorf("%s: first %s, want %s", c.name, first.Format(time.DateOnly), c.first)
		}
		if next := RunAfter(c.freq, c.dom, day(c.start), first); next.Format(time.DateOnly) != c.next {
			t.Errorf("%s: next %s, want %s", c.name, next.Format(time.DateOnly), c.next)
		}
	}
}

// Quarterly and yearly schedules keep the month of the start date as their
// anchor, whatever date the next run is computed from (a resume, a catch-up).
func TestSchedule_KeepsAnchorMonth(t *testing.T) {
	start := day("2026-02-10") // quarterly on the 20th: Feb, May, Aug, Nov
	cases := []struct {
		from, want string
	}{
		{"2026-02-10", "2026-02-20"},
		{"2026-02-21", "2026-05-20"},
		{"2026-06-01", "2026-08-20"}, // a resume in June does not move the anchor to June
		{"2026-08-20", "2026-08-20"},
		{"2027-01-15", "2027-02-20"},
		{"2025-01-01", "2026-02-20"}, // before the start counts from the start
	}
	for _, c := range cases {
		if got := RunOnOrAfter(FreqQuarterly, 20, start, day(c.from)); got.Format(time.DateOnly) != c.want {
			t.Errorf("quarterly from %s: %s, want %s", c.from, got.Format(time.DateOnly), c.want)
		}
	}
	if got := RunOnOrAfter(FreqYearly, LastDay, day("2028-02-01"), day("2029-01-01")); got.Format(time.DateOnly) != "2029-02-28" {
		t.Errorf("yearly last day after a leap year: %s", got.Format(time.DateOnly))
	}
}

func TestOnSchedule(t *testing.T) {
	start := day("2026-04-01")
	for d, want := range map[string]bool{
		"2026-04-30": true,  // monthly last day
		"2026-02-28": false, // before the start
		"2026-05-31": true,
		"2026-05-30": false, // a day that is not the run day
		"2026-06-30": true,
	} {
		if got := OnSchedule(FreqMonthly, LastDay, start, day(d)); got != want {
			t.Errorf("monthly last day %s: %v, want %v", d, got, want)
		}
	}
	// Quarterly from April: July is on the schedule, June is not.
	if !OnSchedule(FreqQuarterly, 15, start, day("2026-07-15")) || OnSchedule(FreqQuarterly, 15, start, day("2026-06-15")) {
		t.Error("quarterly schedule from April must hold July 15 and not June 15")
	}
}

func TestResolveAmounts(t *testing.T) {
	base := decimal.RequireFromString("1000.00")
	lines := []RecurringLine{
		{Account: "6100", DebitCredit: SideDebit, Kind: AmountPercent, Amount: "33.333", Currency: "EUR"},
		{Account: "2100", DebitCredit: SideCredit, Kind: AmountFixed, Amount: "12.5", Currency: "EUR"},
		{Account: "6200", DebitCredit: SideDebit, Kind: AmountPercent, Amount: "50", Currency: "JPY"},
	}
	got, err := ResolveAmounts(lines, &base)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"333.33", "12.50", "500"}
	for i, w := range want {
		if got[i].Resolved != w {
			t.Errorf("line %d: %s, want %s", i+1, got[i].Resolved, w)
		}
	}
	if _, err := ResolveAmounts(lines, nil); err != ErrNoBase {
		t.Errorf("percent without base: %v", err)
	}
	fixedOnly, err := ResolveAmounts(lines[1:2], nil)
	if err != nil || fixedOnly[0].Resolved != "12.50" {
		t.Errorf("fixed without base: %+v %v", fixedOnly, err)
	}
}

func TestResolveAmounts_CarriesTheStatisticalQuantityUnscaled(t *testing.T) {
	base := decimal.RequireFromString("1000.00")
	got, err := ResolveAmounts([]RecurringLine{
		{Account: "6100", DebitCredit: SideDebit, Kind: AmountPercent, Amount: "10", Currency: "EUR", Quantity: "12.500000", Unit: "KG"},
		{Account: "6100", DebitCredit: SideDebit, Kind: AmountFixed, Amount: "0", Currency: "EUR", Quantity: "3.000000", Unit: "HC"},
	}, &base)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Resolved != "100.00" || got[0].Quantity != "12.500000" || got[0].Unit != "KG" {
		t.Errorf("percent line: %+v", got[0])
	}
	if got[1].Resolved != "0.00" || got[1].Quantity != "3.000000" || got[1].Unit != "HC" {
		t.Errorf("statistical line: %+v", got[1])
	}
}
