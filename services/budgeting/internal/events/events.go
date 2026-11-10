// Package events holds the budgeting event types and payloads.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

const Context = "budget"

var (
	RegisterPosted        = events.Type(Context, "register", "posted", 1)
	CheckCompleted        = events.Type(Context, "check", "completed", 1)
	EncumbranceRelieved   = events.Type(Context, "encumbrance", "relieved", 1)
	ReservationConsumed   = events.Type(Context, "reservation", "consumed", 1)
	ProgramCarriedForward = events.Type(Context, "program", "carriedforward", 1)
	EncumbranceReleased   = events.Type(Context, "encumbrance", "released", 1)
	PlanSubmitted         = events.Type(Context, "plan", "submitted", 1)
	PlanApproved          = events.Type(Context, "plan", "approved", 1)
	PlanRejected          = events.Type(Context, "plan", "rejected", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{RegisterPosted, CheckCompleted, EncumbranceRelieved, ReservationConsumed, ProgramCarriedForward, EncumbranceReleased,
		PlanSubmitted, PlanApproved, PlanRejected}
}

// PlanSubmittedData is the payload of plan.submitted.v1: it starts the approval workflow of that submission.
type PlanSubmittedData struct {
	PlanID        string `json:"plan_id"`
	LegalEntityID string `json:"legal_entity_id"`
	Name          string `json:"name"`
	FiscalYear    int    `json:"fiscal_year"`
	Submission    int    `json:"submission"`
	SubmittedBy   string `json:"submitted_by"`
}

// PlanApprovedData is the payload of plan.approved.v1: the approved scenario is now the register's budget.
type PlanApprovedData struct {
	PlanID          string `json:"plan_id"`
	ScenarioID      string `json:"scenario_id"`
	RegisterEntryID string `json:"register_entry_id"`
	Total           string `json:"total"`
}

// PlanRejectedData is the payload of plan.rejected.v1: the plan is back in Draft.
type PlanRejectedData struct {
	PlanID     string `json:"plan_id"`
	Submission int    `json:"submission"`
	Stage      string `json:"stage"`
	RejectedBy string `json:"rejected_by"`
	Reason     string `json:"reason"`
}

// ProgramCarriedForwardData is the payload of dev.passage.budget.program.carriedforward.v1.
type ProgramCarriedForwardData struct {
	ProgramID string                `json:"program_id"`
	FromYear  int                   `json:"from_year"`
	ToYear    int                   `json:"to_year"`
	Amount    string                `json:"amount"`
	Positions []CarriedPositionData `json:"positions"`
}

// CarriedPositionData is one position's carried amount.
type CarriedPositionData struct {
	PositionID string `json:"position_id"`
	Code       string `json:"code"`
	Amount     string `json:"amount"`
}

type RegisterLineData struct {
	DimensionKey string `json:"dimension_key"`
	SpanKey      string `json:"span_key"`
	Amount       string `json:"amount"`
}

type RegisterPostedData struct {
	EntryID       string             `json:"entry_id"`
	RuleVersionID string             `json:"rule_version_id"`
	EntryType     string             `json:"entry_type"`
	Lines         []RegisterLineData `json:"lines"`
}

type CheckLineData struct {
	LineID    string `json:"line_id"`
	Result    string `json:"result"`
	Available string `json:"available"`
	Amount    string `json:"amount"`
}

type CheckCompletedData struct {
	CheckID          string          `json:"check_id"`
	SourceDocumentID string          `json:"source_document_id"`
	Result           string          `json:"result"`
	LineResults      []CheckLineData `json:"line_results"`
}

type EncumbranceRelievedData struct {
	EncumbranceID string `json:"encumbrance_id"`
	InvoiceID     string `json:"invoice_id"`
	Amount        string `json:"amount"`
	Remaining     string `json:"remaining"`
}

// EncumbranceReleasedData is the payload of encumbrance.released.v1 (FR-38.5):
// the open remainder given back to available funds on a PO line cancellation
// or a PO close.
type EncumbranceReleasedData struct {
	EncumbranceID string `json:"encumbrance_id"`
	DocumentID    string `json:"document_id"`
	Reason        string `json:"reason"`
	Amount        string `json:"amount"`
}

// ReservationConsumedData is the payload of reservation.consumed.v1 (FR-38.20).
type ReservationConsumedData struct {
	ReservationID string `json:"reservation_id"`
	Number        string `json:"number"`
	LineID        string `json:"line_id"`
	DocumentType  string `json:"document_type"`
	DocumentID    string `json:"document_id"`
	EncumbranceID string `json:"encumbrance_id"`
	Amount        string `json:"amount"`
	Remaining     string `json:"remaining"`
}
