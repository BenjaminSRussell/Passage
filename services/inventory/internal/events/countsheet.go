package events

// CountSheetChanged is emitted when a count sheet moves through its review
// steps (FR-16.9): submitted for review, approved, a recount requested,
// cancelled or posted. POSTED and CANCELLED end the sheet and lift its stock
// freeze; a posted sheet with variances is also announced by journal.posted
// for its COUNT journal (a sheet whose counts all match the book has none).
const CountSheetChanged = "dev.passage.inventory.countsheet.changed.v1"

// Count sheet actions carried in CountSheetChangedData.Action.
const (
	CountActionSubmitted        = "SUBMITTED"
	CountActionApproved         = "APPROVED"
	CountActionRecountRequested = "RECOUNT_REQUESTED"
	CountActionCancelled        = "CANCELLED"
	CountActionPosted           = "POSTED"
)

// CountSheetChangedData is the payload of countsheet.changed. LineID and Reason
// are set only for a recount request.
type CountSheetChangedData struct {
	SheetID       string  `json:"sheetId"`
	LegalEntityID string  `json:"legalEntityId"`
	PlanID        string  `json:"planId"`
	Number        string  `json:"number"`
	Action        string  `json:"action"`
	Status        string  `json:"status"`
	LineCount     int     `json:"lineCount"`
	CountedCount  int     `json:"countedCount"`
	RecountLines  int     `json:"recountLines"`
	LineID        *string `json:"lineId"`
	Reason        *string `json:"reason"`
}
