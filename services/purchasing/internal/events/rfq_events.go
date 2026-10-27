package events

// RFQ event types (PRD 3.17.5). Each is written to the outbox in the
// transaction that changes the RFQ; the subject is the RFQ id.
const (
	// RFQSent is written by :send, when the vendors are invited.
	RFQSent = "dev.passage.procurement.rfq.sent.v1"
	// RFQAwarded is written by :award, with the purchase orders it created.
	RFQAwarded = "dev.passage.procurement.rfq.awarded.v1"
)

// RFQLineData is one requested line as carried on rfq.sent.v1. Quantities are
// decimal strings.
type RFQLineData struct {
	LineNo       int    `json:"line_no"`
	LineID       string `json:"line_id"`
	ProductID    string `json:"product_id,omitempty"`
	Description  string `json:"description,omitempty"`
	Qty          string `json:"qty"`
	Unit         string `json:"unit"`
	RequiredDate string `json:"required_date,omitempty"`
}

// RFQSentData is the data member of rfq.sent.v1: the vendors invited to reply
// and the lines they are asked to quote.
type RFQSentData struct {
	RFQID         string        `json:"rfq_id"`
	Number        string        `json:"number"`
	LegalEntityID string        `json:"legal_entity_id"`
	VendorIDs     []string      `json:"vendor_ids"`
	ReplyDeadline string        `json:"reply_deadline"`
	Lines         []RFQLineData `json:"lines"`
}

// RFQAwardedLine is one awarded line: the reply that won it, its vendor and
// price, and the purchase order created from it.
type RFQAwardedLine struct {
	LineNo          int    `json:"line_no"`
	LineID          string `json:"line_id"`
	ReplyID         string `json:"reply_id"`
	VendorID        string `json:"vendor_id"`
	UnitPrice       string `json:"unit_price"`
	PriceUnit       string `json:"price_unit"`
	Currency        string `json:"currency"`
	LeadTimeDays    int    `json:"lead_time_days"`
	PurchaseOrderID string `json:"purchase_order_id"`
	OverrideReason  string `json:"override_reason,omitempty"`
}

// RFQAwardedData is the data member of rfq.awarded.v1.
type RFQAwardedData struct {
	RFQID         string           `json:"rfq_id"`
	Number        string           `json:"number"`
	LegalEntityID string           `json:"legal_entity_id"`
	Awards        []RFQAwardedLine `json:"awards"`
}
