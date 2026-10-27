package domain

import (
	"github.com/shopspring/decimal"
)

// PO approval statuses (FR-17.7, BR-17.2).
const (
	POStatusDraft     = "DRAFT"
	POStatusInReview  = "IN_REVIEW"
	POStatusApproved  = "APPROVED"
	POStatusRejected  = "REJECTED"
	POStatusConfirmed = "CONFIRMED"
	POStatusFinalized = "FINALIZED"
	POStatusCancelled = "CANCELLED"
)

// PO line statuses (3.17.4). A cancelled line stays on the order and in its
// versions (BR-17.9).
const (
	POLineOpen      = "OPEN"
	POLineCancelled = "CANCELLED"
)

// PO problem codes (3.17.18).
const (
	// CodePOState is raised when a PO action or edit is not allowed in the current status.
	CodePOState = "PRC-PO-STATE"
	// CodeReceivedFloor is PRC-PO-007: a quantity may not drop below received or invoiced.
	CodeReceivedFloor = "PRC-PO-007"
	// CodeConfirmPrecondition is PRC-PO-008: only an APPROVED PO can be confirmed.
	CodeConfirmPrecondition = "PRC-PO-008"
)

// PO custom methods (3.17.5). Approve and reject stand in for the approval
// tasks of the wave 4 workflow, which is a later slice.
const (
	POActionSubmit  = "submit"
	POActionApprove = "approve"
	POActionReject  = "reject"
	POActionConfirm = "confirm"
	POActionCancel  = "cancel"
)

// POActions lists the custom methods in the order the UI offers them.
var POActions = []string{POActionSubmit, POActionApprove, POActionReject, POActionConfirm, POActionCancel}

// poTransitions is the BR-17.2 approval state machine for the custom methods
// other than cancel. A confirmed PO that a change sent back to IN_REVIEW walks
// approve and confirm again; a rejected one is edited and submitted again.
var poTransitions = map[string]map[string]string{
	POActionSubmit:  {POStatusDraft: POStatusInReview, POStatusRejected: POStatusInReview},
	POActionApprove: {POStatusInReview: POStatusApproved},
	POActionReject:  {POStatusInReview: POStatusRejected},
	POActionConfirm: {POStatusApproved: POStatusConfirmed},
}

// NextApproval returns the approval status after applying action to current.
// Confirming anything but an APPROVED PO is PRC-PO-008; every other refused
// transition is PRC-PO-STATE.
func NextApproval(current, action string) (string, error) {
	edges, ok := poTransitions[action]
	if !ok {
		return "", fail(CodePOState, "unknown purchase order action %q", action)
	}
	next, ok := edges[current]
	if !ok {
		if action == POActionConfirm {
			return "", fail(CodeConfirmPrecondition, "purchase order must be APPROVED before confirmation; it is %s", current)
		}
		return "", fail(CodePOState, "purchase order is %s; %s is not allowed", current, action)
	}
	return next, nil
}

// CheckEditable reports whether PATCH may change a PO in this approval status.
// An APPROVED PO is frozen until it is confirmed (or rejected back), and a
// finalized or cancelled PO is closed.
func CheckEditable(approval string) error {
	switch approval {
	case POStatusDraft, POStatusInReview, POStatusRejected, POStatusConfirmed:
		return nil
	}
	return fail(CodePOState, "purchase order is %s and cannot be edited", approval)
}

// CanCancel reports whether a PO may be cancelled (BR-17.2): it must not be
// finalized or already cancelled, and nothing may have been received or invoiced.
func CanCancel(approval string, receivedTotal, invoicedTotal decimal.Decimal) error {
	switch approval {
	case POStatusFinalized, POStatusCancelled:
		return fail(CodePOState, "purchase order is %s and cannot be cancelled", approval)
	}
	if receivedTotal.IsPositive() || invoicedTotal.IsPositive() {
		return fail(CodePOState, "purchase order has received or invoiced quantities and cannot be cancelled")
	}
	return nil
}

// CheckReduction enforces BR-17.7 and FR-17.8: a line quantity may not drop below
// the received quantity, and may drop below the invoiced quantity only when the
// policy allows invoicing before receipt. newQty is 0 for a removed line.
func CheckReduction(lineNo int, received, invoiced, newQty decimal.Decimal, allowInvoiceBeforeReceipt bool) error {
	if newQty.LessThan(received) {
		return fail(CodeReceivedFloor, "line %d quantity %s cannot be below received %s / invoiced %s", lineNo, newQty, received, invoiced)
	}
	if newQty.LessThan(invoiced) && !allowInvoiceBeforeReceipt {
		return fail(CodeReceivedFloor, "line %d quantity %s cannot be below received %s / invoiced %s", lineNo, newQty, received, invoiced)
	}
	return nil
}
