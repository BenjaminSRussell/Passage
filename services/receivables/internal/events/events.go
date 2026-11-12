// Package events holds the receivables event types and payloads (AsyncAPI in events/asyncapi.yaml).
package events

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

// Context is the bounded context segment of every event type.
const Context = "receivables"

var (
	PaymentReceived = events.Type(Context, "payment", "received", 1)
	PaymentApplied  = events.Type(Context, "payment", "applied", 1)
	// PaymentUnapplied is published when a payment's applications are reversed (#30): the items it settled are open again.
	PaymentUnapplied = events.Type(Context, "payment", "unapplied", 1)
	DisputeOpened    = events.Type(Context, "dispute", "opened", 1)
	DisputeResolved  = events.Type(Context, "dispute", "resolved", 1)
	// DunningLetterIssued is the PRD's dunning-letter.issued.v1; the kernel's
	// naming rule allows no hyphen, so the entity is "dunningletter".
	DunningLetterIssued = events.Type(Context, "dunningletter", "issued", 1)
)

// DisputeOpenedData is the payload of dev.passage.receivables.dispute.opened.v1.
type DisputeOpenedData struct {
	DisputeID     string          `json:"dispute_id"`
	LegalEntityID string          `json:"legal_entity_id"`
	CustomerID    string          `json:"customer_id"`
	OpenItemID    string          `json:"open_item_id"`
	Amount        decimal.Decimal `json:"amount"`
	Currency      string          `json:"currency"`
	ReasonCode    string          `json:"reason_code"`
}

// DisputeResolvedData is the payload of dev.passage.receivables.dispute.resolved.v1.
type DisputeResolvedData struct {
	DisputeID     string          `json:"dispute_id"`
	LegalEntityID string          `json:"legal_entity_id"`
	CustomerID    string          `json:"customer_id"`
	OpenItemID    string          `json:"open_item_id"`
	Amount        decimal.Decimal `json:"amount"`
	Currency      string          `json:"currency"`
	ReasonCode    string          `json:"reason_code"`
	Resolution    string          `json:"resolution"`
}

// LetterItemData is one open item on an issued dunning letter.
type LetterItemData struct {
	OpenItemID  string          `json:"open_item_id"`
	OpenAmount  decimal.Decimal `json:"open_amount"`
	DaysOverdue int             `json:"days_overdue"`
}

// DunningLetterIssuedData is the payload of
// dev.passage.receivables.dunningletter.issued.v1, one event per issued letter
// (§5.30.5: run_id, customer_id, level, items[], fee, document_ref).
type DunningLetterIssuedData struct {
	RunID         string           `json:"run_id"`
	LetterID      string           `json:"letter_id"`
	LegalEntityID string           `json:"legal_entity_id"`
	CustomerID    string           `json:"customer_id"`
	Currency      string           `json:"currency"`
	Level         int              `json:"level"`
	Items         []LetterItemData `json:"items"`
	Fee           decimal.Decimal  `json:"fee"`
	DocumentRef   string           `json:"document_ref"`
}

// PaymentReceivedData is the payload of dev.passage.receivables.payment.received.v1.
type PaymentReceivedData struct {
	PaymentID     string           `json:"payment_id"`
	LegalEntityID string           `json:"legal_entity_id"`
	CustomerID    *string          `json:"customer_id"`
	Source        string           `json:"source"`
	Amount        decimal.Decimal  `json:"amount"`
	Currency      string           `json:"currency"`
	Remittance    []RemittanceLine `json:"remittance"`
}

// RemittanceLine mirrors one remittance reference.
type RemittanceLine struct {
	Reference string          `json:"reference"`
	Amount    decimal.Decimal `json:"amount"`
}

// SettlementData is one allocation inside PaymentAppliedData.
type SettlementData struct {
	OpenItemID   string          `json:"open_item_id"`
	Amount       decimal.Decimal `json:"amount"`
	CashDiscount decimal.Decimal `json:"cash_discount"`
	WriteOff     decimal.Decimal `json:"write_off"`
	// ItemCurrency and ItemAmount are set for a cross-currency settlement: the item's currency and what the settlement took off the
	// item in it (amount is then the cash in the payment currency).
	ItemCurrency string          `json:"item_currency,omitempty"`
	ItemAmount   decimal.Decimal `json:"item_amount,omitempty"`
}

