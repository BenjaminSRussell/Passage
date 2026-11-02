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
	ShipmentClosed       = events.Type(Context, "shipment", "closed", 1)
	ContainerUnpacked    = events.Type(Context, "container", "unpacked", 1)
	ContainerNested      = events.Type(Context, "container", "nested", 1)
	ContainerUnnested    = events.Type(Context, "container", "unnested", 1)
	WaveCreated          = events.Type(Context, "wave", "created", 1)
	WaveAssigned         = events.Type(Context, "wave", "assigned", 1)
	WaveClosed           = events.Type(Context, "wave", "closed", 1)
	ASNIssued            = events.Type(Context, "asn", "issued", 1)
	TrackingUpdated      = events.Type(Context, "tracking", "updated", 1)
	PickTracked          = events.Type(Context, "pick", "tracked", 1)

	WarehouseSettingChanged     = events.Type(Context, "warehousesetting", "changed", 1)
	LegalEntitySettingChanged   = events.Type(Context, "legalentitysetting", "changed", 1)
	WarehouseReleaseRuleChanged = events.Type(Context, "warehousereleaserule", "changed", 1)

	DropShipCreated   = events.Type(Context, "dropship", "created", 1)
	DropShipConfirmed = events.Type(Context, "dropship", "confirmed", 1)
	DropShipCancelled = events.Type(Context, "dropship", "cancelled", 1)
	DropShipClosed    = events.Type(Context, "dropship", "closed", 1)
)

