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
// (BR-27.9; overdelivery_pct is 0 until the parameter table exists).
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

// ReleaseAgreement applies a release of qty against a commitment that already
// has released quantity released. It returns the new released quantity and the
// resulting status. A release that would exceed the commitment is refused with
// ErrAgreementExhausted and nothing changes.
func ReleaseAgreement(commitment, released, qty decimal.Decimal) (decimal.Decimal, string, error) {
	if err := ValidateQuantity(qty); err != nil {
		return released, "", err
	}
	next := released.Add(qty)
	if next.GreaterThan(commitment) {
		return released, "", ErrAgreementExhausted
	}
	status := AgreementActive
	if next.Equal(commitment) {
		status = AgreementExhausted
	}
	return next, status, nil
}

// AgreementClosed is the state of an agreement ended early: it takes no more
// releases (FR-27.10).
const AgreementClosed = "CLOSED"

// Revision and return refusals.
var (
	ErrAgreementClosed         = errors.New("agreement is closed")
	ErrCommitmentBelowReleased = errors.New("commitment cannot fall below the quantity already released")
	ErrInvalidValidity         = errors.New("validFrom must not be after validTo")
)

// AgreementState is the part of an agreement a revision can change.
type AgreementState struct {
	Commitment   decimal.Decimal
	Released     decimal.Decimal
	UnitPrice    decimal.Decimal
	CumTolerance decimal.Decimal
	ValidFrom    *string
	ValidTo      *string
	Status       string
}

// AgreementRevision is a partial change. A nil field is left as it is; for
// ValidFrom and ValidTo the empty string clears the bound.
type AgreementRevision struct {
	Commitment   *decimal.Decimal
	UnitPrice    *decimal.Decimal
	CumTolerance *decimal.Decimal
	ValidFrom    *string
	ValidTo      *string
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
		if rev.Commitment.LessThan(cur.Released) {
			return cur, nil, ErrCommitmentBelowReleased
		}
		next.Commitment = *rev.Commitment
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
	next.Status = AgreementActive
	if next.Commitment.Equal(next.Released) {
		next.Status = AgreementExhausted
	}
	var diff []FieldChange
	num := func(field string, a, b decimal.Decimal) {
		if !a.Equal(b) {
			diff = append(diff, FieldChange{Field: field, Old: a.String(), New: b.String()})
		}
	}
	num("commitmentQty", cur.Commitment, next.Commitment)
	num("unitPrice", cur.UnitPrice, next.UnitPrice)
	num("cumTolerance", cur.CumTolerance, next.CumTolerance)
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
func ReturnToAgreement(commitment, released, qty decimal.Decimal, status string) (decimal.Decimal, string, error) {
	if err := ValidateQuantity(qty); err != nil {
		return released, status, err
	}
	next := released.Sub(qty)
	if next.IsNegative() {
		return released, status, errors.New("returned quantity exceeds the released quantity")
	}
	if status != AgreementClosed {
		status = AgreementActive
		if next.Equal(commitment) {
			status = AgreementExhausted
		}
	}
	return next, status, nil
}
