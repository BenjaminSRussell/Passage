package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestMidQuarterFirstYear_ShareOfYear(t *testing.T) {
	cases := []struct {
		in   time.Time
		want string
	}{
		{date(2026, time.January, 1), "0.875"},
		{date(2026, time.March, 31), "0.875"},
		{date(2026, time.April, 1), "0.625"},
		{date(2026, time.September, 30), "0.375"},
		{date(2026, time.October, 1), "0.125"},
		{date(2026, time.December, 31), "0.125"},
	}
	for _, tc := range cases {
		if got := MidQuarterFirstYear(tc.in); !got.Equal(d(tc.want)) {
			t.Errorf("MidQuarterFirstYear(%s) = %s, want %s", tc.in.Format("2006-01-02"), got, tc.want)
		}
	}
}

// TestSchedule_MidQuarterMatchesPub946 checks the 5-year GDS (200% declining
// balance, switch to straight line) mid-quarter rows of IRS Pub. 946 Tables
// A-2 (Q1) to A-5 (Q4). The tables print percentages rounded to two decimals;
// every yearly amount of a USD 25,000 asset rounds to the published rate and
// the six years sum to the cost exactly.
func TestSchedule_MidQuarterMatchesPub946(t *testing.T) {
	tables := []struct {
		name  string
		in    time.Time
		rates []string
		first string
	}{
		{"A-2 first quarter", date(2026, time.February, 10), []string{"35.00", "26.00", "15.60", "11.01", "11.01", "1.38"}, "8750.00"},
		{"A-3 second quarter", date(2026, time.May, 1), []string{"25.00", "30.00", "18.00", "11.37", "11.37", "4.26"}, "6250.00"},
		{"A-4 third quarter", date(2026, time.August, 31), []string{"15.00", "34.00", "20.40", "12.24", "11.30", "7.06"}, "3750.00"},
		{"A-5 fourth quarter", date(2026, time.November, 5), []string{"5.00", "38.00", "22.80", "13.68", "10.94", "9.58"}, "1250.00"},
	}
	cost := d("25000")
	for _, tc := range tables {
		t.Run(tc.name, func(t *testing.T) {
			lines, err := Schedule(Input{Cost: cost, Salvage: decimal.Zero, Life: 5, Method: MethodDecliningBalance, Factor: d("2"),
				SwitchToStraightLine: true, InService: tc.in, Convention: ConventionMidQuarter, Currency: "USD"})
			if err != nil {
				t.Fatal(err)
			}
			if len(lines) != len(tc.rates) {
				t.Fatalf("got %d years, want %d", len(lines), len(tc.rates))
			}
			if got := lines[0].Amount.StringFixed(2); got != tc.first {
				t.Errorf("first year = %s, want %s", got, tc.first)
			}
			for i, rate := range tc.rates {
				if got := lines[i].Amount.Div(cost).Mul(decimal.NewFromInt(100)).StringFixed(2); got != rate {
					t.Errorf("year %d rate = %s%%, want %s%% (amount %s)", i+1, got, rate, lines[i].Amount)
				}
				if want := time.Date(2026+i, 1, 1, 0, 0, 0, 0, time.UTC).Format("2006"); lines[i].Period != want {
					t.Errorf("year %d period = %s, want %s", i+1, lines[i].Period, want)
				}
			}
			if total := TotalAmount(lines); !total.Equal(cost) {
				t.Errorf("total = %s, want %s", total, cost)
			}
		})
	}
}

func TestSchedule_MidQuarterRejectedOnStraightLine(t *testing.T) {
	_, err := Schedule(Input{Cost: d("1200"), Life: 12, Method: MethodStraightLine, InService: date(2026, time.February, 10),
		Convention: ConventionMidQuarter, Currency: "USD"})
	if ErrorCode(err) != CodeInvalidDepreciation {
		t.Fatalf("err = %v, want %s", err, CodeInvalidDepreciation)
	}
}

func TestMidQuarterTest_BR37_5(t *testing.T) {
	at := func(y int, m time.Month, basis string) YearAddition {
		return YearAddition{InService: date(y, m, 15), Basis: d(basis)}
	}
	cases := []struct {
		name      string
		additions []YearAddition
		applies   bool
		share     string
		total     string
	}{
		{"no additions", nil, false, "0", "0"},
		{"Q4 share 70% applies", []YearAddition{at(2026, time.February, "30"), at(2026, time.November, "70")}, true, "0.7", "100"},
		{"exactly 40% does not apply", []YearAddition{at(2026, time.February, "60"), at(2026, time.December, "40")}, false, "0.4", "100"},
		{"just above 40% applies", []YearAddition{at(2026, time.February, "59.99"), at(2026, time.December, "40.01")}, true, "0.4001", "100"},
		{"other years are ignored", []YearAddition{at(2025, time.December, "900"), at(2027, time.October, "900"), at(2026, time.February, "100")}, false, "0", "100"},
		{"only Q4 additions", []YearAddition{at(2026, time.October, "10"), at(2026, time.December, "5")}, true, "1", "15"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := MidQuarterTest(2026, tc.additions)
			if res.Year != 2026 || res.Applies != tc.applies {
				t.Fatalf("year %d applies %v, want 2026 %v", res.Year, res.Applies, tc.applies)
			}
			if !res.Q4Share.Equal(d(tc.share)) || !res.TotalBasis.Equal(d(tc.total)) {
				t.Errorf("share %s total %s, want %s %s", res.Q4Share, res.TotalBasis, tc.share, tc.total)
			}
			sum := decimal.Zero
			for _, q := range res.QuarterBasis {
				sum = sum.Add(q)
			}
			if !sum.Equal(res.TotalBasis) {
				t.Errorf("Σ quarters %s != total %s", sum, res.TotalBasis)
			}
			if tc.applies != (len(res.Warnings) == 1 && res.Warnings[0] == CodeMidQuarterApplies) {
				t.Errorf("warnings = %v, want AST-0511 only when it applies", res.Warnings)
			}
		})
	}
}
