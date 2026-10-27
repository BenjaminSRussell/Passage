package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// ReservationType is the kind of earmarked funds document (FR-38.20).
type ReservationType string

const (
	FundsReservation ReservationType = "FundsReservation"
	Precommitment    ReservationType = "Precommitment"
	FundsBlocking    ReservationType = "FundsBlocking"
	RevenueForecast  ReservationType = "RevenueForecast"
)

// ReservationStatus is the lifecycle state of a reservation (BR-38.14).
type ReservationStatus string

const (
	ReservationOpen              ReservationStatus = "Open"
	ReservationPartiallyConsumed ReservationStatus = "PartiallyConsumed"
	ReservationConsumed          ReservationStatus = "Consumed"
	ReservationClosed            ReservationStatus = "Closed"
	ReservationExpired           ReservationStatus = "Expired"
)

// Message codes raised by the reservation rules.
const (
	CodeFundsBlockingConsumed = "CO-0631"  // FundsBlocking cannot be consumed (BR-38.14)
	CodeReservationNotOpen    = "BUD-0006" // closed, expired or fully consumed
	CodeConsumptionExceeds    = "BUD-0007" // amount above the line's open amount
	CodeNotConsumable         = "BUD-0008" // RevenueForecast holds no funds to consume
	CodeReservationLapsed     = "BUD-0009" // expiry date reached
)

// ParseReservationType returns the type for a wire value.
func ParseReservationType(s string) (ReservationType, bool) {
	switch t := ReservationType(s); t {
	case FundsReservation, Precommitment, FundsBlocking, RevenueForecast:
		return t, true
	}
	return "", false
}

// ValidReservationStatus reports whether s is a reservation status.
func ValidReservationStatus(s string) bool {
	switch ReservationStatus(s) {
	case ReservationOpen, ReservationPartiallyConsumed, ReservationConsumed, ReservationClosed, ReservationExpired:
		return true
	}
	return false
}

// HoldsFunds reports whether the document reduces available funds. A
// RevenueForecast does not: it would increase them under the rule flag
// revenue-increases-budget, which is not built.
func (t ReservationType) HoldsFunds() bool { return t != RevenueForecast }

// Consumable reports whether a follow-on document may draw on the document.
func (t ReservationType) Consumable() bool { return t == FundsReservation || t == Precommitment }

// Expires reports whether the document lapses at its expiry date. FundsBlocking
// goes Open -> Closed only (BR-38.14).
func (t ReservationType) Expires() bool { return t != FundsBlocking }

// NumberPrefix is the per-type document number prefix (reservation.documentTypes).
func (t ReservationType) NumberPrefix() string {
	switch t {
	case FundsReservation:
		return "FR"
	case Precommitment:
		return "PC"
	case FundsBlocking:
		return "FB"
	default:
		return "RF"
	}
}

// ReservationLine is one earmarked amount on a control combination.
type ReservationLine struct {
	Amount   decimal.Decimal
	Consumed decimal.Decimal
	Released decimal.Decimal
}

// Open is the amount still held: not consumed and not released.
func (l ReservationLine) Open() decimal.Decimal { return l.Amount.Sub(l.Consumed).Sub(l.Released) }

// Lapsed reports whether the expiry date has been reached on day asOf. The
// expiry date is the first day the reservation no longer holds funds.
func Lapsed(expiry *time.Time, asOf time.Time) bool {
	if expiry == nil {
		return false
	}
	return !dateOf(asOf).Before(dateOf(*expiry))
}

func dateOf(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// CheckConsume validates drawing amount from line on a reservation of type t in
// status s on day asOf (FR-38.20, BR-38.14). amount must be positive.
func CheckConsume(t ReservationType, s ReservationStatus, expiry *time.Time, asOf time.Time, line ReservationLine, amount decimal.Decimal) error {
	switch {
	case t == FundsBlocking:
		return &Error{Code: CodeFundsBlockingConsumed, Detail: "funds blocking documents cannot be consumed"}
	case !t.Consumable():
		return &Error{Code: CodeNotConsumable, Detail: string(t) + " documents hold no funds to consume"}
	case s != ReservationOpen && s != ReservationPartiallyConsumed:
		return &Error{Code: CodeReservationNotOpen, Detail: "reservation is " + string(s)}
	case Lapsed(expiry, asOf):
		return &Error{Code: CodeReservationLapsed, Detail: "reservation reached its expiry date " + expiry.Format("2006-01-02")}
	case amount.Sign() <= 0:
		return &Error{Code: CodeConsumptionExceeds, Detail: "consumed amount must be positive"}
	case amount.GreaterThan(line.Open()):
		return &Error{Code: CodeConsumptionExceeds, Detail: "amount " + amount.String() + " exceeds the line's open " + line.Open().String()}
	}
	return nil
}

// StatusAfterConsume derives Open, PartiallyConsumed or Consumed from the
// document totals after a consumption.
func StatusAfterConsume(total, consumed decimal.Decimal) ReservationStatus {
	switch {
	case consumed.Sign() <= 0:
		return ReservationOpen
	case consumed.GreaterThanOrEqual(total):
		return ReservationConsumed
	default:
		return ReservationPartiallyConsumed
	}
}

// CheckClose validates closing a reservation (BR-38.14): every type closes from
// Open; consumable types also from PartiallyConsumed and Consumed. Closed and
// Expired are final.
func CheckClose(t ReservationType, s ReservationStatus) error {
	switch s {
	case ReservationOpen:
		return nil
	case ReservationPartiallyConsumed, ReservationConsumed:
		if t.Consumable() {
			return nil
		}
	}
	return &Error{Code: CodeReservationNotOpen, Detail: "a " + string(t) + " reservation in status " + string(s) + " cannot be closed"}
}

// Expirable reports whether the expiry job moves the reservation to Expired on
// day asOf: an expiring type, still Open or PartiallyConsumed, past its date.
func Expirable(t ReservationType, s ReservationStatus, expiry *time.Time, asOf time.Time) bool {
	return t.Expires() && (s == ReservationOpen || s == ReservationPartiallyConsumed) && Lapsed(expiry, asOf)
}