// PaymentAppliedData is the payload of dev.passage.receivables.payment.applied.v1.
type PaymentAppliedData struct {
	PaymentID     string           `json:"payment_id"`
	LegalEntityID string           `json:"legal_entity_id"`
	CustomerID    *string          `json:"customer_id"`
	Currency      string           `json:"currency"`
	Settlements   []SettlementData `json:"settlements"`
	Residual      decimal.Decimal  `json:"residual"`
	Status        string           `json:"status"`
	// JournalPostingID stays null until settlements post through the ledger
	// (NOT-IMPLEMENTED.md, Ledger posting).
	JournalPostingID *string `json:"journal_posting_id"`
}

// Collection cases (FR-30.18), balance confirmations (FR-30.20) and down payments
// and security deposits (FR-30.21). Entity segments are lower-case letters only,
// as the kernel event type rule requires; one "downpayment" entity carries both
// kinds, told apart by the kind field.
var (
	CaseOpened          = events.Type(Context, "case", "opened", 1)
	CaseClosed          = events.Type(Context, "case", "closed", 1)
	ConfirmationReplied = events.Type(Context, "confirmation", "replied", 1)
	DownPaymentReceived = events.Type(Context, "downpayment", "received", 1)
	DownPaymentCleared  = events.Type(Context, "downpayment", "cleared", 1)
	DownPaymentReleased = events.Type(Context, "downpayment", "released", 1)
	// CreditSettled is a credit item (credit note, adjustment, unapplied payment) offset against debit items (30-3).
	CreditSettled = events.Type(Context, "creditsettlement", "applied", 1)
)

// CreditSettledData is the payload of dev.passage.receivables.creditsettlement.applied.v1. Amounts are positive
// magnitudes; nothing moves in the ledger, both sides stay in 1200.
type CreditSettledData struct {
	BatchID       string               `json:"batch_id"`
	LegalEntityID string               `json:"legal_entity_id"`
	CustomerID    string               `json:"customer_id"`
	Currency      string               `json:"currency"`
	CreditItemID  string               `json:"credit_item_id"`
	Total         decimal.Decimal      `json:"total"`
	Debits        []CreditSettledDebit `json:"debits"`
}

// CreditSettledDebit is one debit item reduced by a credit settlement.
type CreditSettledDebit struct {
	OpenItemID string          `json:"open_item_id"`
	Amount     decimal.Decimal `json:"amount"`
	// ItemCurrency and ItemAmount are set for a cross-currency settlement: the debit's currency and what came off it in that
	// currency (amount is then in the credit's currency).
	ItemCurrency string          `json:"item_currency,omitempty"`
	ItemAmount   decimal.Decimal `json:"item_amount,omitempty"`
}

// CaseOpenedData is the payload of dev.passage.receivables.case.opened.v1.
type CaseOpenedData struct {
	CaseID         string           `json:"case_id"`
	LegalEntityID  string           `json:"legal_entity_id"`
	CustomerID     string           `json:"customer_id"`
	Category       string           `json:"category"`
	Priority       string           `json:"priority"`
	Owner          string           `json:"owner"`
	Reason         string           `json:"reason"`
	Currency       string           `json:"currency"`
	OpenItemID     *string          `json:"open_item_id"`
	AttachedAmount *decimal.Decimal `json:"attached_amount"`
	SLADue         time.Time        `json:"sla_due"`
}

// CaseClosedData is the payload of dev.passage.receivables.case.closed.v1.
type CaseClosedData struct {
	CaseID        string  `json:"case_id"`
	LegalEntityID string  `json:"legal_entity_id"`
	CustomerID    string  `json:"customer_id"`
	Category      string  `json:"category"`
	SLABreached   bool    `json:"sla_breached"`
	Note          *string `json:"note"`
}

