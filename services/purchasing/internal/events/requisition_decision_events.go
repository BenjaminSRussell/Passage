package events

// RequisitionRejected is written when an approver sends an IN_REVIEW requisition back (BR-17.1). The subject is the
// requisition id, like the other requisition events.
const RequisitionRejected = "dev.passage.procurement.requisition.rejected.v1"

// RequisitionCancelled is written when a requisition is cancelled before anything was ordered from it (BR-17.1).
// Budget control (wave 38, FR-38.5) pre-encumbers a requisition when it is approved, so it releases that
// pre-encumbrance only for a cancel whose previous status is APPROVED; a DRAFT or REJECTED requisition holds none.
const RequisitionCancelled = "dev.passage.procurement.requisition.cancelled.v1"

// RequisitionRejectedData is the data member of requisition.rejected.v1.
type RequisitionRejectedData struct {
	RequisitionID string `json:"requisition_id"`
	Number        string `json:"number"`
	LegalEntityID string `json:"legal_entity_id"`
	RequesterID   string `json:"requester_id"`
	Currency      string `json:"currency"`
	Total         string `json:"total"`
	// StepNo and Role name the approval level that rejected.
	StepNo     int    `json:"step_no"`
	Role       string `json:"role"`
	RejectedBy string `json:"rejected_by"`
	Reason     string `json:"reason"`
	Version    int    `json:"version"`
}

// RequisitionCancelledData is the data member of requisition.cancelled.v1.
type RequisitionCancelledData struct {
	RequisitionID  string `json:"requisition_id"`
	Number         string `json:"number"`
	LegalEntityID  string `json:"legal_entity_id"`
	RequesterID    string `json:"requester_id"`
	Currency       string `json:"currency"`
	Total          string `json:"total"`
	PreviousStatus string `json:"previous_status"`
	// CancelledLineIDs are the lines that were still OPEN and are now CANCELLED.
	CancelledLineIDs []string `json:"cancelled_line_ids"`
	CancelledBy      string   `json:"cancelled_by"`
	Reason           string   `json:"reason,omitempty"`
	Version          int      `json:"version"`
}