// All lists every event type this service publishes, for the registry check.
func All() []string {
	return []string{
		ShipmentReleased, PickConfirmed, PackingSlipPosted, PackingSlipCancelled,
		ShipmentCancelled, ShipmentClosed, ContainerUnpacked, ContainerNested, ContainerUnnested, WaveCreated, WaveAssigned, WaveClosed, ASNIssued, TrackingUpdated, PickTracked, WarehouseSettingChanged, LegalEntitySettingChanged, WarehouseReleaseRuleChanged,
		DropShipCreated, DropShipConfirmed, DropShipCancelled, DropShipClosed,
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

// ContainerUnpackedData is published when a CLOSED container is taken apart
// before it ships: its SSCC stays issued (never reused) and Lines are the
// quantities that return to the unpacked pool of the shipment.
type ContainerUnpackedData struct {
	ShipmentID     string    `json:"shipment_id"`
	ShipmentNumber string    `json:"shipment_number"`
	ContainerID    string    `json:"container_id"`
	SSCC           string    `json:"sscc"`
	Lines          []LineRef `json:"lines"`
}

// ContainerNestedData is published when cartons are put on a new pallet
// (nested container, FR-28.3): the pallet has its own SSCC and the cartons keep
// theirs. Dissolving the pallet publishes container.unnested with the same shape.
type ContainerNestedData struct {
	ShipmentID     string   `json:"shipment_id"`
	ShipmentNumber string   `json:"shipment_number"`
	ContainerID    string   `json:"container_id"`
	SSCC           string   `json:"sscc"`
	ContainerType  string   `json:"container_type"`
	ChildSSCCs     []string `json:"child_ssccs"`
}

// ClosedLine is one line of a closed remainder: Qty is the planned quantity
// given up (it will not ship), PickedUnpacked the part of it that was picked but
// never packed and goes back to stock (inventory releases it).
type ClosedLine struct {
	OrderLineID    string `json:"order_line_id"`
	Qty            string `json:"qty"`
	PickedUnpacked string `json:"picked_unpacked"`
}

// ShipmentClosedData is published when the open remainder of a partially
// shipped shipment is closed (FR-28.8, CANCEL_REMAINDER). Sales may cancel or
// re-order the remainder; inventory releases the reservation of Qty.
type ShipmentClosedData struct {
	ShipmentID     string       `json:"shipment_id"`
	ShipmentNumber string       `json:"shipment_number"`
	OrderID        string       `json:"order_id"`
	WarehouseID    string       `json:"warehouse"`
	Lines          []ClosedLine `json:"lines"`
	// BackorderShipmentID is set when the remainder moved to a new shipment
	// (short pick action REMAINDER_BACKORDER): sales keeps the order lines open.
	BackorderShipmentID string `json:"backorder_shipment_id,omitempty"`
}

// WarehouseSettingChangedData is published when a warehouse's short pick action
// is saved (FR-28.8).
type WarehouseSettingChangedData struct {
	WarehouseID         string `json:"warehouse"`
	LegalEntityID       string `json:"legal_entity_id"`
	ShortPickAction     string `json:"short_pick_action"`
	PreviousShortAction string `json:"previous_short_pick_action"`
	Version             int    `json:"version"`
}

// LegalEntitySettingChangedData is published when the posting settings of a
// legal entity are saved (section 5.28.13): the packing slip posting default
// (cogs_at_shipment) and the ASN default (asn_enabled).
type LegalEntitySettingChangedData struct {
	LegalEntityID          string `json:"legal_entity_id"`
	CogsAtShipment         bool   `json:"cogs_at_shipment"`
	AsnEnabled             bool   `json:"asn_enabled"`
	PreviousCogsAtShipment bool   `json:"previous_cogs_at_shipment"`
	PreviousAsnEnabled     bool   `json:"previous_asn_enabled"`
	Version                int    `json:"version"`
}

// WarehouseReleaseRuleChangedData is published when the release and wave
// parameters of a warehouse are saved (FR-28.1, FR-28.13), with the values they replace.
type WarehouseReleaseRuleChangedData struct {
	WarehouseID            string `json:"warehouse"`
	LegalEntityID          string `json:"legal_entity_id"`
	HorizonDays            int    `json:"release_horizon_days"`
	RequireFullATP         bool   `json:"release_requires_full_atp"`
	WaveMaxLines           int    `json:"wave_max_lines"`
	WaveMaxOrders          int    `json:"wave_max_orders"`
	PreviousHorizonDays    int    `json:"previous_release_horizon_days"`
	PreviousRequireFullATP bool   `json:"previous_release_requires_full_atp"`
	PreviousWaveMaxLines   int    `json:"previous_wave_max_lines"`
	PreviousWaveMaxOrders  int    `json:"previous_wave_max_orders"`
	Version                int    `json:"version"`
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

// WaveAssignedData is published when a wave is given to a picker or handed to
// another one (FR-28.13). PreviousPicker is empty for the first assignment.
type WaveAssignedData struct {
	WaveID         string `json:"wave_id"`
	WaveNumber     string `json:"wave_number"`
	LegalEntityID  string `json:"legal_entity_id"`
	WarehouseID    string `json:"warehouse"`
	Picker         string `json:"picker"`
	PreviousPicker string `json:"previous_picker,omitempty"`
}

// WaveClosedData is published when every line of a wave is picked or on a
// cancelled shipment and the wave is closed. ShortLines counts the lines picked
// below plan (their remainder is a backorder).
type WaveClosedData struct {
	WaveID        string   `json:"wave_id"`
	WaveNumber    string   `json:"wave_number"`
	LegalEntityID string   `json:"legal_entity_id"`
	WarehouseID   string   `json:"warehouse"`
	Picker        string   `json:"picker,omitempty"`
	ShipmentIDs   []string `json:"shipment_ids"`
	LineCount     int      `json:"line_count"`
	ShortLines    int      `json:"short_lines"`
}

// ASNLine is one shipment line quantity inside an ASN container.
type ASNLine struct {
	OrderLineID string `json:"order_line_id"`
	Qty         string `json:"qty"`
}

// ASNContainer is one container with its SSCC (FR-28.9).
type ASNContainer struct {
	SSCC          string `json:"sscc"`
	ContainerType string `json:"container_type"`
	// ParentSSCC is the pallet the container sits on, empty for a top-level one.
	ParentSSCC string    `json:"parent_sscc,omitempty"`
	Lines      []ASNLine `json:"lines"`
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
	// BackorderOf is set on a backorder shipment (short pick action
	// REMAINDER_BACKORDER): the shipment whose closed remainder it took over.
	// Sales must not count its lines as released a second time.
	BackorderOf string `json:"backorder_of,omitempty"`
}

type PickLine struct {
	OrderLineID string `json:"order_line_id"`
	Qty         string `json:"qty"`
	Backordered string `json:"backordered"`
	// ShortReason is the short pick reason code, when one was given.
	ShortReason string `json:"short_reason,omitempty"`
}

type PickConfirmedData struct {
	ShipmentID string `json:"shipment_id"`
	// WaveID is the wave whose confirmation picked the lines; absent for a
	// single-shipment pick.
	WaveID string     `json:"wave_id,omitempty"`
	Lines  []PickLine `json:"lines"`
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
	// ProductID and Tracking tell inventory which batches and serials to issue
	// (BR-28.10). Tracking is empty for an untracked line and for reversals.
	ProductID string          `json:"product_id,omitempty"`
	Tracking  []TrackingEntry `json:"tracking,omitempty"`
}

// TrackingEntry is one batch or serial taken from one location.
type TrackingEntry struct {
	BatchNo      string `json:"batch_no,omitempty"`
	SerialNo     string `json:"serial_no,omitempty"`
	LocationCode string `json:"location_code,omitempty"`
	Qty          string `json:"qty"`
}

// TrackedLine is the tracking captured for one shipment line.
type TrackedLine struct {
	OrderLineID string          `json:"order_line_id"`
	ProductID   string          `json:"product_id"`
	Entries     []TrackingEntry `json:"entries"`
}

// PickTrackedData is published when the batch, serial and location entries of
// a shipment line are captured or replaced at pick (BR-28.10). Entries is the
// full set of the line's entries not yet shipped.
type PickTrackedData struct {
	ShipmentID     string      `json:"shipment_id"`
	ShipmentNumber string      `json:"shipment_number"`
	WarehouseID    string      `json:"warehouse"`
	Line           TrackedLine `json:"line"`
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

// DropShipData is published when a drop-ship is created (FR-28.7), confirmed
// by the vendor (once per vendor shipment), closed short or cancelled. The
// confirmed event carries the journal of that confirmation (Dr 5000 / Cr 2110
// at the purchase order cost for ConfirmedQty) for the ledger; no inventory
// moves. Qty is the ordered quantity; QtyConfirmed and QtyClosed are the
// running totals after the event.
type DropShipData struct {
	DropShipID        string         `json:"drop_ship_id"`
	DropShipNumber    string         `json:"drop_ship_number"`
	Status            string         `json:"status"`
	LegalEntityID     string         `json:"legal_entity_id"`
	OrderID           string         `json:"order_id"`
	OrderLineID       string         `json:"order_line_id"`
	CustomerID        string         `json:"customer_id"`
	ProductID         string         `json:"product_id"`
	Unit              string         `json:"unit"`
	Qty               string         `json:"qty"`
	VendorID          string         `json:"vendor_id"`
	PurchaseOrderID   string         `json:"purchase_order_id"`
	PurchaseOrderLine string         `json:"purchase_order_line_id"`
	UnitCost          string         `json:"unit_cost"`
	Currency          string         `json:"currency"`
	VendorShipmentRef string         `json:"vendor_shipment_ref,omitempty"`
	ShippedOn         string         `json:"shipped_on,omitempty"`
	PostingDate       string         `json:"posting_date,omitempty"`
	QtyConfirmed      string         `json:"qty_confirmed"`
	QtyClosed         string         `json:"qty_closed"`
	ConfirmationSeq   int            `json:"confirmation_seq,omitempty"`
	ConfirmedQty      string         `json:"confirmed_qty,omitempty"`
	Journal           []JournalEntry `json:"journal"`
}
