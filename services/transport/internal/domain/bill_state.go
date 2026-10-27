package domain

import (
	"strings"
	"time"
)

// Freight bill states reached by the reconciliation decisions (BR-34.4). A bill
// is UNMATCHED until a carrier invoice matches it (MATCHED or VARIANCE); SETTLED
// is set when the wave-19 AP invoice is posted.
const (
	BillDisputed = "DISPUTED"
	BillApproved = "APPROVED"
	BillSettled  = "SETTLED"
)

// CodeBillState is returned when a decision does not fit the bill's state
// (slice-local, listed in the README).
const CodeBillState = "TMS-0117"

// MaxReasonLength bounds a dispute or approval reason.
const MaxReasonLength = 500

// Decision actions on a freight bill.
const (
	ActionApprove = "APPROVE"
	ActionDispute = "DISPUTE"
)

// Decision is one approval or dispute of a freight bill, kept as an audit trail:
// who decided, when, why, and the state it moved the bill from and to.
type Decision struct {
	ID            string    `json:"id"`
	FreightBillID string    `json:"freightBillId"`
	Action        string    `json:"action"`
	FromStatus    string    `json:"fromStatus"`
	ToStatus      string    `json:"toStatus"`
	Reason        string    `json:"reason,omitempty"`
	DecidedBy     string    `json:"decidedBy"`
	DecidedAt     time.Time `json:"decidedAt"`
}

// Decide applies action to a bill in status and returns the next status.
func Decide(action, status, reason string) (string, error) {
	if action == ActionDispute {
		if strings.TrimSpace(reason) == "" {
			return "", &Error{Code: CodeBillState, Detail: "a dispute needs a reason for the carrier"}
		}
		return DisputeBill(status)
	}
	return ApproveBill(status, reason)
}

// ApproveBill returns the state after approval and whether a reason is needed.
// MATCHED approves as is; VARIANCE (the variance is accepted) and DISPUTED (the
// dispute is closed with the invoiced amount, the "APPROVED with adjustment" path)
// need a reason so the decision is auditable. UNMATCHED, APPROVED and SETTLED
// bills cannot be approved: there is nothing to approve, or it was approved.
func ApproveBill(status, reason string) (string, error) {
	switch status {
	case MatchMatched:
		return BillApproved, nil
	case MatchVariance, BillDisputed:
		if strings.TrimSpace(reason) == "" {
			return "", &Error{Code: CodeBillState, Detail: "approving a " + strings.ToLower(status) + " freight bill needs a reason"}
		}
		return BillApproved, nil
	}
	return "", &Error{Code: CodeBillState, Detail: "a " + strings.ToLower(status) + " freight bill cannot be approved"}
}

// DisputeBill returns DISPUTED for a VARIANCE bill. Only a variance can be
// disputed with the carrier; a matched bill is within tolerance.
func DisputeBill(status string) (string, error) {
	if status == MatchVariance {
		return BillDisputed, nil
	}
	return "", &Error{Code: CodeBillState, Detail: "only a freight bill with a variance can be disputed; this one is " + strings.ToLower(status)}
}
