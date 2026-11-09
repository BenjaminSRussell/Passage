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
	// RequisitionLineID is the requisition line the RFQ line was raised for, when it was.
	RequisitionLineID string `json:"requisition_line_id,omitempty"`
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
	LineNo   int    `json:"line_no"`
	LineID   string `json:"line_id"`
	ReplyID  string `json:"reply_id"`
	VendorID string `json:"vendor_id"`
	// RequisitionLineID is the requisition line the awarded RFQ line was raised for, when it was.
	RequisitionLineID string `json:"requisition_line_id,omitempty"`
	UnitPrice         string `json:"unit_price"`
	PriceUnit         string `json:"price_unit"`
	Currency          string `json:"currency"`
	LeadTimeDays      int    `json:"lead_time_days"`
	PurchaseOrderID   string `json:"purchase_order_id"`
	OverrideReason    string `json:"override_reason,omitempty"`
}

// RFQAwardedData is the data member of rfq.awarded.v1.
type RFQAwardedData struct {
	RFQID         string           `json:"rfq_id"`
	Number        string           `json:"number"`
	LegalEntityID string           `json:"legal_entity_id"`
	Awards        []RFQAwardedLine `json:"awards"`
}

// RFQ amendment and type events (FR-17.19).
const (
	// RFQAmended is written by POST /rfqs/{id}/amendments, when an addendum is issued to the invited vendors.
	RFQAmended = "dev.passage.procurement.rfq.amended.v1"
	// RFQTypeCreated is written when an RFQ type is created.
	RFQTypeCreated = "dev.passage.procurement.rfqtype.created.v1"
	// RFQTypeChanged is written when an RFQ type is changed or deactivated.
	RFQTypeChanged = "dev.passage.procurement.rfqtype.changed.v1"
)

// RFQAmendedData is the data member of rfq.amended.v1: the addendum RFQ-n-A<k>, the vendors that must
// acknowledge it, and the deadline it moved, when it did.
type RFQAmendedData struct {
	RFQID            string   `json:"rfq_id"`
	Number           string   `json:"number"`
	AmendmentNo      int      `json:"amendment_no"`
	LegalEntityID    string   `json:"legal_entity_id"`
	Summary          string   `json:"summary"`
	VendorIDs        []string `json:"vendor_ids"`
	PreviousDeadline string   `json:"previous_deadline,omitempty"`
	ReplyDeadline    string   `json:"reply_deadline"`
	CriteriaChanged  bool     `json:"criteria_changed"`
	// LateClearedVendorIDs are the vendors whose replies were late only against PreviousDeadline and are
	// on time since the deadline moved (BR-17.11).
	LateClearedVendorIDs []string `json:"late_cleared_vendor_ids"`
	IssuedBy             string   `json:"issued_by,omitempty"`
}

// RFQTypeData is the data member of rfqtype.created.v1 and rfqtype.changed.v1.
type RFQTypeData struct {
	RFQTypeID         string `json:"rfq_type_id"`
	LegalEntityID     string `json:"legal_entity_id"`
	Code              string `json:"code"`
	Name              string `json:"name"`
	Sealed            bool   `json:"sealed"`
	AmendmentsAllowed bool   `json:"amendments_allowed"`
	RankingRule       string `json:"ranking_rule"`
	CriteriaCount     int    `json:"criteria_count"`
	Status            string `json:"status"`
	Version           int    `json:"version"`
	ChangedBy         string `json:"changed_by,omitempty"`
}
