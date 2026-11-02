package events

const (
	ReconciliationMatched   = "dev.passage.treasury.reconciliation.matched.v1"
	ReconciliationUnmatched = "dev.passage.treasury.reconciliation.unmatched.v1"
)

// ReconciliationMatchedData is the payload of ReconciliationMatched, published
// for each auto or manual match (7.36.5, FR-36.3).
type ReconciliationMatchedData struct {
	MatchID       string `json:"matchId"`
	StatementID   string `json:"statementId"`
	BankAccountID string `json:"bankAccountId"`
	LegalEntityID string `json:"legalEntityId"`
	Kind          string `json:"kind"`
	LineCount     int    `json:"lineCount"`
	ItemCount     int    `json:"itemCount"`
	Score         *int   `json:"score,omitempty"`
}

// ReconciliationUnmatchedData is the payload of ReconciliationUnmatched.
type ReconciliationUnmatchedData struct {
	MatchID       string `json:"matchId"`
	StatementID   string `json:"statementId"`
	BankAccountID string `json:"bankAccountId"`
	LegalEntityID string `json:"legalEntityId"`
	Reason        string `json:"reason"`
	UnmatchedBy   string `json:"unmatchedBy"`
}
