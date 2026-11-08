package domain

import (
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

// Sales agreement states (FR-27.10). An agreement is EXHAUSTED once released
// quantity reaches the commitment.
const (
	AgreementActive    = "ACTIVE"
	AgreementExhausted = "EXHAUSTED"
)

// ErrAgreementExhausted is returned when a release would exceed the commitment
// plus its over-delivery tolerance (BR-27.9).
var ErrAgreementExhausted = errors.New("agreement commitment exhausted")

// ErrAgreementNotValid is returned when a release falls outside the agreement's
// validity period.
var ErrAgreementNotValid = errors.New("agreement is not valid on the release date")

// FormatAgreementNumber renders the SA- sequence (PRD §5.27.15).
func FormatAgreementNumber(seq int64) string { return fmt.Sprintf("SA-%07d", seq) }

// AgreementValidOn reports whether day lies within the inclusive validity
// bounds. Nil bounds are open-ended.
func AgreementValidOn(from, to *time.Time, day time.Time) bool {
	if from != nil && day.Before(*from) {
		return false
	}
	return to == nil || !day.After(*to)
}

// ErrInvalidOverdelivery is returned for an over-delivery tolerance outside 0
// to 100 percent or with more than two decimal places.
var ErrInvalidOverdelivery = errors.New("overdeliveryPct must be between 0 and 100 with at most 2 decimal places")

// ValidateOverdelivery checks an over-delivery tolerance in percent.
func ValidateOverdelivery(pct decimal.Decimal) error {
	if pct.IsNegative() || pct.GreaterThan(decimal.NewFromInt(100)) || !pct.Round(2).Equal(pct) {
		return ErrInvalidOverdelivery
	}
	return nil
}

// AgreementCeiling is the most that may be released against a commitment: the
// commitment plus the over-delivery tolerance pct, cut to 6 places (BR-27.9).
// It never exceeds the exact figure, which the database check enforces.
func AgreementCeiling(commitment, pct decimal.Decimal) decimal.Decimal {
	return commitment.Mul(decimal.NewFromInt(100).Add(pct)).Div(decimal.NewFromInt(100)).Truncate(6)
}

// agreementStatus is EXHAUSTED once released reaches the ceiling, else ACTIVE.
func agreementStatus(commitment, pct, released decimal.Decimal) string {
	if released.GreaterThanOrEqual(AgreementCeiling(commitment, pct)) {
		return AgreementExhausted
	}
	return AgreementActive
}

// ReleaseAgreement applies a release of qty against a commitment with
// over-delivery tolerance pct that already has released quantity released. It
// returns the new released quantity and the resulting status. A release that
// would exceed the ceiling is refused with ErrAgreementExhausted and nothing
// changes. The agreement is EXHAUSTED when released reaches the ceiling.
func ReleaseAgreement(commitment, pct, released, qty decimal.Decimal) (decimal.Decimal, string, error) {
	if err := ValidateQuantity(qty); err != nil {
		return released, "", err
	}
	next := released.Add(qty)
	if next.GreaterThan(AgreementCeiling(commitment, pct)) {
		return released, "", ErrAgreementExhausted
	}
	return next, agreementStatus(commitment, pct, next), nil
}

// AgreementClosed is the state of an agreement ended early: it takes no more
// releases (FR-27.10).
const AgreementClosed = "CLOSED"

// Revision and return refusals.
var (
	ErrAgreementClosed         = errors.New("agreement is closed")
	ErrCommitmentBelowReleased = errors.New("commitment, with its over-delivery tolerance, cannot fall below the quantity already released")
	ErrInvalidValidity         = errors.New("validFrom must not be after validTo")
)

// AgreementState is the part of an agreement a revision can change.
type AgreementState struct {
	Commitment   decimal.Decimal
	Released     decimal.Decimal
	UnitPrice    decimal.Decimal
	CumTolerance decimal.Decimal
	// OverdeliveryPct is the tolerance above the commitment (BR-27.9).
	OverdeliveryPct decimal.Decimal
	ValidFrom       *string
	ValidTo         *string
	Status          string
}

// AgreementRevision is a partial change. A nil field is left as it is; for
// ValidFrom and ValidTo the empty string clears the bound.
type AgreementRevision struct {
	Commitment   *decimal.Decimal
	UnitPrice    *decimal.Decimal
	CumTolerance *decimal.Decimal
	// OverdeliveryPct changes the tolerance above the commitment.
	OverdeliveryPct *decimal.Decimal
	ValidFrom       *string
	ValidTo         *string
}

func dayText(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func bound(cur *string, in *string) *string {
	if in == nil {
		return cur
	}
	if *in == "" {
		return nil
	}
	return in
}

// ReviseAgreement applies rev to cur and returns the new state and the
// field-level diff, empty when nothing changes. A closed agreement is refused
// with ErrAgreementClosed, a commitment below the released quantity with
// ErrCommitmentBelowReleased and inverted validity with ErrInvalidValidity.
// The status follows the commitment: EXHAUSTED when everything is released,
// otherwise ACTIVE. A new price applies to later releases only; orders already
// created keep the price they were released at.
func ReviseAgreement(cur AgreementState, rev AgreementRevision) (AgreementState, []FieldChange, error) {
	if cur.Status == AgreementClosed {
		return cur, nil, ErrAgreementClosed
	}
	next := cur
	if rev.Commitment != nil {
		if err := ValidateQuantity(*rev.Commitment); err != nil {
			return cur, nil, err
		}
		next.Commitment = *rev.Commitment
	}
	if rev.OverdeliveryPct != nil {
		if err := ValidateOverdelivery(*rev.OverdeliveryPct); err != nil {
			return cur, nil, err
		}
		next.OverdeliveryPct = *rev.OverdeliveryPct
	}
	if AgreementCeiling(next.Commitment, next.OverdeliveryPct).LessThan(cur.Released) {
		return cur, nil, ErrCommitmentBelowReleased
	}
	if rev.UnitPrice != nil {
		next.UnitPrice = *rev.UnitPrice
	}
	if rev.CumTolerance != nil {
		next.CumTolerance = *rev.CumTolerance
	}
	next.ValidFrom, next.ValidTo = bound(cur.ValidFrom, rev.ValidFrom), bound(cur.ValidTo, rev.ValidTo)
	if next.ValidFrom != nil && next.ValidTo != nil && *next.ValidFrom > *next.ValidTo {
		return cur, nil, ErrInvalidValidity
	}
	next.Status = agreementStatus(next.Commitment, next.OverdeliveryPct, next.Released)
	var diff []FieldChange
	num := func(field string, a, b decimal.Decimal) {
		if !a.Equal(b) {
			diff = append(diff, FieldChange{Field: field, Old: a.String(), New: b.String()})
		}
	}
	num("commitmentQty", cur.Commitment, next.Commitment)
	num("unitPrice", cur.UnitPrice, next.UnitPrice)
	num("cumTolerance", cur.CumTolerance, next.CumTolerance)
	num("overdeliveryPct", cur.OverdeliveryPct, next.OverdeliveryPct)
	diff = append(diff, DiffHeader("validFrom", dayText(cur.ValidFrom), dayText(next.ValidFrom))...)
	diff = append(diff, DiffHeader("validTo", dayText(cur.ValidTo), dayText(next.ValidTo))...)
	if len(diff) == 0 {
		return cur, nil, nil
	}
	diff = append(diff, DiffHeader("status", cur.Status, next.Status)...)
	return next, diff, nil
}

// ReturnToAgreement gives qty of a cancelled release back to the commitment.
// It returns the new released quantity and the status, which stays CLOSED for a
// closed agreement and is otherwise ACTIVE again.
func ReturnToAgreement(commitment, pct, released, qty decimal.Decimal, status string) (decimal.Decimal, string, error) {
	if err := ValidateQuantity(qty); err != nil {
		return released, status, err
	}
	next := released.Sub(qty)
	if next.IsNegative() {
		return released, status, errors.New("returned quantity exceeds the released quantity")
	}
	if status != AgreementClosed {
		status = agreementStatus(commitment, pct, next)
	}
	return next, status, nil
}
