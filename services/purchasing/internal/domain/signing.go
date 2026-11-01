package domain

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Signing limit codes (PRD 3.17.18 and the signing-limit codes of this service).
const (
	// CodeSigningExpired (PRC-SL-001): the worker's limit is past its recertification date.
	CodeSigningExpired = "PRC-SL-001"
	// CodeSigningApprovalLimit (PRC-SL-002): the amount is above the approver's approval limit.
	CodeSigningApprovalLimit = "PRC-SL-002"
	// CodeSigningSelfDecision (PRC-SL-003): a worker may not decide or recertify their own limit.
	CodeSigningSelfDecision = "PRC-SL-003"
)

// Signing limit document types (FR-17.17). INVOICE and EXPENSE are consumed by waves 18 and 43.
const (
	DocRequisition   = "REQUISITION"
	DocPurchaseOrder = "PURCHASE_ORDER"
	DocInvoice       = "INVOICE"
	DocExpense       = "EXPENSE"
)

// Signing limit request statuses.
const (
	SigningPending  = "PENDING"
	SigningApproved = "APPROVED"
	SigningRejected = "REJECTED"
)

// RecertificationMonths is how long an approved or recertified limit holds (PRD 3.17.10
// signing_limit_recertification_months, default 12; the setting itself is not stored yet).
const RecertificationMonths = 12

// ValidSigningDocType reports whether t is a signing limit document type.
func ValidSigningDocType(t string) bool {
	return t == DocRequisition || t == DocPurchaseOrder || t == DocInvoice || t == DocExpense
}

// SigningLimit is a worker's spending and approval limit for a document type and currency.
type SigningLimit struct {
	ID                string          `json:"id"`
	LegalEntityID     string          `json:"legalEntityId"`
	WorkerID          string          `json:"workerId"`
	DocumentType      string          `json:"documentType"`
	Currency          string          `json:"currency"`
	SpendingLimit     decimal.Decimal `json:"spendingLimit"`
	ApprovalLimit     decimal.Decimal `json:"approvalLimit"`
	RecertifyBy       Day             `json:"recertifyBy"`
	LastRecertifiedOn *Day            `json:"lastRecertifiedOn,omitempty"`
	Version           int             `json:"version"`
	CreatedBy         string          `json:"createdBy,omitempty"`
	CreatedAt         time.Time       `json:"createdAt"`
	UpdatedBy         string          `json:"updatedBy,omitempty"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

// Expired reports whether the limit is past its recertification date on d.
func (l SigningLimit) Expired(d Day) bool { return d.After(l.RecertifyBy.Time) }

// SigningLimitRequest asks for a limit to be set or changed; approving it writes the limit.
type SigningLimitRequest struct {
	ID             string          `json:"id"`
	LegalEntityID  string          `json:"legalEntityId"`
	WorkerID       string          `json:"workerId"`
	DocumentType   string          `json:"documentType"`
	Currency       string          `json:"currency"`
	SpendingLimit  decimal.Decimal `json:"spendingLimit"`
	ApprovalLimit  decimal.Decimal `json:"approvalLimit"`
	Reason         string          `json:"reason,omitempty"`
	Status         string          `json:"status"`
	RequestedBy    string          `json:"requestedBy"`
	RequestedAt    time.Time       `json:"requestedAt"`
	DecidedBy      string          `json:"decidedBy,omitempty"`
	DecidedAt      *time.Time      `json:"decidedAt,omitempty"`
	DecisionNote   string          `json:"decisionNote,omitempty"`
	SigningLimitID string          `json:"signingLimitId,omitempty"`
	Version        int             `json:"version"`
}

// SigningWarning names a signing limit finding that did not stop the document.
type SigningWarning struct {
	Code   string
	Detail string
}

// limitFor picks the limit in the document currency. A limit in another currency is not
// converted (the rate source is wave 12), so it cannot be compared with the amount.
func limitFor(limits []SigningLimit, currency string) *SigningLimit {
	for i := range limits {
		if limits[i].Currency == currency {
			return &limits[i]
		}
	}
	return nil
}

// currencies lists the currencies of limits, for a refusal that names them.
func currencies(limits []SigningLimit) string {
	out := make([]string, 0, len(limits))
	for _, l := range limits {
		out = append(out, l.Currency)
	}
	return strings.Join(out, ", ")
}

// RequesterEscalates applies the spending limit of the requester (BR-17.15): an amount above
// it, or any amount while the limit is past its recertification date (PRC-SL-001), needs one
// more approval level rather than a rejection. A requester whose limits are all in other
// currencies also gets the level, because the amount cannot be converted yet. A requester
// with no limit for the document type at all is not escalated until default signing limits
// by job exist (a later slice); the warning is set for an expired limit.
func RequesterEscalates(limits []SigningLimit, amount decimal.Decimal, currency string, today Day) (bool, *SigningWarning) {
	if len(limits) == 0 {
		return false, nil
	}
	l := limitFor(limits, currency)
	if l == nil {
		return amount.IsPositive(), nil
	}
	if l.Expired(today) {
		return true, &SigningWarning{Code: CodeSigningExpired, Detail: "signing limit expired on " + l.RecertifyBy.String() +
			"; approval is routed one level up until it is recertified"}
	}
	return amount.GreaterThan(l.SpendingLimit), nil
}

// CheckApprover applies the approval limit of the approving worker (BR-17.15): an approver
// whose approval limit is below the amount, or whose limit is past its recertification date,
// cannot approve and the document stays with the next approver. The rule fails closed: an
// approver with no limit for the document type in the document currency has an approval
// limit of zero (PRC-POL-001: signing limits exhausted until a limit is set), and a limit in
// another currency is not converted, so it does not let the approver approve.
func CheckApprover(limits []SigningLimit, amount decimal.Decimal, currency string, today Day) error {
	l := limitFor(limits, currency)
	if l == nil {
		if !amount.IsPositive() {
			return nil
		}
		if len(limits) > 0 {
			return fail(CodeSigningApprovalLimit, "you have no approval limit in %s (your limits are in %s and amounts are not converted between currencies); the next approver must approve",
				currency, currencies(limits))
		}
		return fail(CodeSigningApprovalLimit, "you have no approval limit for this document type in %s; a limit must be approved for you before you approve %s %s",
			currency, amount.StringFixed(2), currency)
	}
	if l.Expired(today) {
		return fail(CodeSigningExpired, "your signing limit expired on %s; it must be recertified before you approve", l.RecertifyBy)
	}
	if amount.GreaterThan(l.ApprovalLimit) {
		return fail(CodeSigningApprovalLimit, "the amount %s %s is above your approval limit of %s %s; the next approver must approve",
			amount.StringFixed(2), currency, l.ApprovalLimit.StringFixed(2), currency)
	}
	return nil
}

// RecertifyDate is the next recertification date for a limit approved or recertified on d.
func RecertifyDate(d Day) Day { return Day{Time: d.AddDate(0, RecertificationMonths, 0)} }

// RoleNextApprover is the extra approval level BR-17.15 adds when the requester's spending
// limit is below the amount; the workflow resolves it to the approver above the chain.
const RoleNextApprover = "NEXT_APPROVER"
