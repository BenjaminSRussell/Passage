package events

// ReconciliationDiff is emitted when a daily inventory-to-ledger run finds a
// non-zero difference (FR-16.12); a matched run emits nothing. A re-run on the
// same day that still differs emits it again for the same run id.
const ReconciliationDiff = "dev.passage.inventory.reconciliation.diff.v1"

// ReconciliationDiffData is the payload of reconciliation.diff. Amounts are
// decimal strings in Currency, rounded to its minor unit; Diff is
// InventoryValue minus LedgerBalance. NegativeRows counts the on-hand rows of
// the legal entity with negative physical stock at the run (FR-16.4).
type ReconciliationDiffData struct {
	RunID          string `json:"runId"`
	LegalEntityID  string `json:"legalEntityId"`
	Account        string `json:"account"`
	Currency       string `json:"currency"`
	RunDate        string `json:"runDate"`
	InventoryValue string `json:"inventoryValue"`
	LedgerBalance  string `json:"ledgerBalance"`
	Diff           string `json:"diff"`
	LedgerSource   string `json:"ledgerSource"`
	NegativeRows   int    `json:"negativeRows"`
}
