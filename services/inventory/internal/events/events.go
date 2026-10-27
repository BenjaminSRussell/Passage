// Package events holds the event type names and payloads this service publishes.
// Schemas are documented in events/asyncapi.yaml.
package events

// Event types (dev.passage.<context>.<entity>.<verb>.v<n>).
const (
	TransactionPosted = "dev.passage.inventory.transaction.posted.v1"
	OnhandChanged     = "dev.passage.inventory.onhand.changed.v1"
	JournalPosted     = "dev.passage.inventory.journal.posted.v1"
)

// All lists every event type the service publishes.
func All() []string { return []string{TransactionPosted, OnhandChanged, JournalPosted} }

// BucketPosted is the on-hand bucket a journal posting moves: posted quantity
// and value (financial statuses SOLD / PURCHASED).
const BucketPosted = "POSTED"

// TransactionPostedData is one inventory transaction written by a posting.
// Cost amounts are signed, in Currency (the accounting currency), rounded to
// its minor unit.
type TransactionPostedData struct {
	TransactionID      string            `json:"transactionId"`
	LegalEntityID      string            `json:"legalEntityId"`
	JournalID          string            `json:"journalId"`
	OriginID           string            `json:"originId"`
	ProductID          string            `json:"productId"`
	DimensionSet       map[string]string `json:"dimensionSet"`
	Qty                string            `json:"qty"`
	IssueStatus        *string           `json:"issueStatus"`
	ReceiptStatus      *string           `json:"receiptStatus"`
	PhysicalDate       string            `json:"physicalDate"`
	FinancialDate      string            `json:"financialDate"`
	Currency           string            `json:"currency"`
	CostAmountPhysical string            `json:"costAmountPhysical"`
	CostAmountPosted   string            `json:"costAmountPosted"`
}

// OnhandChangedData is the projection row after a posting changed it.
type OnhandChangedData struct {
	LegalEntityID  string            `json:"legalEntityId"`
	ProductID      string            `json:"productId"`
	DimensionSet   map[string]string `json:"dimensionSet"`
	AvailPhysical  string            `json:"availPhysical"`
	AvailOrdered   string            `json:"availOrdered"`
	PhysicalInvent string            `json:"physicalInvent"`
	DeltaBucket    string            `json:"deltaBucket"`
	DeltaQty       string            `json:"deltaQty"`
}

// GLLineData is one ledger line of a journal entry.
type GLLineData struct {
	Account string `json:"account"`
	Debit   string `json:"debit"`
	Credit  string `json:"credit"`
}

// JournalPostedData summarizes a posted journal and the balanced GL entry that
// the ledger (wave 11) will post. LedgerEntryID stays null until that exists.
type JournalPostedData struct {
	JournalID     string       `json:"journalId"`
	LegalEntityID string       `json:"legalEntityId"`
	JournalType   string       `json:"journalType"`
	PostingDate   string       `json:"postingDate"`
	Currency      string       `json:"currency"`
	LineCount     int          `json:"lineCount"`
	GLLines       []GLLineData `json:"glLines"`
	LedgerEntryID *string      `json:"ledgerEntryId"`
}
