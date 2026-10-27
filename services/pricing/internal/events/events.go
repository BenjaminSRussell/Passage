// Package events holds the CloudEvent type names and payloads the pricing
// service publishes. Contract: events/asyncapi.yaml.
package events

// Event types. The kernel regex allows three segments after the namespace, so the
// catalog's condition-record.activated collapses to conditionrecord.activated (as org does).
const (
	ConditionRecordActivated = "dev.passage.pricing.conditionrecord.activated.v1"
	ConditionRecordWithdrawn = "dev.passage.pricing.conditionrecord.withdrawn.v1"
	JournalPosted            = "dev.passage.pricing.journal.posted.v1"
	RebateAccrualPosted      = "dev.passage.pricing.rebateaccrual.posted.v1"
	CommissionCalculated     = "dev.passage.pricing.commission.calculated.v1"
)

// JournalPostedData is the payload of journal.posted (post and reverse, FR-26.9).
type JournalPostedData struct {
	JournalID     string `json:"journal_id"`
	LegalEntityID string `json:"legal_entity_id"`
	LineCount     int    `json:"line_count"`
	// ReversedJournalID is set when this journal reverses another.
	ReversedJournalID string `json:"reversed_journal_id,omitempty"`
}

// RebateAccrualPostedData is the payload of rebateaccrual.posted. The ledger (w11) posts
// Dr DebitAccount / Cr CreditAccount for Amount when it consumes the event (FR-26.11;
// 4150 / 2150 unless the agreement names other accounts). Amount is negative for a
// credit note or a tier recompute that lowers the period.
type RebateAccrualPostedData struct {
	AccrualID     string `json:"accrual_id"`
	LegalEntityID string `json:"legal_entity_id"`
	AgreementID   string `json:"agreement_id"`
	PeriodID      string `json:"period_id"`
	InvoiceLineID string `json:"invoice_line_id"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	DebitAccount  string `json:"debit_account"`
	CreditAccount string `json:"credit_account"`
	// Retroactive is true for the per-period delta of a retroactive recompute (BR-26.7, FR-26.23).
	Retroactive bool `json:"retroactive"`
}

// CommissionCalculatedData is the payload of commission.calculated (FR-26.12, FR-26.18):
// one rep's commission on one invoice line. The ledger (w11) posts Dr DebitAccount /
// Cr CreditAccount for Amount (6200 / 2170); Amount is negative on a credit note, which
// names the line it reverses. It carries the rep id only: rep names never appear in
// events (PRD §5.26.20).
type CommissionCalculatedData struct {
	TransactionID         string `json:"transaction_id"`
	LegalEntityID         string `json:"legal_entity_id"`
	InvoiceLineID         string `json:"invoice_line_id"`
	ReversesInvoiceLineID string `json:"reverses_invoice_line_id,omitempty"`
	SalesGroupID          string `json:"sales_group_id"`
	SalesRepID            string `json:"sales_rep_id"`
	Basis                 string `json:"basis"`
	BasisAmount           string `json:"basis_amount"`
	Rate                  string `json:"rate"`
	Amount                string `json:"amount"`
	Currency              string `json:"currency"`
	PeriodID              string `json:"period_id"`
	DebitAccount          string `json:"debit_account"`
	CreditAccount         string `json:"credit_account"`
}

// ConditionRecordData is the payload of conditionrecord.activated. Keys are
// carried, never customer names or personal data.
type ConditionRecordData struct {
	ID            string `json:"id"`
	LegalEntityID string `json:"legal_entity_id"`
	Type          string `json:"type"`
	CustomerKind  string `json:"customer_kind"`
	CustomerKey   string `json:"customer_key"`
	ProductKind   string `json:"product_kind"`
	ProductKey    string `json:"product_key"`
	Variant       string `json:"variant"`
	Currency      string `json:"currency"`
	Unit          string `json:"unit"`
	FromQuantity  string `json:"from_quantity"`
	ValidFrom     string `json:"valid_from"`
	ValidTo       string `json:"valid_to"`
	Version       int    `json:"version"`
	// Retroactive is true when valid_from is before the activation day (BR-26.7).
	Retroactive bool `json:"retroactive"`
	// JournalID names the price journal that posted or reversed the record; empty for a single activation.
	JournalID string `json:"journal_id,omitempty"`
}

// ConditionRecordWithdrawnData is the payload of conditionrecord.withdrawn.
type ConditionRecordWithdrawnData struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Reason  string `json:"reason"`
}
