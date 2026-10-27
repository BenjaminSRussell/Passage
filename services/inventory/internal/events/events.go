// Package events holds the event type names and payloads this service publishes.
// Schemas are documented in events/asyncapi.yaml.
package events

// Event types (dev.passage.<context>.<entity>.<verb>.v<n>).
const (
	TransactionPosted   = "dev.passage.inventory.transaction.posted.v1"
	OnhandChanged       = "dev.passage.inventory.onhand.changed.v1"
	JournalPosted       = "dev.passage.inventory.journal.posted.v1"
	ReservationCreated  = "dev.passage.inventory.reservation.created.v1"
	ReservationReleased = "dev.passage.inventory.reservation.released.v1"
	BlockingCreated     = "dev.passage.inventory.blocking.created.v1"
	BlockingReleased    = "dev.passage.inventory.blocking.released.v1"
)

// All lists every event type the service publishes.
func All() []string {
	return []string{TransactionPosted, OnhandChanged, JournalPosted, ReservationCreated, ReservationReleased, BlockingCreated, BlockingReleased,
		TransferShipped, TransferReceived}
}

// On-hand buckets an onhand.changed event names as moved. BucketPosted is
// posted quantity and value (financial statuses SOLD / PURCHASED), moved by
// journals and by blocking legs; BucketReservedPhysical is moved by
// reservations.
const (
	BucketPosted           = "POSTED"
	BucketReservedPhysical = "RESERVED_PHYSICAL"
)

// TransactionPostedData is one inventory transaction written by a posting.
// Cost amounts are signed, in Currency (the accounting currency), rounded to
// its minor unit. SourceType is the origin type (INVENT_JOURNAL_LINE,
// BLOCKING); JournalID is set for journal lines and ReferenceID for the other
// documents (the blocking id).
type TransactionPostedData struct {
	TransactionID      string            `json:"transactionId"`
	LegalEntityID      string            `json:"legalEntityId"`
	SourceType         string            `json:"sourceType"`
	JournalID          *string           `json:"journalId"`
	ReferenceID        *string           `json:"referenceId"`
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
	// CorrectsTransactionID is the transaction a reversal line negates (BR-16.12).
	CorrectsTransactionID *string `json:"correctsTransactionId"`
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
	// ReversesJournalID is set when the posted journal reverses another (BR-16.12).
	ReversesJournalID *string `json:"reversesJournalId"`
}

// ReservationData is the payload of both reservation events: one allocation
// of a reservation request.
type ReservationData struct {
	ReservationID string            `json:"reservationId"`
	LegalEntityID string            `json:"legalEntityId"`
	ProductID     string            `json:"productId"`
	DimensionSet  map[string]string `json:"dimensionSet"`
	Qty           string            `json:"qty"`
	DemandType    string            `json:"demandType"`
	DemandID      string            `json:"demandId"`
	Policy        string            `json:"policy"`
	Status        string            `json:"status"`
}

// BlockingData is the payload of both blocking events. DimensionSet is the
// unrestricted source; the blocked stock sits at the same dimensions with
// inventory_status = targetStatus. Disposition is null while ACTIVE.
type BlockingData struct {
	BlockingID    string            `json:"blockingId"`
	LegalEntityID string            `json:"legalEntityId"`
	ProductID     string            `json:"productId"`
	DimensionSet  map[string]string `json:"dimensionSet"`
	Qty           string            `json:"qty"`
	TargetStatus  string            `json:"targetStatus"`
	Reason        string            `json:"reason"`
	State         string            `json:"state"`
	Disposition   *string           `json:"disposition"`
}