// ConfirmationRepliedData is the payload of dev.passage.receivables.confirmation.replied.v1.
// CaseID names the DISPUTE case a disagreement opened (BR-30.13).
type ConfirmationRepliedData struct {
	ConfirmationID  string          `json:"confirmation_id"`
	RunID           string          `json:"run_id"`
	LegalEntityID   string          `json:"legal_entity_id"`
	CustomerID      string          `json:"customer_id"`
	Currency        string          `json:"currency"`
	Reply           string          `json:"reply"`
	OurBalance      decimal.Decimal `json:"our_balance"`
	CustomerBalance decimal.Decimal `json:"customer_balance"`
	Difference      decimal.Decimal `json:"difference"`
	CaseID          *string         `json:"case_id"`
}

// DownPaymentData is the payload of the downpayment.received, .cleared and
// .released events. Amount is the amount of this movement; Postings are the
// ledger lines for it, until the ledger client posts them (NOT-IMPLEMENTED.md).
type DownPaymentData struct {
	DownPaymentID string           `json:"downpayment_id"`
	LegalEntityID string           `json:"legal_entity_id"`
	CustomerID    string           `json:"customer_id"`
	SalesOrderID  *string          `json:"sales_order_id"`
	Kind          string           `json:"kind"`
	Amount        decimal.Decimal  `json:"amount"`
	Currency      string           `json:"currency"`
	Remaining     decimal.Decimal  `json:"remaining"`
	OpenItemID    *string          `json:"open_item_id"`
	Postings      []domain.Posting `json:"postings"`
}

// Deductions workbench (FR-30.17). A camt.053 import publishes payment.received.v1
// with source CAMT053; it has no event of its own.
var (
	DeductionOpened   = events.Type(Context, "deduction", "opened", 1)
	DeductionResolved = events.Type(Context, "deduction", "resolved", 1)
)

// DeductionOpenedData is the payload of dev.passage.receivables.deduction.opened.v1:
// the amount moved off the short-paid open item into the workbench.
type DeductionOpenedData struct {
	DeductionID   string          `json:"deduction_id"`
	LegalEntityID string          `json:"legal_entity_id"`
	CustomerID    string          `json:"customer_id"`
	OpenItemID    string          `json:"open_item_id"`
	PaymentID     *string         `json:"payment_id"`
	ParentID      *string         `json:"parent_id"`
	Type          string          `json:"type"`
	ReasonCode    string          `json:"reason_code"`
	Amount        decimal.Decimal `json:"amount"`
	Currency      string          `json:"currency"`
}

// DeductionResolvedData is the payload of dev.passage.receivables.deduction.resolved.v1.
// Resolution is APPROVED (billing issues the credit note against the claim, which
// consumes the fund or accrual), DENIED (RebillItemID is the debit item re-billed
// to the customer), WRITTEN_OFF (Postings carry Dr 6110 / Cr 1200) or SPLIT
// (ChildIDs are the new OPEN deductions).
type DeductionResolvedData struct {
	DeductionID   string           `json:"deduction_id"`
	LegalEntityID string           `json:"legal_entity_id"`
	CustomerID    string           `json:"customer_id"`
	Resolution    string           `json:"resolution"`
	Amount        decimal.Decimal  `json:"amount"`
	Currency      string           `json:"currency"`
	ClaimType     *string          `json:"claim_type"`
	ClaimRef      *string          `json:"claim_ref"`
	DenialReason  *string          `json:"denial_reason"`
	RebillItemID  *string          `json:"rebill_item_id"`
	ChildIDs      []string         `json:"child_ids"`
	Postings      []domain.Posting `json:"postings"`
}

// PaymentUnappliedData is the payload of payment.unapplied.v1: the settlements reversed, as they were applied.
type PaymentUnappliedData struct {
	PaymentID     string           `json:"payment_id"`
	LegalEntityID string           `json:"legal_entity_id"`
	CustomerID    *string          `json:"customer_id"`
	Currency      string           `json:"currency"`
	Settlements   []SettlementData `json:"settlements"`
	Reason        string           `json:"reason,omitempty"`
}
