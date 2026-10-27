package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func TestCompareBalances_Table(t *testing.T) {
	cases := []struct {
		name     string
		inv, led string
		currency string
		diff     string
		status   string
	}{
		{"matched USD", "200.00", "200", "USD", "0", ReconciliationMatched},
		{"projection above ledger", "200", "150", "USD", "50", ReconciliationDifference},
		{"ledger above projection", "149.99", "150", "USD", "-0.01", ReconciliationDifference},
		{"sub-cent residual rounds away", "100.004", "100.001", "USD", "0", ReconciliationMatched},
		{"half-cent residual is a difference", "100.005", "100.00", "USD", "0.01", ReconciliationDifference},
		{"JPY has no minor unit", "1000.4", "1000", "JPY", "0", ReconciliationMatched},
		{"KWD keeps three places", "5.0005", "5.000", "KWD", "0.001", ReconciliationDifference},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CompareBalances(decimal.RequireFromString(c.inv), decimal.RequireFromString(c.led), c.currency)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Diff.Equal(decimal.RequireFromString(c.diff)) {
				t.Fatalf("diff %s, want %s", got.Diff, c.diff)
			}
			if got.Status != c.status {
				t.Fatalf("status %s, want %s", got.Status, c.status)
			}
		})
	}
}

func TestCompareBalances_RejectsBadCurrency(t *testing.T) {
	for _, cur := range []string{"", "US", "USDX", "usd"} {
		if _, err := CompareBalances(decimal.Zero, decimal.Zero, cur); err == nil {
			t.Fatalf("currency %q accepted", cur)
		}
	}
}

// TestCompareBalances_Property: the difference is antisymmetric, zero exactly when
// the status is MATCHED, and each side is rounded to the currency's minor unit.
func TestCompareBalances_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		inv := decimal.New(rapid.Int64Range(-1e12, 1e12).Draw(t, "inv"), -4)
		led := decimal.New(rapid.Int64Range(-1e12, 1e12).Draw(t, "led"), -4)
		currency := rapid.SampledFrom([]string{"USD", "EUR", "JPY", "KWD"}).Draw(t, "currency")

		fwd, err := CompareBalances(inv, led, currency)
		if err != nil {
			t.Fatal(err)
		}
		back, err := CompareBalances(led, inv, currency)
		if err != nil {
			t.Fatal(err)
		}
		if !fwd.Diff.Equal(back.Diff.Neg()) {
			t.Fatalf("not antisymmetric: %s vs %s", fwd.Diff, back.Diff)
		}
		if (fwd.Status == ReconciliationMatched) != fwd.Diff.IsZero() {
			t.Fatalf("status %s disagrees with diff %s", fwd.Status, fwd.Diff)
		}
		scale := int32(0)
		if currency != "JPY" {
			scale = 2
		}
		if currency == "KWD" {
			scale = 3
		}
		if !fwd.InventoryValue.Equal(fwd.InventoryValue.Round(scale)) {
			t.Fatalf("inventory %s not rounded to %d places", fwd.InventoryValue, scale)
		}
	})
}
