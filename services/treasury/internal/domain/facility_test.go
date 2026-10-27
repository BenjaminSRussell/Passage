package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

// AC-36.6: facility EUR 1,000,000 with EUR 950,000 utilized; an LC of
// EUR 100,000 is blocked without the override duty.
func TestCheckFacilityLimit_AC36_6(t *testing.T) {
	limit := decimal.NewFromInt(1000000)
	used := decimal.NewFromInt(950000)
	amount := decimal.NewFromInt(100000)

	err := CheckFacilityLimit(limit, used, amount, false)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeFacilityLimit {
		t.Fatalf("error = %v, want %s", err, CodeFacilityLimit)
	}
	if de.Detail != "facility limit exceeded by 50000.00" {
		t.Fatalf("detail = %q", de.Detail)
	}
	if err := CheckFacilityLimit(limit, used, amount, true); err != nil {
		t.Fatalf("override should pass: %v", err)
	}
	if err := CheckFacilityLimit(limit, used, decimal.NewFromInt(50000), false); err != nil {
		t.Fatalf("exact fill should pass: %v", err)
	}
}

func TestCheckFacilityLimit_RejectsNonPositiveAmount(t *testing.T) {
	err := CheckFacilityLimit(decimal.NewFromInt(10), decimal.Zero, decimal.Zero, true)
	if err == nil {
		t.Fatal("zero amount accepted")
	}
}

// Property: without override, an instrument passes iff utilized + amount <= limit.
func TestCheckFacilityLimit_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		limit := rapid.Int64Range(1, 1000000).Draw(t, "limit")
		used := rapid.Int64Range(0, 1000000).Draw(t, "used")
		amount := rapid.Int64Range(1, 1000000).Draw(t, "amount")
		override := rapid.Bool().Draw(t, "override")
		err := CheckFacilityLimit(decimal.NewFromInt(limit), decimal.NewFromInt(used), decimal.NewFromInt(amount), override)
		want := override || used+amount <= limit
		if (err == nil) != want {
			t.Fatalf("limit=%d used=%d amount=%d override=%v: err=%v want ok=%v", limit, used, amount, override, err, want)
		}
	})
}

func TestHeadroomNeverNegative(t *testing.T) {
	if h := Headroom(decimal.NewFromInt(10), decimal.NewFromInt(25)); !h.IsZero() {
		t.Fatalf("headroom = %s, want 0", h)
	}
	if h := Headroom(decimal.NewFromInt(100), decimal.NewFromInt(25)); h.StringFixed(2) != "75.00" {
		t.Fatalf("headroom = %s, want 75.00", h)
	}
}

func TestValidInstrumentKind(t *testing.T) {
	for _, k := range []string{InstrumentLCImport, InstrumentLCExport, InstrumentGuarantee} {
		if !ValidInstrumentKind(k) {
			t.Fatalf("%s rejected", k)
		}
	}
	if ValidInstrumentKind("bill") {
		t.Fatal("bill accepted as facility instrument")
	}
}
