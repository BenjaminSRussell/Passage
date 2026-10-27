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
	tests := []struct{ reserved, relieved, released, want string }{
		{"500", "0", "0", StatusOpen},
		{"500", "200", "0", StatusPartiallyRelieved},
		{"500", "500", "0", StatusRelieved},
		{"0", "0", "0", StatusRelieved},          // a line revised to zero has nothing open
		{"500", "0", "500", StatusReleased},      // PO line cancelled before any invoice
		{"500", "200", "300", StatusReleased},    // PO closed with a residual
		{"500", "500", "0.0001", StatusReleased}, // any release closes the line
	}
	for _, tc := range tests {
		if got := EncumbranceStatus(d(tc.reserved), d(tc.relieved), d(tc.released)); got != tc.want {
			t.Errorf("EncumbranceStatus(%s, %s, %s) = %s, want %s", tc.reserved, tc.relieved, tc.released, got, tc.want)
		}
	}
}

// FR-38.5: cancelling or closing a PO line releases exactly the open remainder.
func TestRelease_Table(t *testing.T) {
	tests := []struct{ reserved, relieved, released, want string }{
		{"500", "0", "0", "500"},   // cancelled before any invoice
		{"500", "320", "0", "180"}, // closed after a partial invoice
		{"500", "500", "0", "0"},   // fully invoiced: nothing to release
		{"500", "320", "180", "0"}, // already released
		{"0", "0", "0", "0"},       // revised to zero
		{"500.0050", "0.0025", "0", "500.0025"},
	}
	for _, tc := range tests {
		if got := Release(d(tc.reserved), d(tc.relieved), d(tc.released)); !got.Equal(d(tc.want)) {
			t.Errorf("Release(%s, %s, %s) = %s, want %s", tc.reserved, tc.relieved, tc.released, got, tc.want)
		}
	}
}

// Property: after a release the reservation is fully accounted for
// (relieved + released = reserved) and a second release gives nothing.
func TestRelease_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		reserved := decimal.New(rapid.Int64Range(0, 10_000_000).Draw(t, "reserved"), -2)
		relieved := decimal.New(rapid.Int64Range(0, reserved.Shift(2).IntPart()).Draw(t, "relieved"), -2)
		rel := Release(reserved, relieved, decimal.Zero)
		if rel.Sign() < 0 {
			t.Fatalf("negative release %s", rel)
		}
		if !relieved.Add(rel).Equal(reserved) {
			t.Fatalf("relieved %s + released %s != reserved %s", relieved, rel, reserved)
		}
		if again := Release(reserved, relieved, rel); !again.IsZero() {
			t.Fatalf("second release gave %s", again)
		}
		if after := Relief(reserved.Sub(rel), relieved, reserved); !after.IsZero() {
			t.Fatalf("relief after release gave %s", after)
		}
	})
}
