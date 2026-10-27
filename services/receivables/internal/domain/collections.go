package domain

import (
	"fmt"
	"net/http"
	"time"

	"github.com/shopspring/decimal"
)

// Dispute and promise rules (FR-30.9, BR-30.6). Pure functions, no I/O.
const (
	DisputeOpen             = "OPEN"
	DisputeUnderReview      = "UNDER_REVIEW"
	DisputeResolvedCredit   = "RESOLVED_CREDIT"
	DisputeResolvedPaid     = "RESOLVED_PAID"
	DisputeResolvedWriteOff = "RESOLVED_WRITE_OFF"
	DisputeRejected         = "REJECTED"

	// DisputeSLAHours is the CollectionsMaintain default for DISPUTE cases (§5.30.13).
	DisputeSLAHours = 120
	// PromiseGraceDays: an unbroken promise holds until promise date + 2 days (BR-30.2).
	PromiseGraceDays = 2

	CodeDisputeState  = "AR-DISPUTE-STATE"
	CodeDisputeExists = "AR-DISPUTE-EXISTS"
	CodePromiseExists = "AR-PROMISE-EXISTS"
	CodePromiseState  = "AR-PROMISE-STATE"
	CodeAmountRange   = "AR-AMOUNT-RANGE"
)

var disputeNext = map[string]map[string]bool{
	DisputeOpen:        {DisputeUnderReview: true, DisputeRejected: true},
	DisputeUnderReview: {DisputeResolvedCredit: true, DisputeResolvedPaid: true, DisputeResolvedWriteOff: true, DisputeRejected: true},
}

// CanMoveDispute reports whether BR-30.6 allows the move from → to.
func CanMoveDispute(from, to string) bool { return disputeNext[from][to] }

// ResolutionStatus maps a resolution code to the terminal dispute status.
func ResolutionStatus(resolution string) (string, bool) {
	switch resolution {
	case "CREDIT":
		return DisputeResolvedCredit, true
	case "PAID":
		return DisputeResolvedPaid, true
	case "WRITE_OFF":
		return DisputeResolvedWriteOff, true
	case "REJECTED":
		return DisputeRejected, true
	}
	return "", false
}

// DisputeActive reports whether a dispute still excludes its item from dunning
// and interest (FR-30.9).
func DisputeActive(status string) bool {
	return status == DisputeOpen || status == DisputeUnderReview
}

// PromiseHolds reports whether a promise to pay still excludes its item at asOf.
func PromiseHolds(promiseDate, asOf time.Time) bool {
	return !asOf.After(promiseDate.AddDate(0, 0, PromiseGraceDays))
}

// ValidateAmountWithin requires 0 < amount <= open (disputes and promises).
func ValidateAmountWithin(amount, open decimal.Decimal) *Error {
	if !amount.IsPositive() {
		return &Error{Status: http.StatusBadRequest, Code: CodeAmountRange, Detail: "amount must be positive"}
	}
	if amount.GreaterThan(open) {
		return &Error{Status: http.StatusUnprocessableEntity, Code: CodeAmountRange,
			Detail: fmt.Sprintf("amount %s exceeds the open amount %s", amount.String(), open.String())}
	}
	return nil
}
