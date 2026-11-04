package events

// Inbound change events (slice 18). inboundchange.recorded.v1 has the change id as its subject.
const (
	// InboundChangeRecorded is written for every change another service asked of a purchase order,
	// whether it was applied, refused by a purchasing rule or ignored. The purchase order's own
	// po.confirmed.v1 and poversion.created.v1 are written beside it when the change moved a line.
	InboundChangeRecorded = "dev.passage.procurement.inboundchange.recorded.v1"
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
}
