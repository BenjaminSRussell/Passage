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
