package domain

import (
	"errors"

	"github.com/shopspring/decimal"
)

// Scheduling-agreement release and schedule line states (FR-27.20, BR-27.12).
const (
	ReleaseForecast   = "FORECAST"
	ReleaseJIT        = "JIT"
	ReleaseAccepted   = "ACCEPTED"
	ReleaseHeld       = "HELD"
	ReleaseRejected   = "REJECTED"
	ReleaseSuperseded = "SUPERSEDED"
	ReleaseLineOpen   = "OPEN"
	ReleaseLineOld    = "SUPERSEDED"

	DecisionAccept = "ACCEPT"
	DecisionReject = "REJECT"
)

var maxCumulative = decimal.New(1, 14)

// ValidateCumulative checks a cumulative quantity: not negative, at most six
// decimal places and below 10^14 (the column is numeric(20,6)).
func ValidateCumulative(q decimal.Decimal) error {
	switch {
	case q.IsNegative():
		return errors.New("must not be negative")
	case !q.Round(quantityScale).Equal(q):
		return errors.New("allows at most 6 decimal places")
	case q.GreaterThanOrEqual(maxCumulative):
		return errors.New("is too large")
	}
	return nil
}

// OpenReleaseLines derives the open schedule lines of a release that is
// accepted without the cumulative comparison (a reviewer's override, or a
// recalculation after shipments changed): open quantity is cumulative required
// less what was shipped since the reset.
func OpenReleaseLines(cumShipped decimal.Decimal, buckets []ReleaseBucket) (ReleaseCheck, error) {
	return CheckRelease(cumShipped, cumShipped, decimal.Zero, buckets)
}

// CheckShippedUpdate validates a new cumulative shipped quantity. The
// quantity may only fall when the cumulative reset date moves forward (a new
// fiscal year or an explicit reset); the reset date never moves back.
func CheckShippedUpdate(prevShipped, newShipped decimal.Decimal, prevReset, newReset *string) error {
	if newShipped.IsNegative() {
		return errors.New("cumulative shipped quantity must not be negative")
	}
	if newReset != nil && prevReset != nil && *newReset < *prevReset {
		return errors.New("the cumulative reset date must not move back")
	}
	reset := newReset != nil && (prevReset == nil || *newReset > *prevReset)
	if !reset && newShipped.LessThan(prevShipped) {
		return errors.New("cumulative shipped quantity cannot decrease without a later reset date")
	}
	return nil
}
