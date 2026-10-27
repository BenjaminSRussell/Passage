package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// TransitionCapex returns a CodeCapexState error unless from → to is allowed (BR-37.12).
func TransitionCapex(from, to CapexStatus) error {
	if !CanTransitionCapex(from, to) {
		return fail(CodeCapexState, fmt.Sprintf("capex request in status %s cannot move to %s", from, to))
	}
	return nil
}

// CapexStatus is the lifecycle of a capital appropriation request (BR-37.12).
type CapexStatus string

const (
	CapexDraft      CapexStatus = "Draft"
	CapexSubmitted  CapexStatus = "Submitted"
	CapexInApproval CapexStatus = "InApproval"
	CapexApproved   CapexStatus = "Approved"
	CapexReleased   CapexStatus = "Released"
	CapexClosed     CapexStatus = "Closed"
	CapexRejected   CapexStatus = "Rejected"
	CapexPostponed  CapexStatus = "Postponed"
)

var capexTransitions = map[CapexStatus][]CapexStatus{
	CapexDraft:      {CapexSubmitted},
	CapexSubmitted:  {CapexInApproval},
	CapexInApproval: {CapexApproved, CapexRejected, CapexPostponed},
	CapexPostponed:  {CapexDraft},
	CapexApproved:   {CapexReleased, CapexSubmitted},
	CapexReleased:   {CapexClosed},
}

// CanTransitionCapex reports whether a request may move from one status to another.
func CanTransitionCapex(from, to CapexStatus) bool {
	for _, next := range capexTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// ApprovalLevel routes a request by amount (capex.approvalLevels defaults).
// Amounts are compared in their own currency; currency conversion is not in this slice.
func ApprovalLevel(amount decimal.Decimal) string {
	switch {
	case amount.LessThanOrEqual(decimal.NewFromInt(50000)):
		return "PlantManager"
	case amount.LessThanOrEqual(decimal.NewFromInt(500000)):
		return "CFO"
	default:
		return "Board"
	}
}

// NeedsReapproval reports whether an amount change after approval returns the
// request to Submitted: the increase exceeds the lower of 10% of the approved
// amount and 10,000 (BR-37.12).
func NeedsReapproval(approved, next decimal.Decimal) bool {
	increase := next.Sub(approved)
	if increase.LessThanOrEqual(decimal.Zero) {
		return false
	}
	pct := approved.Mul(decimal.RequireFromString("0.1"))
	limit := decimal.NewFromInt(10000)
	if pct.LessThan(limit) {
		limit = pct
	}
	return increase.GreaterThan(limit)
}
