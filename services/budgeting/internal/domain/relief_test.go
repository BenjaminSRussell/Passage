package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// AC-38.3: PO 10 x 50.00 encumbered; invoice 10 x 52.00 relieves 500.00 and checks 20.00.
func TestRelief_AC383PriceVariance(t *testing.T) {
	reserved := d("500")
	matched := d("10").Mul(d("50")) // invoice quantity x PO unit price
	relief := Relief(reserved, decimal.Zero, matched)
	if !relief.Equal(d("500")) {
		t.Fatalf("relief = %s, want 500", relief)
	}
	if got := Excess(d("10").Mul(d("52")), relief); !got.Equal(d("20")) {
		t.Fatalf("excess = %s, want 20", got)
	}
}

func TestRelief_Table(t *testing.T) {
	tests := []struct{ reserved, relieved, matched, want string }{
		{"500", "0", "300", "300"},   // partial invoice
		{"500", "300", "300", "200"}, // capped by remaining reservation
		{"500", "500", "100", "0"},   // fully relieved
		{"500", "0", "0", "0"},       // nothing matched
		{"500", "0", "-50", "0"},     // never negative
	}
	for _, tc := range tests {
		if got := Relief(d(tc.reserved), d(tc.relieved), d(tc.matched)); !got.Equal(d(tc.want)) {
			t.Errorf("Relief(%s,%s,%s) = %s, want %s", tc.reserved, tc.relieved, tc.matched, got, tc.want)
		}
	}
}

// Property: relief is never negative, never exceeds the matched share, and the
// reservation never goes below zero (Σ reserved − relieved ≥ 0).
func TestRelief_Property(t *testing.T) {
	cents := rapid.Int64Range(0, 10_000_000)
	rapid.Check(t, func(t *rapid.T) {
		reserved := decimal.New(cents.Draw(t, "reserved"), -2)
		relieved := decimal.New(rapid.Int64Range(0, reserved.Shift(2).IntPart()).Draw(t, "relieved"), -2)
		matched := decimal.New(cents.Draw(t, "matched"), -2)
		relief := Relief(reserved, relieved, matched)
		if relief.Sign() < 0 {
			t.Fatalf("negative relief %s", relief)
		}
		if relief.GreaterThan(matched) {
			t.Fatalf("relief %s exceeds matched share %s", relief, matched)
		}
		after := reserved.Sub(relieved.Add(relief))
		if after.Sign() < 0 {
			t.Fatalf("reservation overdrawn: %s", after)
		}
	})
}

func TestEncumbranceStatus(t *testing.T) {
	tests := []struct{ reserved, relieved, want string }{
		{"500", "0", StatusOpen},
		{"500", "200", StatusPartiallyRelieved},
		{"500", "500", StatusRelieved},
		{"0", "0", StatusRelieved}, // a line revised to zero has nothing open
	}
	for _, tc := range tests {
		if got := EncumbranceStatus(d(tc.reserved), d(tc.relieved)); got != tc.want {
			t.Errorf("EncumbranceStatus(%s, %s) = %s, want %s", tc.reserved, tc.relieved, got, tc.want)
		}
	}
}
