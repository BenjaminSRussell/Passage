package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

// TestProrationTable is the hire-date fixture set for BR-42.3 (AC-42.2). Each
// expected value is rate × employed ÷ days, rounded half-up to two decimals.
func TestProrationTable(t *testing.T) {
	cases := []struct {
		name     string
		rate     string
		employed int
		days     int
		prorate  bool
		want     string
	}{
		{"full period is the rate", "1.25", 31, 31, true, "1.25"},
		{"hired 16 March", "1.25", 16, 31, true, "0.65"},
		{"hired 15 March", "1.25", 17, 31, true, "0.69"},
		{"hired last day of March", "1.25", 1, 31, true, "0.04"},
		{"hired 15 February 2026", "1.25", 15, 28, true, "0.67"},
		{"hired 14 February, exact tie rounds up", "1.25", 14, 28, true, "0.63"},
		{"30-day month, rate 2.00", "2.00", 10, 30, true, "0.67"},
		{"29 of 30 days", "1.25", 29, 30, true, "1.21"},
		{"no proration keeps full rate", "1.25", 5, 31, false, "1.25"},
		{"rate with four decimals rounds", "0.8333", 31, 31, true, "0.83"},
		{"no days employed accrues nothing", "1.25", 0, 31, true, "0.00"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AccrualAmount(dec(c.rate), c.employed, c.days, c.prorate)
			if !got.Equal(dec(c.want)) {
				t.Fatalf("AccrualAmount(%s, %d, %d, %v) = %s, want %s", c.rate, c.employed, c.days, c.prorate, got, c.want)
			}
		})
	}
}

// TestPlanYearUSPTO15Scenario is the worked example in BR-42.3 and AC-42.3:
// hire 2026-03-16, rate 1.25 per month, max 22.50, carry-over max 5.00.
func TestPlanYearUSPTO15Scenario(t *testing.T) {
	rate, limit, carryMax := dec("1.25"), dec("22.50"), dec("5.00")

	var l Ledger
	march := AccrualAmount(rate, 16, 31, true)
	l, _ = l.Accrue(march, limit)
	for month := 4; month <= 12; month++ {
		l, _ = l.Accrue(AccrualAmount(rate, 30, 30, true), limit)
	}
	if !l.Current.Accrued.Equal(dec("11.90")) {
		t.Fatalf("accrued through December = %s, want 11.90", l.Current.Accrued)
	}

	l, err := l.Consume(dec("4.00"), decimal.Zero)
	if err != nil {
		t.Fatalf("consume 4.00: %v", err)
	}
	if !l.Total().Equal(dec("7.90")) {
		t.Fatalf("balance at 2026-12-31 = %s, want 7.90", l.Total())
	}

	l, expired := l.YearEnd(carryMax)
	if !l.Carry.Balance().Equal(dec("5.00")) || !expired.Equal(dec("2.90")) {
		t.Fatalf("year-end carry = %s expired = %s; want 5.00 and 2.90", l.Carry.Balance(), expired)
	}

	l, _ = l.Accrue(rate, limit)
	if !l.Current.Accrued.Equal(dec("1.25")) || !l.Total().Equal(dec("6.25")) {
		t.Fatalf("January accrual: current %s total %s; want 1.25 and 6.25", l.Current.Accrued, l.Total())
	}

	l, err = l.Consume(dec("2.00"), decimal.Zero)
	if err != nil {
		t.Fatalf("consume 2.00: %v", err)
	}
	if !l.Carry.Used.Equal(dec("2.00")) || !l.Current.Used.Equal(decimal.Zero) {
		t.Fatalf("consumption must take carry-over first: carry used %s, current used %s", l.Carry.Used, l.Current.Used)
	}
}

// TestConsumeRespectsFloor covers FR-42.7 and BR-42.4: a request beyond the
// balance (or beyond the negative floor) is refused with HCM-ABS-002.
func TestConsumeRespectsFloor(t *testing.T) {
	cases := []struct {
		name    string
		balance string
		qty     string
		floor   string
		refused bool
	}{
		{"3.00 against 2.00 with no negatives", "2.00", "3.00", "0", true},
		{"exact balance is allowed", "2.00", "2.00", "0", false},
		{"negative allowed down to the floor", "2.00", "3.50", "-2.50", false},
		{"beyond the negative floor", "2.00", "4.60", "-2.50", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			l := Ledger{Current: Bucket{Accrued: dec(c.balance)}}
			_, err := l.Consume(dec(c.qty), dec(c.floor))
			var de *Error
			refused := err != nil
			if refused != c.refused {
				t.Fatalf("Consume(%s, floor %s) refused=%v, want %v", c.qty, c.floor, refused, c.refused)
			}
			if refused && (!errors.As(err, &de) || de.Code != "HCM-ABS-002") {
				t.Fatalf("refusal code = %v, want HCM-ABS-002", err)
			}
		})
	}
}
