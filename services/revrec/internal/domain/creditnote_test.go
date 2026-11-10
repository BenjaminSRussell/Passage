package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestPlanReversal_UnrecognizedFirstThenRecognized(t *testing.T) {
	for _, tc := range []struct {
		share, unrecognized, recognized, reduced, reversed string
		ok                                                 bool
	}{
		{"1125.00", "1932.01", "317.99", "1125.00", "0.00", true}, // a partial credit inside what is still deferred
		{"1125.00", "730.00", "395.00", "730.00", "395.00", true}, // the rest of the credit reverses recognized revenue
		{"1350.00", "0.00", "1350.00", "0.00", "1350.00", true},   // a satisfied milestone is reversed whole
		{"1350.01", "0.00", "1350.00", "0.00", "1350.01", false},  // more than the obligation holds
		{"0.00", "100.00", "0.00", "0.00", "0.00", true},
	} {
		plan, ok := PlanReversal(dec(tc.share), dec(tc.unrecognized), dec(tc.recognized))
		if ok != tc.ok || !plan.Reduced.Equal(dec(tc.reduced)) || !plan.Reversed.Equal(dec(tc.reversed)) {
			t.Errorf("PlanReversal(%s, %s, %s) = %+v %v, want reduced %s reversed %s ok %v", tc.share, tc.unrecognized, tc.recognized,
				plan, ok, tc.reduced, tc.reversed, tc.ok)
		}
	}
}

func TestApportion_SplitsByWeightToTheCent(t *testing.T) {
	got := Apportion(dec("100.00"), []decimal.Decimal{dec("1"), dec("1"), dec("1")}, 2)
	if got[0].StringFixed(2) != "33.34" || got[1].StringFixed(2) != "33.33" || got[2].StringFixed(2) != "33.33" {
		t.Errorf("Apportion 100.00 in thirds = %v, want 33.34 / 33.33 / 33.33", got)
	}
	if zero := Apportion(dec("5.00"), []decimal.Decimal{decimal.Zero}, 2); !zero[0].IsZero() {
		t.Errorf("Apportion over zero weight = %v, want 0", zero)
	}
}

// TestReduceLines_Properties: the reductions sum to what is taken off, never take a line below zero, are at the minor unit,
// and taking everything zeroes every line.
func TestReduceLines_Properties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		cents := rapid.SliceOfN(rapid.Int64Range(0, 500_000), 1, 14).Draw(t, "lines")
		amounts := make([]decimal.Decimal, len(cents))
		total := int64(0)
		for i, c := range cents {
			amounts[i], total = decimal.New(c, -2), total+c
		}
		cut := rapid.Int64Range(0, total).Draw(t, "cut")
		parts := ReduceLines(amounts, decimal.New(cut, -2), 2)
		sum := decimal.Zero
		for i, p := range parts {
			if p.IsNegative() || p.GreaterThan(amounts[i]) || !p.Equal(p.Round(2)) {
				t.Fatalf("line %d: reduction %s of %s", i, p, amounts[i])
			}
			sum = sum.Add(p)
		}
		if !sum.Equal(decimal.New(cut, -2)) {
			t.Fatalf("reductions %v sum to %s, want %s", parts, sum, decimal.New(cut, -2))
		}
		if cut == total {
			for i, p := range parts {
				if !p.Equal(amounts[i]) {
					t.Fatalf("taking everything left %s on line %d", amounts[i].Sub(p), i)
				}
			}
		}
	})
}

func TestRollForward_WithCredits(t *testing.T) {
	rf := NewRollForward(dec("2116.94"), dec("0"), dec("184.93")).WithCredits(dec("395.00"), dec("-2475.00"))
	if want := dec("2116.94").Sub(dec("184.93")).Add(dec("395.00")).Sub(dec("2475.00")); !rf.Closing.Equal(want) {
		t.Errorf("closing %s, want opening − recognized + reversed + adjustments = %s", rf.Closing, want)
	}
}
