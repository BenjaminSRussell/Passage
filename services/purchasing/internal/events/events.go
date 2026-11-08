// Package events holds the event types and payloads the purchasing service
// publishes. Schemas are in events/asyncapi.yaml.
package events

// Context is the bounded context segment of every event type (PRD 3.17.5).
const Context = "procurement"

// Event types. Each follows dev.passage.<context>.<entity>.<verb>.v1 and is
// checked against platform/events.ValidType in the package test.
const (
	// RequisitionSubmitted is written when a DRAFT requisition is submitted.
	RequisitionSubmitted = "dev.passage.procurement.requisition.submitted.v1"
	// RequisitionApproved is written when a requisition reaches APPROVED: on submit
	// when the policy chain is empty, or when the last level approves.
	RequisitionApproved = "dev.passage.procurement.requisition.approved.v1"
	// AgreementThreshold is written when a release takes a purchase agreement
	// line to the threshold (90 % by default) or overruns a SOFT limit
	// (FR-17.5, BR-17.10, PRD 3.17.5).
	AgreementThreshold = "dev.passage.procurement.agreement.threshold.v1"
)

// RequisitionLine is one line of a requisition event. Money is a decimal string.
type RequisitionLine struct {
	LineNo    int    `json:"line_no"`
	LineID    string `json:"line_id"`
	ProductID string `json:"product_id,omitempty"`
	Category  string `json:"category,omitempty"`
	Qty       string `json:"qty"`
	Amount    string `json:"amount"`
}

// RequisitionData is the data member of requisition.submitted.v1 and
// requisition.approved.v1.
type RequisitionData struct {
	RequisitionID string            `json:"requisition_id"`
	Number        string            `json:"number"`
	LegalEntityID string            `json:"legal_entity_id"`
	RequesterID   string            `json:"requester_id"`
	Currency      string            `json:"currency"`
	Lines         []RequisitionLine `json:"lines"`
	Total         string            `json:"total"`
	ApproverChain []string          `json:"approver_chain"`
	PolicyVersion string            `json:"policy_version"`
	Status        string            `json:"status"`
}

// AgreementThresholdData is the data member of agreement.threshold.v1. Pct is
// the released percentage of the line's commitment after the release; Overrun
// is true when a SOFT limit was exceeded. Expiry is the agreement's valid_to,
// omitted when open-ended.
type AgreementThresholdData struct {
	AgreementID    string `json:"agreement_id"`
	Number         string `json:"number"`
	VendorID       string `json:"vendor_id"`
	Line           int    `json:"line"`
	LineID         string `json:"line_id"`
	CommitmentType string `json:"commitment_type"`
	Pct            string `json:"pct"`
	Overrun        bool   `json:"overrun"`
	Expiry         string `json:"expiry,omitempty"`
}
