package events

// Cash desk events (FR-36.16). The outbox aggregate is the desk id, so the events of one desk stay ordered.
const (
	CashDeskCreated     = "dev.passage.treasury.cashdesk.created.v1"
	CashDeskUpdated     = "dev.passage.treasury.cashdesk.updated.v1"
	CashDeskDeactivated = "dev.passage.treasury.cashdesk.deactivated.v1"
	CashDeskDayClosed   = "dev.passage.treasury.cashdeskday.closed.v1"
	CashDeskEntryPosted = "dev.passage.treasury.cashdeskentry.posted.v1"
)

// CashDeskData is the payload of CashDeskCreated, CashDeskUpdated and CashDeskDeactivated.
type CashDeskData struct {
	DeskID        string `json:"deskId"`
	LegalEntityID string `json:"legalEntityId"`
	Code          string `json:"code"`
	Currency      string `json:"currency"`
	Status        string `json:"status"`
	MaxBalance    string `json:"maxBalance"`
	DualControl   bool   `json:"dualControl"`
}

// CashDeskDayClosedData is the payload of CashDeskDayClosed: published when the day is final (after the second
// signature when the desk needs one). Amounts are decimal strings.
type CashDeskDayClosedData struct {
	DeskID         string `json:"deskId"`
	LegalEntityID  string `json:"legalEntityId"`
	BusinessDate   string `json:"businessDate"`
	Currency       string `json:"currency"`
	OpeningBalance string `json:"openingBalance"`
	BookBalance    string `json:"bookBalance"`
	CountedBalance string `json:"countedBalance"`
	Difference     string `json:"difference"`
	ClosedBy       string `json:"closedBy"`
	ApprovedBy     string `json:"approvedBy,omitempty"`
}

// CashDeskEntryPostedData is the payload of CashDeskEntryPosted: a journal line reached the general ledger.
type CashDeskEntryPostedData struct {
	DeskID        string `json:"deskId"`
	EntryID       string `json:"entryId"`
	LegalEntityID string `json:"legalEntityId"`
	BusinessDate  string `json:"businessDate"`
	Kind          string `json:"kind"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	LedgerEntryID string `json:"ledgerEntryId"`
}
