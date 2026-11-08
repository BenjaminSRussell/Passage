package domain

import (
	"errors"
	"fmt"
)

// Requisition statuses the state machine moves between (BR-17.1).
const (
	ReqDraft     = "DRAFT"
	ReqInReview  = "IN_REVIEW"
	ReqApproved  = "APPROVED"
	ReqRejected  = "REJECTED"
	ReqCancelled = "CANCELLED"
)

// Requisition actions after submit (BR-17.1).
const (
	ReqActionApprove = "approve"
	ReqActionReject  = "reject"
	ReqActionReopen  = "reopen"
	ReqActionCancel  = "cancel"
	// ReqActionWithdraw takes an IN_REVIEW requisition back to DRAFT before any level has decided on it.
	ReqActionWithdraw = "withdraw"
)

// ReqActions lists the actions in the order the spec names them.
var ReqActions = []string{ReqActionApprove, ReqActionReject, ReqActionReopen, ReqActionCancel, ReqActionWithdraw}

// ErrRequisitionState is wrapped by every refusal of RequisitionTransition so the store can turn it into 409.
var ErrRequisitionState = errors.New("requisition state")

// RequisitionTransition applies BR-17.1 after submit: IN_REVIEW → APPROVED when the last chain level approves
// (otherwise it stays IN_REVIEW one level further on); IN_REVIEW → REJECTED; IN_REVIEW → DRAFT by withdrawal while no level has approved; REJECTED → DRAFT (the requester
// revises and submits again, which routes the chain afresh); DRAFT, REJECTED or APPROVED → CANCELLED. It returns
// the next status and the number of levels approved afterwards. Whether an APPROVED requisition may be cancelled
// (no line ordered or held by an RFQ) depends on its lines and is the store's check.
func RequisitionTransition(status, action string, approvedSteps, chainLen int) (string, int, error) {
	refuse := func(want string) (string, int, error) {
		return "", 0, fmt.Errorf("%w: requisition is %s; %s needs %s", ErrRequisitionState, status, action, want)
	}
	switch action {
	case ReqActionApprove:
		if status != ReqInReview {
			return refuse("IN_REVIEW")
		}
		if approvedSteps < 0 || approvedSteps >= chainLen {
			return "", 0, fmt.Errorf("%w: no approval level is left (%d of %d approved)", ErrRequisitionState, approvedSteps, chainLen)
		}
		if approvedSteps+1 == chainLen {
			return ReqApproved, approvedSteps + 1, nil
		}
		return ReqInReview, approvedSteps + 1, nil
	case ReqActionReject:
		if status != ReqInReview {
			return refuse("IN_REVIEW")
		}
		return ReqRejected, approvedSteps, nil
	case ReqActionReopen:
		if status != ReqRejected {
			return refuse("REJECTED")
		}
		return ReqDraft, 0, nil
	case ReqActionWithdraw:
		if status != ReqInReview {
			return refuse("IN_REVIEW")
		}
		if approvedSteps != 0 {
			return "", 0, fmt.Errorf("%w: level %d has already approved; only the approvers can send it back now", ErrRequisitionState, approvedSteps)
		}
		return ReqDraft, 0, nil
	case ReqActionCancel:
		if status != ReqDraft && status != ReqRejected && status != ReqApproved {
			return refuse("DRAFT, REJECTED or APPROVED")
		}
		return ReqCancelled, approvedSteps, nil
	}
	return "", 0, fmt.Errorf("%w: unknown action %q", ErrRequisitionState, action)
}

// NextApprover names the role that decides next, or "" when no level is waiting.
func NextApprover(chain []string, approvedSteps int) string {
	if approvedSteps < 0 || approvedSteps >= len(chain) {
		return ""
	}
	return chain[approvedSteps]
}
