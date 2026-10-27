package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func codeOf(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func day(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return t
}

func dayPtr(s string) *time.Time { t := day(s); return &t }

func TestCheckConsume_Rules(t *testing.T) {
	line := ReservationLine{Amount: d("20000"), Consumed: d("5000"), Released: d("0")}
	asOf := day("2026-09-15")
	tests := []struct {
		name   string
		typ    ReservationType
		status ReservationStatus
		expiry *time.Time
		amount string
		code   string
	}{
		{"funds reservation draws its open amount", FundsReservation, ReservationPartiallyConsumed, dayPtr("2026-12-31"), "15000", ""},
		{"precommitment is consumable", Precommitment, ReservationOpen, nil, "10", ""},
		{"funds blocking is never consumable", FundsBlocking, ReservationOpen, nil, "10", CodeFundsBlockingConsumed},
		{"revenue forecast holds nothing to consume", RevenueForecast, ReservationOpen, nil, "10", CodeNotConsumable},
		{"above the open amount", FundsReservation, ReservationPartiallyConsumed, nil, "15000.01", CodeConsumptionExceeds},
		{"zero amount", FundsReservation, ReservationOpen, nil, "0", CodeConsumptionExceeds},
		{"closed", FundsReservation, ReservationClosed, nil, "10", CodeReservationNotOpen},
		{"expired", FundsReservation, ReservationExpired, nil, "10", CodeReservationNotOpen},
		{"fully consumed", FundsReservation, ReservationConsumed, nil, "10", CodeReservationNotOpen},
		{"expiry date reached", FundsReservation, ReservationOpen, dayPtr("2026-09-15"), "10", CodeReservationLapsed},
		{"day before expiry", FundsReservation, ReservationOpen, dayPtr("2026-09-16"), "10", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckConsume(tc.typ, tc.status, tc.expiry, asOf, line, d(tc.amount))
			if got := codeOf(err); got != tc.code {
				t.Fatalf("code = %q, want %q (err %v)", got, tc.code, err)
			}
		})
	}
}

func TestReservationLine_OpenExcludesReleased(t *testing.T) {
	l := ReservationLine{Amount: d("100"), Consumed: d("30"), Released: d("20")}
	if !l.Open().Equal(d("50")) {
		t.Fatalf("open = %s, want 50", l.Open())
	}
}

// AC-38.12: a 20,000.00 reservation consumed by an 8,000.00 PO is PartiallyConsumed with 12,000.00 open.
func TestStatusAfterConsume(t *testing.T) {
	tests := []struct {
		total, consumed string
		want            ReservationStatus
	}{
		{"20000", "0", ReservationOpen},
		{"20000", "8000", ReservationPartiallyConsumed},
		{"20000", "20000", ReservationConsumed},
	}
	for _, tc := range tests {
		if got := StatusAfterConsume(d(tc.total), d(tc.consumed)); got != tc.want {
			t.Errorf("StatusAfterConsume(%s, %s) = %s, want %s", tc.total, tc.consumed, got, tc.want)
		}
	}
}

// BR-38.14: FundsBlocking goes Open -> Closed only; Closed and Expired are final.
func TestCheckClose_StateMachine(t *testing.T) {
	tests := []struct {
		typ    ReservationType
		status ReservationStatus
		ok     bool
	}{
		{FundsReservation, ReservationOpen, true},
		{FundsReservation, ReservationPartiallyConsumed, true},
		{FundsReservation, ReservationConsumed, true},
		{FundsReservation, ReservationClosed, false},
		{FundsReservation, ReservationExpired, false},
		{FundsBlocking, ReservationOpen, true},
		{FundsBlocking, ReservationClosed, false},
		{RevenueForecast, ReservationOpen, true},
		{RevenueForecast, ReservationExpired, false},
	}
	for _, tc := range tests {
		err := CheckClose(tc.typ, tc.status)
		if (err == nil) != tc.ok {
			t.Errorf("CheckClose(%s, %s) = %v, want ok=%v", tc.typ, tc.status, err, tc.ok)
		}
		if err != nil && codeOf(err) != CodeReservationNotOpen {
			t.Errorf("CheckClose(%s, %s) code = %s", tc.typ, tc.status, codeOf(err))
		}
	}
}

func TestExpirable(t *testing.T) {
	asOf := day("2026-10-01")
	tests := []struct {
		name   string
		typ    ReservationType
		status ReservationStatus
		expiry *time.Time
		want   bool
	}{
		{"open past its date", FundsReservation, ReservationOpen, dayPtr("2026-09-30"), true},
		{"partially consumed on its date", Precommitment, ReservationPartiallyConsumed, dayPtr("2026-10-01"), true},
		{"not yet due", FundsReservation, ReservationOpen, dayPtr("2026-10-02"), false},
		{"no expiry date", FundsReservation, ReservationOpen, nil, false},
		{"fully consumed stays consumed", FundsReservation, ReservationConsumed, dayPtr("2026-09-01"), false},
		{"funds blocking never expires", FundsBlocking, ReservationOpen, dayPtr("2026-09-01"), false},
		{"revenue forecast expires", RevenueForecast, ReservationOpen, dayPtr("2026-09-01"), true},
	}
	for _, tc := range tests {
		if got := Expirable(tc.typ, tc.status, tc.expiry, asOf); got != tc.want {
			t.Errorf("%s: Expirable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestParseReservationTypeAndPrefix(t *testing.T) {
	want := map[string]string{"FundsReservation": "FR", "Precommitment": "PC", "FundsBlocking": "FB", "RevenueForecast": "RF"}
	for s, prefix := range want {
		typ, ok := ParseReservationType(s)
		if !ok || typ.NumberPrefix() != prefix {
			t.Errorf("%s: ok=%v prefix=%s, want %s", s, ok, typ.NumberPrefix(), prefix)
		}
	}
	if _, ok := ParseReservationType("Encumbrance"); ok {
		t.Error("Encumbrance accepted as a reservation type")
	}
	if RevenueForecast.HoldsFunds() || !FundsBlocking.HoldsFunds() {
		t.Error("only RevenueForecast holds no funds")
	}
}

// Property: any sequence of valid consumptions keeps consumed + open = amount,
// never goes negative, and is refused exactly when it would exceed the open amount.
func TestCheckConsume_ConservationProperty(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		amount := decimal.New(rapid.Int64Range(1, 100_000_000).Draw(rt, "cents"), -2)
		line := ReservationLine{Amount: amount, Consumed: decimal.Zero, Released: decimal.Zero}
		asOf := day("2026-01-01")
		for i, n := 0, rapid.IntRange(0, 20).Draw(rt, "steps"); i < n; i++ {
			take := decimal.New(rapid.Int64Range(1, 100_000_000).Draw(rt, "take"), -2)
			err := CheckConsume(FundsReservation, ReservationOpen, nil, asOf, line, take)
			if take.GreaterThan(line.Open()) {
				if codeOf(err) != CodeConsumptionExceeds {
					rt.Fatalf("over-consumption of %s on open %s accepted: %v", take, line.Open(), err)
				}
				continue
			}
			if err != nil {
				rt.Fatalf("valid consumption of %s on open %s refused: %v", take, line.Open(), err)
			}
			line.Consumed = line.Consumed.Add(take)
		}
		if line.Open().IsNegative() || !line.Consumed.Add(line.Open()).Equal(amount) {
			rt.Fatalf("consumed %s + open %s != amount %s", line.Consumed, line.Open(), amount)
		}
	})
}
