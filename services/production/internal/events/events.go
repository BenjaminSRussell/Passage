// Package events holds the production event types and payloads.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded context name used in event types.
const Context = "production"

// Event types. The kernel naming rule allows no hyphen, so the PRD's
// order.status-changed is order.statuschanged (see NOT-IMPLEMENTED.md).
var (
	OrderCreated       = events.Type(Context, "order", "created", 1)
	OrderUpdated       = events.Type(Context, "order", "updated", 1)
	OrderStatusChanged = events.Type(Context, "order", "statuschanged", 1)
	OrderSettled       = events.Type(Context, "order", "settled", 1)
	JournalPosted      = events.Type(Context, "journal", "posted", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{OrderCreated, OrderUpdated, OrderStatusChanged, OrderSettled, JournalPosted}
}

// OrderCreatedData is the payload of order.created.v1.
type OrderCreatedData struct {
	OrderID      string  `json:"orderId"`
	OrderNumber  string  `json:"orderNumber"`
	ProductID    string  `json:"productId"`
	Quantity     string  `json:"quantity"`
	SiteID       string  `json:"siteId"`
	DeliveryDate *string `json:"deliveryDate"`
	OriginType   string  `json:"originType"`
	OriginID     *string `json:"originId"`
}

// OrderUpdatedData is the payload of order.updated.v1: the header after a PATCH.
type OrderUpdatedData struct {
	OrderID      string  `json:"orderId"`
	Quantity     string  `json:"quantity"`
	DeliveryDate *string `json:"deliveryDate"`
	Status       string  `json:"status"`
	Version      int     `json:"version"`
	Actor        string  `json:"actor"`
}

// OrderStatusChangedData is the payload of order.statuschanged.v1.
type OrderStatusChangedData struct {
	OrderID    string `json:"orderId"`
	From       string `json:"from"`
	To         string `json:"to"`
	Quantity   string `json:"quantity"`
	QtyRAF     string `json:"qtyRaf"`
	QtyError   string `json:"qtyError"`
	Actor      string `json:"actor"`
	OccurredAt string `json:"occurredAt"`
}

// JournalLine is one posted line of journal.posted.v1: an issue of a BOM line,
// hours on an operation, or the finished receipt. Quantities and amounts are
// decimal strings, negative on a reversal journal.
type JournalLine struct {
	Kind       string  `json:"kind"`
	LineNo     *string `json:"lineNo"`
	OperNumber *int    `json:"operNumber"`
	ItemID     *string `json:"itemId"`
	Qty        string  `json:"qty"`
	UnitCost   string  `json:"unitCost"`
	Amount     string  `json:"amount"`
	Hours      *string `json:"hours"`
}

// Posting is one ledger line of a journal or settlement, for the general
// ledger (wave 11) to book.
type Posting struct {
	PostingType string `json:"postingType"`
	Account     string `json:"account"`
	Debit       string `json:"debit"`
	Credit      string `json:"credit"`
}

// JournalPostedData is the payload of journal.posted.v1 (PRD 4.21.5).
// JournalEntryID stays null until the general ledger (wave 11) books the postings.
type JournalPostedData struct {
	JournalID      string        `json:"journalId"`
	JournalNumber  string        `json:"journalNumber"`
	OrderID        string        `json:"orderId"`
	Type           string        `json:"type"`
	PostingDate    string        `json:"postingDate"`
	Currency       string        `json:"currency"`
	ReversalOf     *string       `json:"reversalOf"`
	Lines          []JournalLine `json:"lines"`
	Postings       []Posting     `json:"postings"`
	JournalEntryID *string       `json:"journalEntryId"`
}

// CostByGroup is planned or actual cost per cost group, to the currency's minor unit.
type CostByGroup struct {
	Material    string `json:"material"`
	Labor       string `json:"labor"`
	Overhead    string `json:"overhead"`
	Subcontract string `json:"subcontract"`
}

// VarianceAmount is one variance row of order.settled.v1.
type VarianceAmount struct {
	Type      string `json:"type"`
	CostGroup string `json:"costGroup"`
	Amount    string `json:"amount"`
}

// OrderSettledData is the payload of order.settled.v1 (PRD 4.21.5): planned
// and actual cost by group, the variances that cleared the WIP balance, and
// the settlement postings.
type OrderSettledData struct {
	OrderID     string           `json:"orderId"`
	Currency    string           `json:"currency"`
	PostingDate string           `json:"postingDate"`
	QtyRAF      string           `json:"qtyRaf"`
	WIPCleared  string           `json:"wipCleared"`
	PlannedCost CostByGroup      `json:"plannedCost"`
	ActualCost  CostByGroup      `json:"actualCost"`
	Variances   []VarianceAmount `json:"variances"`
	Postings    []Posting        `json:"postings"`
}
