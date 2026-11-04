package events

// RequisitionSplit is written when a buyer cuts new lines out of an OPEN line of an APPROVED requisition
// (FR-17.3). The subject is the requisition id, like the other requisition events.
const RequisitionSplit = "dev.passage.procurement.requisition.split.v1"

// RequisitionSplitLine is a line a split produced or changed: the original with what it keeps, or a new line.
type RequisitionSplitLine struct {
	LineNo int    `json:"line_no"`
	LineID string `json:"line_id"`
	Qty    string `json:"qty"`
	Amount string `json:"amount"`
}

// RequisitionSplitData is the data member of requisition.split.v1. The total does not change: the quantities
// and amounts of the original and the new lines add up to what the original line held.
type RequisitionSplitData struct {
	RequisitionID string                 `json:"requisition_id"`
	Number        string                 `json:"number"`
	LegalEntityID string                 `json:"legal_entity_id"`
	Currency      string                 `json:"currency"`
	Version       int                    `json:"version"`
	Original      RequisitionSplitLine   `json:"original"`
	NewLines      []RequisitionSplitLine `json:"new_lines"`
	SplitBy       string                 `json:"split_by,omitempty"`
}
