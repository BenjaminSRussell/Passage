// Package events holds the fulfillment event types and payloads. Event types
// follow the kernel rule dev.passage.<context>.<entity>.<verb>.v<n>; the
// packing-slip entity collapses to "packingslip" and the PRD's
// shipment.tracking-updated becomes tracking.updated, because the kernel
// allows one lowercase segment per name part.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

const Context = "fulfillment"

var (
	ShipmentReleased     = events.Type(Context, "shipment", "released", 1)
	PickConfirmed        = events.Type(Context, "pick", "confirmed", 1)
	PackingSlipPosted    = events.Type(Context, "packingslip", "posted", 1)
	PackingSlipCancelled = events.Type(Context, "packingslip", "cancelled", 1)
	ShipmentCancelled    = events.Type(Context, "shipment", "cancelled", 1)
	WaveCreated          = events.Type(Context, "wave", "created", 1)
	ASNIssued            = events.Type(Context, "asn", "issued", 1)
	TrackingUpdated      = events.Type(Context, "tracking", "updated", 1)
)

// All lists every event type this service publishes, for the registry check.
func All() []string {
	return []string{
		ShipmentReleased, PickConfirmed, PackingSlipPosted, PackingSlipCancelled,
		ShipmentCancelled, WaveCreated, ASNIssued, TrackingUpdated,
	}
}

// ShipmentCancelledData is published when a shipment is cancelled before it
// ships (BR-28.6). Inventory releases the lines' reservations from it; sales
// may release the order lines again.
type ShipmentCancelledData struct {
	ShipmentID     string    `json:"shipment_id"`
	ShipmentNumber string    `json:"shipment_number"`
	OrderID        string    `json:"order_id"`
	WarehouseID    string    `json:"warehouse"`
	Lines          []LineRef `json:"lines"`
}

// WaveCreatedData is published for each wave the picking workbench creates
// (FR-28.13); its shipments move to PICKING in the same transaction.
type WaveCreatedData struct {
	WaveID        string   `json:"wave_id"`
	WaveNumber    string   `json:"wave_number"`
	LegalEntityID string   `json:"legal_entity_id"`
	WarehouseID   string   `json:"warehouse"`
	GroupBy       string   `json:"group_by"`
	GroupKey      string   `json:"group_key"`
	ShipmentIDs   []string `json:"shipment_ids"`
	LineCount     int      `json:"line_count"`
	OrderCount    int      `json:"order_count"`
}

// ASNLine is one shipment line quantity inside an ASN container.
type ASNLine struct {
	OrderLineID string `json:"order_line_id"`
	Qty         string `json:"qty"`
}

// ASNContainer is one container with its SSCC (FR-28.9).
type ASNContainer struct {
	SSCC          string    `json:"sscc"`
	ContainerType string    `json:"container_type"`
	Lines         []ASNLine `json:"lines"`
}

// ASNIssuedData is the advance ship notice emitted when a slip posts with ASN
// enabled (FR-28.9): the containers that left with that slip. The carrier
// tracking number is not known at post (carrier labels are not built).
type ASNIssuedData struct {
	ShipmentID        string         `json:"shipment_id"`
	ShipmentNumber    string         `json:"shipment_number"`
	OrderID           string         `json:"order_id"`
	PackingSlipID     string         `json:"packing_slip_id"`
	PackingSlipNumber string         `json:"packing_slip_number"`
	Carrier           string         `json:"carrier"`
	Containers        []ASNContainer `json:"containers"`
}

// TrackingUpdatedData is published when a carrier reports a new tracking event
// (FR-28.10); the portal and sales read it.
type TrackingUpdatedData struct {
	ShipmentID     string `json:"shipment_id"`
	OrderID        string `json:"order_id"`
	Provider       string `json:"provider"`
	TrackingNumber string `json:"tracking_number"`
	EventKey       string `json:"event_key"`
	Status         string `json:"status"`
	Description    string `json:"description"`
	EventTime      string `json:"event_time"`
}

type LineRef struct {
	OrderLineID string `json:"order_line_id"`
	Qty         string `json:"qty"`
}

type ShipmentReleasedData struct {
	ShipmentID     string    `json:"shipment_id"`
	ShipmentNumber string    `json:"shipment_number"`
	OrderID        string    `json:"order_id"`
	WarehouseID    string    `json:"warehouse"`
	Lines          []LineRef `json:"lines"`
}

type PickLine struct {
	OrderLineID string `json:"order_line_id"`
	Qty         string `json:"qty"`
	Backordered string `json:"backordered"`
}

type PickConfirmedData struct {
	ShipmentID string     `json:"shipment_id"`
	Lines      []PickLine `json:"lines"`
}

type JournalEntry struct {
	Account     string `json:"account"`
	Amount      string `json:"amount"`
	Currency    string `json:"currency"`
	OrderLineID string `json:"order_line_id"`
}

type SlipLine struct {
	OrderLineID    string `json:"order_line_id"`
	Qty            string `json:"qty"`
	CostAmount     string `json:"cost_amount"`
	CostSnapshotID string `json:"cost_snapshot_id"`
}

// PackagingFee is one statistical packaging fee line of a slip (BR-28.12). It
// is not a journal line; billing (wave 29) reads it when packaging_fee_billable.
type PackagingFee struct {
	CountryCode  string `json:"country_code"`
	MaterialCode string `json:"material_code"`
	WeightKg     string `json:"weight_kg"`
	RatePerKg    string `json:"rate_per_kg"`
	FeeAmount    string `json:"fee_amount"`
	Currency     string `json:"currency"`
}

// ReturnableMove is the change of a customer's returnable packaging balance
// for one container type (BR-28.13): positive on post, negative on cancel.
type ReturnableMove struct {
	ContainerType string `json:"container_type"`
	Qty           string `json:"qty"`
}

// PackingSlipPostedData carries the shipped lines at cost and the journal
// (Dr 1405 / Cr 1400, or Dr 5000 / Cr 1400 when COGS posts at shipment).
// Inventory consumes the lines to issue stock; the ledger posts the journal
// with Idempotency-Key = packing_slip_id.
type PackingSlipPostedData struct {
	PackingSlipID     string           `json:"packing_slip_id"`
	PackingSlipNumber string           `json:"packing_slip_number"`
	ShipmentID        string           `json:"shipment_id"`
	OrderID           string           `json:"order_id"`
	CustomerID        string           `json:"customer_id"`
	PostingDate       string           `json:"posting_date"`
	Currency          string           `json:"currency"`
	TotalCost         string           `json:"total_cost"`
	Lines             []SlipLine       `json:"lines"`
	Journal           []JournalEntry   `json:"journal"`
	PackagingFees     []PackagingFee   `json:"packaging_fees"`
	Returnables       []ReturnableMove `json:"returnables"`
}

// PackingSlipCancelledData names the cancelled slip and the reversing slip that
// mirrors it: lines carry negative quantities and costs, the journal negative
// amounts on the same accounts (FR-28.5).
type PackingSlipCancelledData struct {
	PackingSlipID string           `json:"packing_slip_id"`
	ReversalID    string           `json:"reversal_id"`
	ShipmentID    string           `json:"shipment_id"`
	OrderID       string           `json:"order_id"`
	PostingDate   string           `json:"posting_date"`
	Currency      string           `json:"currency"`
	Lines         []SlipLine       `json:"lines"`
	Journal       []JournalEntry   `json:"journal"`
	PackagingFees []PackagingFee   `json:"packaging_fees"`
	Returnables   []ReturnableMove `json:"returnables"`
}
