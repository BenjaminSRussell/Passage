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

// RequisitionWithdrawn is written when the requester takes an IN_REVIEW requisition back to DRAFT before any
// approval level decided on it (BR-17.1), so approver inboxes and notifications drop it.
const RequisitionWithdrawn = "dev.passage.procurement.requisition.withdrawn.v1"

// RequisitionLineCancelled is written when one OPEN line of an APPROVED requisition is cancelled while the rest of
// the requisition continues (BR-17.1). Budget control (wave 38) releases the line's pre-encumbrance.
const RequisitionLineCancelled = "dev.passage.procurement.requisitionline.cancelled.v1"

// RequisitionWithdrawnData is the data member of requisition.withdrawn.v1.
type RequisitionWithdrawnData struct {
	RequisitionID string `json:"requisition_id"`
	Number        string `json:"number"`
	LegalEntityID string `json:"legal_entity_id"`
	RequesterID   string `json:"requester_id"`
	Currency      string `json:"currency"`
	Total         string `json:"total"`
	WithdrawnBy   string `json:"withdrawn_by"`
	Reason        string `json:"reason,omitempty"`
	Version       int    `json:"version"`
}

// RequisitionLineCancelledData is the data member of requisitionline.cancelled.v1. Amount is the line's amount
// in the requisition's currency, the pre-encumbrance to release; Total is the requisition total after the
// cancellation (the lines not cancelled) and RequisitionStatus is APPROVED, or CLOSED when every line left is
// consolidated.
type RequisitionLineCancelledData struct {
	RequisitionID string `json:"requisition_id"`
	Number        string `json:"number"`
	LegalEntityID string `json:"legal_entity_id"`
	RequesterID   string `json:"requester_id"`
	Currency      string `json:"currency"`
	LineID        string `json:"line_id"`
	LineNo        int    `json:"line_no"`
	ProductID     string `json:"product_id,omitempty"`
	Category      string `json:"category,omitempty"`
	Qty           string `json:"qty"`
	Amount        string `json:"amount"`
	Total         string `json:"total"`
	// RequisitionStatus is the requisition's status after the cancellation.
	RequisitionStatus string `json:"requisition_status"`
	CancelledBy       string `json:"cancelled_by"`
	Reason            string `json:"reason,omitempty"`
	Version           int    `json:"version"`
}
