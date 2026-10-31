package events

// Journal name events (PRD 3.16.13): a journal name was created or changed.
const (
	JournalNameCreated = "dev.passage.inventory.journalname.created.v1"
	JournalNameUpdated = "dev.passage.inventory.journalname.updated.v1"
)

// JournalNameData is the payload of both journal name events.
type JournalNameData struct {
	JournalNameID    string  `json:"journalNameId"`
	LegalEntityID    string  `json:"legalEntityId"`
	Code             string  `json:"code"`
	Description      string  `json:"description"`
	JournalType      string  `json:"journalType"`
	RequiresApproval bool    `json:"requiresApproval"`
	OffsetAccountID  *string `json:"offsetAccountId"`
	Active           bool    `json:"active"`
	Version          int     `json:"version"`
}
