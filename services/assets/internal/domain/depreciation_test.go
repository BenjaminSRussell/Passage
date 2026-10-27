package domain

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func date(y int, m time.Month, day int) time.Time { return time.Date(y, m, day, 0, 0, 0, 0, time.UTC) }

// amountsOf returns the rounded string of each line amount.
func amountsOf(lines []Line) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.Amount.StringFixed(2)
	}
	return out
}

func TestSchedule_StraightLineMidMonthWorkedExample(t *testing.T) {
	// FR-37.3 / AC-37.1: USD 12,000, 5 years, in service 2026-03-10, mid-month 15th.
	lines, err := Schedule(Input{Cost: d("12000"), Salvage: d("0"), Life: 60, Method: MethodStraightLine,
		InService: date(2026, time.March, 10), Convention: ConventionMidMonth15, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	fy := map[string]decimal.Decimal{}
	for _, l := range lines {
		fy[l.Period[:4]] = fy[l.Period[:4]].Add(l.Amount)
	}
	want := map[string]string{"2026": "1900.00", "2027": "2400.00", "2028": "2400.00", "2029": "2400.00", "2030": "2400.00", "2031": "500.00"}
	for year, amount := range want {
		if got := fy[year].StringFixed(2); got != amount {
			t.Errorf("FY%s = %s, want %s", year, got, amount)
		}
	}
	if first := lines[0]; first.Period != "2026-03" || first.Amount.StringFixed(2) != "100.00" {
		t.Errorf("first line = %s %s, want 2026-03 100.00", first.Period, first.Amount.StringFixed(2))
	}
	if total := TotalAmount(lines); !total.Equal(d("12000")) {
		t.Errorf("total = %s, want 12000", total)
	}
}

func TestSchedule_MACRSFiveYearHalfYear(t *testing.T) {
	// FR-37.4 / AC-37.2: USD 25,000, 5-year GDS, half-year convention, 200% DB with switch.
	lines, err := Schedule(Input{Cost: d("25000"), Life: 5, Method: MethodDecliningBalance, Factor: d("2"),
		SwitchToStraightLine: true, InService: date(2026, time.April, 1), Convention: ConventionHalfYear, Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"5000.00", "8000.00", "4800.00", "2880.00", "2880.00", "1440.00"}
	if got := amountsOf(lines); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("amounts = %v, want %v", got, want)
	}
	percents := []string{"20.00", "32.00", "19.20", "11.52", "11.52", "5.76"}
	for i, l := range lines {
		pct := l.Amount.Mul(d("100")).Div(d("25000")).StringFixed(2)
		if pct != percents[i] {
			t.Errorf("year %d = %s%%, want %s%%", i+1, pct, percents[i])
		}
	}
	if lines[5].Period != "2031" {
		t.Errorf("last period = %s, want 2031", lines[5].Period)
	}
	if !lines[5].NBV.IsZero() {
		t.Errorf("final NBV = %s, want 0", lines[5].NBV)
	}
}

func TestSchedule_ErrorsAreCoded(t *testing.T) {
	cases := []struct {
		name string
		in   Input
		code string
	}{
		{"salvage above cost", Input{Cost: d("10"), Salvage: d("11"), Life: 1, Method: MethodStraightLine, Currency: "USD", Convention: ConventionNone}, CodeInvalidDepreciation},
		{"zero life", Input{Cost: d("10"), Life: 0, Method: MethodStraightLine, Currency: "USD", Convention: ConventionNone}, CodeInvalidDepreciation},
		{"half-year on monthly", Input{Cost: d("10"), Life: 12, Method: MethodStraightLine, Currency: "USD", Convention: ConventionHalfYear}, CodeInvalidDepreciation},
		{"bad factor", Input{Cost: d("10"), Life: 5, Method: MethodDecliningBalance, Factor: d("4"), Currency: "USD", Convention: ConventionNone}, CodeInvalidDepreciation},
		{"mid-month 1st not built", Input{Cost: d("10"), Life: 12, Method: MethodStraightLine, Currency: "USD", Convention: "MidMonth1"}, CodeInvalidDepreciation},
		{"bad currency", Input{Cost: d("10"), Life: 5, Method: MethodStraightLine, Currency: "US", Convention: ConventionNone}, CodeInvalidDepreciation},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Schedule(c.in)
			de, ok := err.(*Error)
			if !ok || de.Code != c.code {
				t.Fatalf("err = %v, want code %s", err, c.code)
			}
		})
	}
}

func TestSchedule_LifeTotalsEqualDepreciableBasis(t *testing.T) {
	// Property: Σ depreciation = cost − salvage, for random inputs, both methods.
	rapid.Check(t, func(t *rapid.T) {
		cost := decimal.New(rapid.Int64Range(1, 100_000_000).Draw(t, "cents"), -2)
		salvage := cost.Mul(decimal.New(rapid.Int64Range(0, 50).Draw(t, "salvagePct"), -2))
		salvage = salvage.Round(2)
		month := rapid.IntRange(1, 12).Draw(t, "month")
		in := Input{Cost: cost, Salvage: salvage, InService: date(2026, time.Month(month), 1), Currency: "USD"}
		if rapid.Bool().Draw(t, "sl") {
			in.Method = MethodStraightLine
			in.Life = rapid.IntRange(1, 240).Draw(t, "months")
			in.Convention = rapid.SampledFrom([]Convention{ConventionNone, ConventionFullMonth, ConventionMidMonth15}).Draw(t, "conv")
		} else {
			in.Method = MethodDecliningBalance
			in.Life = rapid.IntRange(1, 20).Draw(t, "years")
			in.Factor = d(rapid.SampledFrom([]string{"1.25", "1.5", "1.75", "2"}).Draw(t, "factor"))
			in.SwitchToStraightLine = rapid.Bool().Draw(t, "switch")
			in.Convention = rapid.SampledFrom([]Convention{ConventionNone, ConventionHalfYear, ConventionMidQuarter}).Draw(t, "dbconv")
		}
		lines, err := Schedule(in)
		if err != nil {
			t.Fatal(err)
		}
		if total := TotalAmount(lines); !total.Equal(cost.Sub(salvage)) {
			t.Fatalf("total %s != basis %s", total, cost.Sub(salvage))
		}
		for _, l := range lines {
			if l.Amount.IsNegative() || l.Amount.Exponent() < -2 {
				t.Fatalf("line %d has amount %s", l.Index, l.Amount)
			}
		}
		if last := lines[len(lines)-1]; !last.NBV.Equal(salvage) {
			t.Fatalf("final NBV %s != salvage %s", last.NBV, salvage)
		}
	})
}
