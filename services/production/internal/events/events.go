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
	AsBuiltAssigned    = events.Type(Context, "asbuilt", "assigned", 1)
	AsBuiltDetached    = events.Type(Context, "asbuilt", "detached", 1)
	// OrderBOMEdited reports an order-specific BOM or route edit (FR-21.18).
	OrderBOMEdited = events.Type(Context, "order", "bomedited", 1)
	// TriggerPointFired reports a trigger point evaluated as due on route-card feedback
	// (FR-21.17): what its function did, or why it failed (PROD-0019).
	TriggerPointFired = events.Type(Context, "triggerpoint", "fired", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{OrderCreated, OrderUpdated, OrderStatusChanged, OrderSettled, JournalPosted, AsBuiltAssigned, AsBuiltDetached, OrderBOMEdited, TriggerPointFired}
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
	// ReworkReference is the original order, batch or serial of a rework order (FR-21.20); null otherwise.
	ReworkReference *ReworkReference `json:"reworkReference"`
}

// ReworkReference names what a rework order reports against: type order with
// the order id, or batch or serial with its number.
type ReworkReference struct {
	Type string `json:"type"`
	ID   string `json:"id"`
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

// AsBuiltData is the payload of asbuilt.assigned.v1 and asbuilt.detached.v1
// (FR-21.19): one component serial or batch consumed into a finished serial or
// batch, for the trace inquiry (wave 16) and nonconformance (wave 33).
// DetachedAt is null on assigned.
type AsBuiltData struct {
	RecordID               string  `json:"recordId"`
	OrderID                string  `json:"orderId"`
	FinishedItemID         string  `json:"finishedItemId"`
	FinishedSerialOrBatch  string  `json:"finishedSerialOrBatch"`
	ComponentItemID        string  `json:"componentItemId"`
	Tracking               string  `json:"tracking"`
	ComponentSerialOrBatch string  `json:"componentSerialOrBatch"`
	Qty                    string  `json:"qty"`
	AssignedBy             string  `json:"assignedBy"`
	AssignedAt             string  `json:"assignedAt"`
	DetachedAt             *string `json:"detachedAt"`
}

// EditApplied names one applied edit of order.bomedited.v1: the target (line or
// operation), the op (add, remove, change) and the line or operation number.
type EditApplied struct {
	Target string `json:"target"`
	Op     string `json:"op"`
	Key    string `json:"key"`
}

// RequirementLine is a BOM line requirement after an edit, for planning.
type RequirementLine struct {
	LineNo      string `json:"lineNo"`
	ItemID      string `json:"itemId"`
	QtyRequired string `json:"qtyRequired"`
	Flushing    string `json:"flushing"`
}

// OrderBOMEditedData is the payload of order.bomedited.v1 (FR-21.18): the edits
// applied, the order's requirement lines after them and the re-estimated planned
// cost (null while the order is not estimated).
type OrderBOMEditedData struct {
	OrderID     string            `json:"orderId"`
	Status      string            `json:"status"`
	Version     int               `json:"version"`
	Edits       []EditApplied     `json:"edits"`
	Lines       []RequirementLine `json:"lines"`
	PlannedCost *CostByGroup      `json:"plannedCost"`
	Actor       string            `json:"actor"`
}

// TriggerFeedback is the route-card feedback a trigger point's condition was evaluated
// against: the hours, good and error quantity reported on its operation.
type TriggerFeedback struct {
	Hours    string `json:"hours"`
	QtyGood  string `json:"qtyGood"`
	QtyError string `json:"qtyError"`
}

// TriggerPointFiredData is the payload of triggerpoint.fired.v1 (FR-21.17, BR-21.13).
// Status is fired or failed; a failed function carries the reason and changed nothing.
// InsertedOperations are the operation numbers insert-operations added; ReworkOrderID is
// the order create-rework-order created. notify does nothing but publish this event.
type TriggerPointFiredData struct {
	FiringID           string          `json:"firingId"`
	TriggerPointID     string          `json:"triggerPointId"`
	OrderID            string          `json:"orderId"`
	JournalID          string          `json:"journalId"`
	OperNumber         int             `json:"operNumber"`
	Sequence           int             `json:"sequence"`
	Condition          string          `json:"condition"`
	Function           string          `json:"function"`
	Status             string          `json:"status"`
	Reason             *string         `json:"reason"`
	Feedback           TriggerFeedback `json:"feedback"`
	InsertedOperations []int           `json:"insertedOperations"`
	ReworkOrderID      *string         `json:"reworkOrderId"`
	Actor              string          `json:"actor"`
}
