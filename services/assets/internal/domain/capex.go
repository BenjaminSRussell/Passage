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

// CapexDecision is an approver or owner action on a request after it is
// submitted (FR-37.17, BR-37.12). Each action is one legal move of the state
// machine; amount changes after approval (back to Submitted) are a separate
// command.
type CapexDecision string

const (
	DecisionApprove  CapexDecision = "approve"
	DecisionReject   CapexDecision = "reject"
	DecisionPostpone CapexDecision = "postpone"
	DecisionReopen   CapexDecision = "reopen"
	DecisionRelease  CapexDecision = "release"
	DecisionClose    CapexDecision = "close"
)

// CapexDecisions lists every action in the order the API documents them.
var CapexDecisions = []CapexDecision{DecisionApprove, DecisionReject, DecisionPostpone, DecisionReopen, DecisionRelease, DecisionClose}

var decisionMoves = map[CapexDecision]struct{ from, to CapexStatus }{
	DecisionApprove:  {CapexInApproval, CapexApproved},
	DecisionReject:   {CapexInApproval, CapexRejected},
	DecisionPostpone: {CapexInApproval, CapexPostponed},
	DecisionReopen:   {CapexPostponed, CapexDraft},
	DecisionRelease:  {CapexApproved, CapexReleased},
	DecisionClose:    {CapexReleased, CapexClosed},
}

// IsApproverDecision reports whether the action is the approver's verdict
// (approve, reject, postpone), which the requester may not take on their own
// request (PRD 7.37.17 segregation of duties).
func (d CapexDecision) IsApproverDecision() bool {
	return d == DecisionApprove || d == DecisionReject || d == DecisionPostpone
}

// Apply returns the status the action moves a request into from its current
// status, or an AST-0016 error when the move is not allowed. Each action has
// exactly one source status, so approve on a Postponed request is refused
// even though Postponed is reachable from InApproval.
func (d CapexDecision) Apply(from CapexStatus) (CapexStatus, error) {
	move, ok := decisionMoves[d]
	if !ok {
		return "", fail(CodeCapexState, fmt.Sprintf("unknown capex decision %q", d))
	}
	if from != move.from {
		return "", fail(CodeCapexState, fmt.Sprintf("capex request in status %s cannot be %s; only a %s request can", from, d.pastTense(), move.from))
	}
	if err := TransitionCapex(from, move.to); err != nil {
		return "", err
	}
	return move.to, nil
}

func (d CapexDecision) pastTense() string {
	switch d {
	case DecisionApprove:
		return "approved"
	case DecisionReject:
		return "rejected"
	case DecisionPostpone:
		return "postponed"
	case DecisionReopen:
		return "reopened"
	case DecisionRelease:
		return "released"
	case DecisionClose:
		return "closed"
	}
	return string(d)
}
