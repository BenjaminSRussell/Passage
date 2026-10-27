package domain

import (
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

func TestAccrualAmount_Proration(t *testing.T) {
	cases := []struct {
		name            string
		rate            string
		employed, total int
		prorate         bool
		want            string
	}{
		{"hire 16 March prorates to 0.65", "1.25", 16, 31, true, "0.65"},
		{"full month is the rate", "1.25", 31, 31, true, "1.25"},
		{"no proration ignores days", "1.25", 16, 31, false, "1.25"},
		{"zero days employed accrues nothing", "1.25", 0, 31, true, "0"},
		{"half-up at the second decimal", "1.00", 1, 8, true, "0.13"},
	}
	for _, c := range cases {
		got := AccrualAmount(dec(c.rate), c.employed, c.total, c.prorate)
		if !got.Equal(dec(c.want)) {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

// AC-42.3 / BR-42.3: plan US-PTO-15, hire 2026-03-16, rate 1.25 per month,
// max 22.50, carry-over max 5.00. Balances must match the stated figures.
func TestLedger_US_PTO_15_Walkthrough(t *testing.T) {
	rate, maxBal, carryMax := dec("1.25"), dec("22.50"), dec("5.00")
	var l Ledger
	var discarded decimal.Decimal
	first := AccrualAmount(rate, 16, 31, true) // March, 16 days employed
	l, d := l.Accrue(first, maxBal)
	discarded = discarded.Add(d)
	if !first.Equal(dec("0.65")) {
		t.Fatalf("March accrual = %s, want 0.65", first)
	}
	for month := 4; month <= 12; month++ { // April to December: 9 full months
		l, d = l.Accrue(rate, maxBal)
		discarded = discarded.Add(d)
	}
	if !l.Current.Accrued.Equal(dec("11.90")) {
		t.Fatalf("accrued 2026 = %s, want 11.90", l.Current.Accrued)
	}
	l, err := l.Consume(dec("4.00"), decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Total().Equal(dec("7.90")) {
		t.Fatalf("balance at 2026-12-31 = %s, want 7.90", l.Total())
	}
	l, expired := l.YearEnd(carryMax)
	if !l.Carry.Balance().Equal(dec("5.00")) || !expired.Equal(dec("2.90")) {
		t.Fatalf("carry = %s expired = %s, want 5.00 and 2.90", l.Carry.Balance(), expired)
	}
	l, _ = l.Accrue(rate, maxBal) // 2027-01-31
	if !l.Total().Equal(dec("6.25")) {
		t.Fatalf("balance 2027-01-31 = %s, want 6.25", l.Total())
	}
	if !discarded.IsZero() {
		t.Errorf("nothing should be discarded below the cap, got %s", discarded)
	}
}

func TestLedger_ConsumeTakesCarryOverFirst(t *testing.T) {
	l := Ledger{Carry: Bucket{Accrued: dec("5.00")}, Current: Bucket{Accrued: dec("1.25")}}
	l, err := l.Consume(dec("2.00"), decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	if !l.Carry.Balance().Equal(dec("3.00")) || !l.Current.Balance().Equal(dec("1.25")) {
		t.Errorf("carry %s current %s, want 3.00 and 1.25", l.Carry.Balance(), l.Current.Balance())
	}
}

func TestLedger_ConsumeRejectsBeyondFloor(t *testing.T) {
	l := Ledger{Current: Bucket{Accrued: dec("2.00")}}
	_, err := l.Consume(dec("3.00"), decimal.Zero)
	if e, ok := err.(*Error); !ok || e.Code != "HCM-ABS-002" { // AC-42.4
		t.Fatalf("want HCM-ABS-002, got %v", err)
	}
	// With a negative floor (negatives allowed) the same request is accepted.
	if _, err := l.Consume(dec("3.00"), dec("-2.50")); err != nil {
		t.Errorf("negative allowance should accept: %v", err)
	}
}

// Property: accruals never push the total above the cap, and the excess plus
// the kept amount equals what was offered.
func TestLedger_AccrueRespectsCap(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		maxBal := decimal.NewFromInt(int64(rapid.IntRange(1, 40).Draw(t, "max")))
		var l Ledger
		for i := 0; i < rapid.IntRange(1, 30).Draw(t, "steps"); i++ {
			offer := decimal.NewFromInt(int64(rapid.IntRange(0, 8).Draw(t, "offer")))
			before := l.Total()
			next, discarded := l.Accrue(offer, maxBal)
			if next.Total().GreaterThan(maxBal) {
				t.Fatalf("total %s exceeds cap %s", next.Total(), maxBal)
			}
			if !next.Total().Add(discarded).Equal(before.Add(offer)) {
				t.Fatalf("kept + discarded != offered")
			}
			l = next
		}
	})
}

// Property: consumption never breaches the floor it was given.
func TestLedger_ConsumeNeverBreachesFloor(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		l := Ledger{Current: Bucket{Accrued: decimal.NewFromInt(int64(rapid.IntRange(0, 20).Draw(t, "start")))}}
		floor := decimal.NewFromInt(-int64(rapid.IntRange(0, 5).Draw(t, "floor")))
		for i := 0; i < rapid.IntRange(1, 10).Draw(t, "requests"); i++ {
			qty := decimal.NewFromInt(int64(rapid.IntRange(1, 6).Draw(t, "qty")))
			next, err := l.Consume(qty, floor)
			if err != nil {
				continue
			}
			if next.Total().LessThan(floor) {
				t.Fatalf("balance %s below floor %s", next.Total(), floor)
			}
			l = next
		}
	})
}
