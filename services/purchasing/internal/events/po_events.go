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
	// PORepriced is written when :reprice applies a subsequent price change as
	// the next PO version (FR-17.22).
	PORepriced = "dev.passage.procurement.po.repriced.v1"
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

// PORepricedLine is one line on po.repriced.v1. Prices, quantities and amounts
// are decimal strings; amounts are for Qty, and Delta = NewAmount − OldAmount.
type PORepricedLine struct {
	LineNo       int    `json:"line_no"`
	Qty          string `json:"qty"`
	OldPrice     string `json:"old_price"`
	OldPriceUnit string `json:"old_price_unit"`
	NewPrice     string `json:"new_price"`
	NewPriceUnit string `json:"new_price_unit"`
	OldAmount    string `json:"old_amount"`
	NewAmount    string `json:"new_amount"`
	Delta        string `json:"delta"`
	OldSource    string `json:"old_price_source"`
	NewSource    string `json:"new_price_source"`
}

// PORepricedData is the data member of po.repriced.v1 (FR-17.22, BR-17.18).
// VersionNo is the version the run wrote; when ReapprovalRequired it waits for
// re-approval and po.confirmed follows once it is confirmed. Lines are the
// moved lines (Qty is the line quantity). InvoicedLines kept the old price and
// carry the invoiced quantity and its delta for the wave 18 subsequent debit or
// credit (FR-18.18).
type PORepricedData struct {
	POID               string           `json:"po_id"`
	Number             string           `json:"number"`
	VersionNo          int              `json:"version_no"`
	ReapprovalRequired bool             `json:"reapproval_required"`
	Currency           string           `json:"currency"`
	TotalDelta         string           `json:"total_delta"`
	Lines              []PORepricedLine `json:"lines"`
	InvoicedLines      []PORepricedLine `json:"invoiced_lines"`
}
