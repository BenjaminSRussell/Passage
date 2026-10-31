// Package events holds the event types and payloads the warehouse service
// publishes (PRD §6.31.5). Schemas are in events/asyncapi.yaml. Payload fields
// are snake_case like the PRD; quantities are decimal strings.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the event context the PRD names for warehouse management.
const Context = "wms"

// Source is the CloudEvents source of this service.
const Source = "warehouse"

// Event types published so far.
var (
	WorkCreated       = events.Type(Context, "work", "created", 1)
	WorkCompleted     = events.Type(Context, "work", "completed", 1)
	WorkReleased      = events.Type(Context, "work", "released", 1)
	ReceiptRegistered = events.Type(Context, "receipt", "registered", 1)
)

// All lists every published type; asyncapi.yaml must name each one.
func All() []string { return []string{WorkCreated, WorkCompleted, WorkReleased, ReceiptRegistered} }

// Line is one line of a work event.
type Line struct {
	LineNo         int    `json:"line_no"`
	ItemID         string `json:"item_id"`
	Direction      string `json:"direction"`
	Qty            string `json:"qty"`
	ConfirmedQty   string `json:"confirmed_qty"`
	Status         string `json:"status"`
	FromLocationID string `json:"from_location_id,omitempty"`
	ToLocationID   string `json:"to_location_id,omitempty"`
}

// WorkCreatedData is the payload of dev.passage.wms.work.created.v1, emitted
// when a work header is committed, including a short-pick remainder.
type WorkCreatedData struct {
	WorkID       string `json:"work_id"`
	WorkType     string `json:"work_type"`
	WarehouseID  string `json:"warehouse_id"`
	Priority     int    `json:"priority"`
	Status       string `json:"status"`
	ParentWorkID string `json:"parent_work_id,omitempty"`
	Lines        []Line `json:"lines"`
}

// WorkCompletedData is the payload of dev.passage.wms.work.completed.v1,
// emitted when the last open line is confirmed or short picked: the work is
// COMPLETED or PARTIALLY_COMPLETED. Inventory posts the confirmed quantities.
type WorkCompletedData struct {
	WorkID      string `json:"work_id"`
	WorkType    string `json:"work_type"`
	WarehouseID string `json:"warehouse_id"`
	Status      string `json:"status"`
	User        string `json:"user"`
	DurationS   int64  `json:"duration_s"`
	Lines       []Line `json:"lines"`
}

// WorkReleasedData is the payload of dev.passage.wms.work.released.v1, emitted
// when a WAITING work becomes OPEN because the work it depended on ended:
// AfterStatus is that work's terminal status (COMPLETED or CANCELLED).
type WorkReleasedData struct {
	WorkID      string `json:"work_id"`
	WarehouseID string `json:"warehouse_id"`
	AfterWorkID string `json:"after_work_id"`
	AfterStatus string `json:"after_status"`
}

// ItemQty is a quantity of one item.
type ItemQty struct {
	ItemID string `json:"item_id"`
	Qty    string `json:"qty"`
}

// ReceiptRegisteredData is the payload of dev.passage.wms.receipt.registered.v1,
// emitted when a received ASN pallet is committed (FR-31.9): one event per
// pallet, so purchasing and quality see each receipt as it happens. Quantities
// are what arrived; the variance is against the ASN (BR-31.10).
type ReceiptRegisteredData struct {
	ReceiptID   string    `json:"receipt_id"`
	WarehouseID string    `json:"warehouse_id"`
	ASNRef      string    `json:"asn_ref"`
	LineNo      int       `json:"line_no"`
	POLineRefs  []string  `json:"po_line_refs"`
	LPIDs       []string  `json:"lp_ids"`
	Items       []ItemQty `json:"items"`
	ExpectedQty string    `json:"expected_qty"`
	VarianceQty string    `json:"variance_qty"`
	VariancePct string    `json:"variance_pct"`
	Discrepancy bool      `json:"discrepancy"`
	WorkID      string    `json:"work_id"`
	User        string    `json:"user"`
}
