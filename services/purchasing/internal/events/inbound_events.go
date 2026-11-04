package events

// Inbound change events (slice 18). inboundchange.recorded.v1 has the change id as its subject.
const (
	// InboundChangeRecorded is written for every change another service asked of a purchase order,
	// whether it was applied, refused by a purchasing rule or ignored. The purchase order's own
	// po.confirmed.v1 and poversion.created.v1 are written beside it when the change moved a line.
	InboundChangeRecorded = "dev.passage.procurement.inboundchange.recorded.v1"
	// InboundChangeRefused is written beside inboundchange.recorded.v1 when a purchasing rule refused the
	// change (slice 19). It is the buyer's notification: buyer_id is the user who made the order, and the
	// change waits in the refused queue until a buyer acknowledges or retries it.
	InboundChangeRefused = "dev.passage.procurement.inboundchange.refused.v1"
	// InboundChangeResolved is written when a buyer settles a refused change: acknowledged by hand, or
	// retried and no longer refused (slice 19).
	InboundChangeResolved = "dev.passage.procurement.inboundchange.resolved.v1"
)

// InboundChangeData is the data member of inboundchange.recorded.v1. Qty and date members are set
// only when they apply to the change (a date move has dates, a quantity change has quantities).
type InboundChangeData struct {
	ChangeID           string `json:"change_id"`
	LegalEntityID      string `json:"legal_entity_id"`
	Source             string `json:"source"`
	SourceKey          string `json:"source_key"`
	Action             string `json:"action"`
	Status             string `json:"status"`
	POID               string `json:"po_id,omitempty"`
	Number             string `json:"number,omitempty"`
	LineNo             int    `json:"line_no,omitempty"`
	ReasonCode         string `json:"reason_code,omitempty"`
	Reason             string `json:"reason,omitempty"`
	QtyBefore          string `json:"qty_before,omitempty"`
	QtyAfter           string `json:"qty_after,omitempty"`
	DateBefore         string `json:"date_before,omitempty"`
	DateAfter          string `json:"date_after,omitempty"`
	VersionNo          int    `json:"version_no,omitempty"`
	ReapprovalRequired bool   `json:"reapproval_required"`
	// Attempts counts the runs of the request (slice 19); BuyerID is the user who made the order, empty when unknown.
	Attempts int    `json:"attempts,omitempty"`
	BuyerID  string `json:"buyer_id,omitempty"`
}

// InboundChangeResolvedData is the data member of inboundchange.resolved.v1. Resolution is ACKNOWLEDGED
// (status stays REFUSED) or RETRIED (status is APPLIED or IGNORED, the retry's outcome).
type InboundChangeResolvedData struct {
	ChangeID           string `json:"change_id"`
	LegalEntityID      string `json:"legal_entity_id"`
	Source             string `json:"source"`
	SourceKey          string `json:"source_key"`
	Action             string `json:"action"`
	Resolution         string `json:"resolution"`
	Status             string `json:"status"`
	POID               string `json:"po_id,omitempty"`
	Number             string `json:"number,omitempty"`
	LineNo             int    `json:"line_no,omitempty"`
	ReasonCode         string `json:"reason_code,omitempty"`
	Attempts           int    `json:"attempts"`
	ResolvedBy         string `json:"resolved_by,omitempty"`
	ResolvedAt         string `json:"resolved_at,omitempty"`
	Note               string `json:"note,omitempty"`
	VersionNo          int    `json:"version_no,omitempty"`
	ReapprovalRequired bool   `json:"reapproval_required"`
}
