// Package events holds the receivables event types and payloads (AsyncAPI in events/asyncapi.yaml).
package events

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// Context is the bounded context segment of every event type.
const Context = "receivables"

var (
	PaymentReceived = events.Type(Context, "payment", "received", 1)
	PaymentApplied  = events.Type(Context, "payment", "applied", 1)
	DisputeOpened   = events.Type(Context, "dispute", "opened", 1)
	DisputeResolved = events.Type(Context, "dispute", "resolved", 1)
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
