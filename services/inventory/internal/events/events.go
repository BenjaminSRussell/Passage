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
	BlockingEscalated   = "dev.passage.inventory.blocking.escalated.v1"
	HoldPlaced          = "dev.passage.inventory.hold.placed.v1"
	HoldReleased        = "dev.passage.inventory.hold.released.v1"
)

// All lists every event type the service publishes.
func All() []string {
	return []string{TransactionPosted, OnhandChanged, JournalPosted, ReservationCreated, ReservationReleased, BlockingCreated, BlockingReleased, BlockingEscalated, HoldPlaced, HoldReleased,
		TransferShipped, TransferReceived, TransferChanged, OwnershipChanged, ConsignmentClosed, ReconciliationDiff, CountSheetChanged, JournalNameCreated, JournalNameUpdated}
}

// On-hand buckets an onhand.changed event names as moved. BucketPosted is
// posted quantity and value (financial statuses SOLD / PURCHASED), moved by
// journals and by blocking and transfer legs; BucketReservedPhysical is moved
// by reservations; BucketReceived by vendor-owned consignment receipts and
// BucketDeducted by the vendor-owned leg of an ownership change (FR-16.17).
// A registration or status move through transactions:apply or a named
// backward command names the bucket of the status it reached, which may be any
// status bucket (ON_ORDER, PICKED, ARRIVED, ...), or POSTED for SOLD / PURCHASED.
const (
	BucketPosted           = "POSTED"
	BucketReservedPhysical = "RESERVED_PHYSICAL"
	BucketHeldPhysical     = "HELD_PHYSICAL"
	BucketReceived         = "RECEIVED"
	BucketDeducted         = "DEDUCTED"
)

// TransactionPostedData is one inventory transaction written by a posting.
// Cost amounts are signed, in Currency (the accounting currency), rounded to
// its minor unit. SourceType is the origin type; JournalID is set for journal
// lines (and the receipt leg of an ownership change) and ReferenceID for the
// other documents (blocking, transfer or replenishment order id).
// FinancialDate and CostAmountPosted are null for a physical status (a
// vendor-owned RECEIVED or DEDUCTED transaction, FR-16.11).
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
	FinancialDate      *string           `json:"financialDate"`
	Currency           string            `json:"currency"`
	CostAmountPhysical string            `json:"costAmountPhysical"`
	CostAmountPosted   *string           `json:"costAmountPosted"`
	// CorrectsTransactionID is the transaction a reversal line negates (BR-16.12).
	CorrectsTransactionID *string `json:"correctsTransactionId"`
	// StatusChangeID is the audit row of a registration or status move through
	// transactions:apply or a named backward command; null for document postings.
	StatusChangeID *string `json:"statusChangeId"`
	// PreviousStatus is the status a status move left; null for an insert.
	PreviousStatus *string `json:"previousStatus"`
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
	DemandSegment string            `json:"demandSegment"`
	Policy        string            `json:"policy"`
	Status        string            `json:"status"`
	// Reason is "EXPIRED" when the release came from the expiry run, empty otherwise.
	Reason string `json:"reason,omitempty"`
}

// BlockingData is the payload of the blocking events. DimensionSet is the
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
	// ExpectedReleaseDate is null when none was given; ScrapJournalID is null
	// unless the blocking was scrapped (the ADJUSTMENT journal that wrote it off).
	ExpectedReleaseDate *string `json:"expectedReleaseDate"`
	ScrapJournalID      *string `json:"scrapJournalId"`
}

// HoldData is the payload of both hold events. Qty is the quantity placed,
// ReleasedQty the cumulative quantity released; held stock is their difference.
type HoldData struct {
	HoldID        string            `json:"holdId"`
	LegalEntityID string            `json:"legalEntityId"`
	ProductID     string            `json:"productId"`
	DimensionSet  map[string]string `json:"dimensionSet"`
	Qty           string            `json:"qty"`
	ReleasedQty   string            `json:"releasedQty"`
	Reason        string            `json:"reason"`
	SourceType    string            `json:"sourceType"`
	SourceRef     string            `json:"sourceRef"`
	State         string            `json:"state"`
}
