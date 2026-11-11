// Package events holds the event types and payloads treasury publishes.
package events

// Context is the bounded-context segment of every treasury event type.
const Context = "treasury"

// StatementImported is emitted when a statement and its lines commit (7.36.5).
// It equals events.Type(Context, "statement", "imported", 1); a test checks that.
const StatementImported = "dev.passage.treasury.statement.imported.v1"

// All lists every event type this service publishes.
func All() []string {
	return []string{StatementImported, CheckVoided, ReconciliationMatched, ReconciliationUnmatched,
		CashDeskCreated, CashDeskUpdated, CashDeskDeactivated, CashDeskDayClosed, CashDeskEntryPosted}
}

// StatementImportedData is the payload of StatementImported. Amounts are decimal strings.
type StatementImportedData struct {
	StatementID      string `json:"statementId"`
	BankStatementRef string `json:"bankStatementRef"`
	BankAccountID    string `json:"bankAccountId"`
	LegalEntityID    string `json:"legalEntityId"`
	LineCount        int    `json:"lineCount"`
	OpeningBalance   string `json:"openingBalance"`
	ClosingBalance   string `json:"closingBalance"`
}
