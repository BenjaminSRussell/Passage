package money

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestRoundingByCurrency(t *testing.T) {
	cases := []struct{ in, cur, want string }{
		{"1.005", "USD", "1.01 USD"}, {"1.004", "USD", "1.00 USD"}, {"-1.005", "USD", "-1.01 USD"},
		{"100.4", "JPY", "100 JPY"}, {"100.5", "JPY", "101 JPY"}, {"1.2345", "KWD", "1.235 KWD"},
	}
	for _, c := range cases {
		if got := Must(c.in, c.cur).String(); got != c.want {
			t.Errorf("%s %s: got %s want %s", c.in, c.cur, got, c.want)
		}
	}
}

func TestAddMismatch(t *testing.T) {
	if _, err := Must("1", "USD").Add(Must("1", "EUR")); err != ErrCurrencyMismatch {
		t.Fatalf("expected mismatch, got %v", err)
	}
}

// Allocation must always sum back to the original amount, for any amount and any n.
func TestAllocateSumsExactly(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		cents := rapid.Int64Range(-1_000_000_000, 1_000_000_000).Draw(rt, "cents")
		n := rapid.IntRange(1, 50).Draw(rt, "n")
		cur := rapid.SampledFrom([]string{"USD", "JPY", "KWD"}).Draw(rt, "cur")
		a := Amount{Value: decimal.New(cents, -MinorUnits(cur)), Currency: cur}
		parts := a.Allocate(n)
		sum, err := Sum(cur, parts...)
		if err != nil {
			rt.Fatal(err)
		}
		if !sum.Value.Equal(a.Value) {
			rt.Fatalf("sum %s != %s (parts %v)", sum, a, parts)
		}
		for _, p := range parts {
			if !p.Value.Equal(p.Round().Value) {
				rt.Fatalf("part %s not at minor unit", p)
			}
		}
	})
}
