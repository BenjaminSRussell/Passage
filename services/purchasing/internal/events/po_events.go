package events

// Purchase order event types (PRD 3.17.5). Each is written to the outbox in the
// transaction that changes the PO; the subject is the PO id.
const (
	// POConfirmed is written when a version of a PO is confirmed: by :confirm,
	// or by a post-confirmation change that needs no re-approval (FR-17.8).
	POConfirmed = "dev.passage.procurement.po.confirmed.v1"
	// POVersionCreated is written for every confirmed version above 1. The PRD
	// names it po.version.created; the platform type grammar allows one entity
	// segment, so the entity is poversion (as engineering's bomversion).
	POVersionCreated = "dev.passage.procurement.poversion.created.v1"
	// POCancelled is written by :cancel.
	POCancelled = "dev.passage.procurement.po.cancelled.v1"
)

// POLineData is one line as carried on po.confirmed.v1. Money and quantities
// are decimal strings.
type POLineData struct {
	LineNo       int    `json:"line_no"`
	ProductID    string `json:"product_id,omitempty"`
	Description  string `json:"description,omitempty"`
	Qty          string `json:"qty"`
	Unit         string `json:"unit"`
	UnitPrice    string `json:"unit_price"`
	PriceUnit    string `json:"price_unit"`
	LineAmount   string `json:"line_amount"`
	DeliveryDate string `json:"delivery_date"`
	Status       string `json:"status"`
}

// POConfirmedData is the data member of po.confirmed.v1. Lines include
// cancelled lines with status CANCELLED so consumers can close what they hold.
type POConfirmedData struct {
	POID          string       `json:"po_id"`
	Number        string       `json:"number"`
	VersionNo     int          `json:"version_no"`
	LegalEntityID string       `json:"legal_entity_id"`
	VendorID      string       `json:"vendor_id"`
	SiteID        string       `json:"site_id,omitempty"`
	WarehouseID   string       `json:"warehouse_id,omitempty"`
	Currency      string       `json:"currency"`
	Subtotal      string       `json:"subtotal"`
	DiscountTotal string       `json:"discount_total"`
	ChargesTotal  string       `json:"charges_total"`
	TaxTotal      string       `json:"tax_total"`
	Total         string       `json:"total"`
	Lines         []POLineData `json:"lines"`
}

// POVersionCreatedData is the data member of poversion.created.v1. Patch is
// the RFC 6902 diff from the previous version's snapshot.
type POVersionCreatedData struct {
	POID               string `json:"po_id"`
	Number             string `json:"number"`
	VersionNo          int    `json:"version_no"`
	Patch              any    `json:"patch"`
	ReapprovalRequired bool   `json:"reapproval_required"`
}

// POCancelledLine is one line cancelled by po.cancelled.v1 with the quantity
// that was still to be received.
type POCancelledLine struct {
	LineNo       int    `json:"line_no"`
	ProductID    string `json:"product_id,omitempty"`
	Unit         string `json:"unit"`
	RemainingQty string `json:"remaining_qty"`
}

// POCancelledData is the data member of po.cancelled.v1.
type POCancelledData struct {
	POID        string            `json:"po_id"`
	Number      string            `json:"number"`
	VersionNo   int               `json:"version_no"`
	PriorStatus string            `json:"prior_status"`
	Lines       []POCancelledLine `json:"lines"`
}
