package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestCheckFixedCount(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		demand     string
		leadTimeH  int
		safety     string
		fixed      string
		suggested  int64
		deviation  string
		reportable bool
	}{
		// ⌈10 × 2 days × 1.1 / 20⌉ = ⌈1.1⌉ = 2 against 5 configured: 60 %.
		{"far fewer cards needed", 5, "10", 48, "10", "20", 2, "60", true},
		// ⌈10 × 1 × 1 / 20⌉ = 1 against 1: no deviation.
		{"matches", 1, "10", 24, "0", "20", 1, "0", false},
		// ⌈30 × 1 / 10⌉ = 3 against 4: exactly 25 % is not above the limit.
		{"exactly at the limit", 4, "30", 24, "0", "10", 3, "25", false},
		// ⌈40 × 1 / 10⌉ = 4 against 3: 33.33 %.
		{"more cards needed, rounded to two places", 3, "40", 24, "0", "10", 4, "33.33", true},
		// 12 h is half a day: ⌈100 × 0.5 / 10⌉ = 5 against 5.
		{"lead time in hours becomes days", 5, "100", 12, "0", "10", 5, "0", false},
		// No demand needs no cards: 0 against 2 is a 100 % deviation.
		{"no demand", 2, "0", 24, "0", "10", 0, "100", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CheckFixedCount(tc.configured, dec(tc.demand), tc.leadTimeH, dec(tc.safety), dec(tc.fixed))
			if err != nil {
				t.Fatal(err)
			}
			if got.Configured != tc.configured || got.Suggested != tc.suggested ||
				!got.DeviationPct.Equal(dec(tc.deviation)) || got.Reportable != tc.reportable {
				t.Fatalf("got %+v (deviation %s), want suggested %d deviation %s reportable %v",
					got, got.DeviationPct, tc.suggested, tc.deviation, tc.reportable)
			}
		})
	}
}

func TestCheckFixedCount_ZeroConfigured(t *testing.T) {
	got, err := CheckFixedCount(0, dec("10"), 24, dec("0"), dec("10"))
	if err != nil || got.Suggested != 1 || !got.DeviationPct.Equal(dec("100")) || !got.Reportable {
		t.Fatalf("got %+v %v", got, err)
	}
	got, err = CheckFixedCount(0, dec("0"), 24, dec("0"), dec("10"))
	if err != nil || got.Suggested != 0 || !got.DeviationPct.IsZero() || got.Reportable {
		t.Fatalf("nothing configured, nothing suggested: %+v %v", got, err)
	}
}

func TestCheckFixedCount_Refusals(t *testing.T) {
	cases := map[string]func() (CountCheck, error){
		"negative demand":     func() (CountCheck, error) { return CheckFixedCount(1, dec("-1"), 24, dec("0"), dec("10")) },
		"negative safety":     func() (CountCheck, error) { return CheckFixedCount(1, dec("1"), 24, dec("-5"), dec("10")) },
		"zero fixed quantity": func() (CountCheck, error) { return CheckFixedCount(1, dec("1"), 24, dec("0"), dec("0")) },
		"negative lead time":  func() (CountCheck, error) { return CheckFixedCount(1, dec("1"), -1, dec("0"), dec("10")) },
		"negative configured": func() (CountCheck, error) { return CheckFixedCount(-1, dec("1"), 24, dec("0"), dec("10")) },
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := run()
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeInvalid {
				t.Fatalf("err = %v, want %s", err, CodeInvalid)
			}
		})
	}
}

// The check is reported exactly when the suggestion is more than 25 % away
// from the configured count, whatever rounding the displayed figure has.
func TestCheckFixedCount_ReportableProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		configured := rapid.IntRange(1, MaxKanbansPerRule).Draw(t, "configured")
		demand := decimal.New(rapid.Int64Range(0, 1e6).Draw(t, "demand"), -2)
		leadH := rapid.IntRange(0, 24*30).Draw(t, "leadH")
		safety := decimal.NewFromInt(rapid.Int64Range(0, 100).Draw(t, "safety"))
		fixed := decimal.NewFromInt(rapid.Int64Range(1, 1000).Draw(t, "fixed"))
		got, err := CheckFixedCount(configured, demand, leadH, safety, fixed)
		if err != nil {
			t.Fatal(err)
		}
		exact := decimal.NewFromInt(got.Suggested - int64(configured)).Abs().
			Mul(decimal.NewFromInt(100)).Div(decimal.NewFromInt(int64(configured)))
		if got.Reportable != exact.GreaterThan(decimal.NewFromInt(CountDeviationLimitPct)) {
			t.Fatalf("reportable %v for exact deviation %s", got.Reportable, exact)
		}
		if !got.DeviationPct.Equal(exact.Round(2)) {
			t.Fatalf("deviation %s, want %s", got.DeviationPct, exact.Round(2))
		}
	})
}
