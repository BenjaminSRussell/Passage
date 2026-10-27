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
)

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
