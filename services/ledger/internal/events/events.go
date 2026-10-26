// Package events holds the ledger's CloudEvents type names and payloads.
// Event types follow dev.passage.<context>.<entity>.<verb>.v<n> (naming rule in
// the platform events package; checked by the tests in this package).
package events

// EventSource is the CloudEvents source of every ledger event (/passage/<context>/<service>).
const EventSource = "/passage/ledger/ledger"

// Event types published by the ledger (see events/asyncapi.yaml).
const (
	JournalPosted   = "dev.passage.ledger.journal.posted.v1"
	JournalReversed = "dev.passage.ledger.journal.reversed.v1"
	BalanceRebuilt  = "dev.passage.ledger.balance.rebuilt.v1"
)

// Source is the FR-11.12 source object carried on every entry.
type Source struct {
	System string `json:"system"`
	Type   string `json:"type"`
	ID     string `json:"id"`
	URI    string `json:"uri,omitempty"`
}

// Amounts are the entry totals in the ledger currency (decimal strings).
type Amounts struct {
	Currency string `json:"currency"`
	Debit    string `json:"debit"`
	Credit   string `json:"credit"`
}

// JournalPostedData is the payload of dev.passage.ledger.journal.posted.v1.
type JournalPostedData struct {
	EntryID         string  `json:"entryId"`
	LedgerID        string  `json:"ledgerId"`
	DocumentID      string  `json:"documentId"`
	Voucher         string  `json:"voucher"`
	PostingSequence int64   `json:"postingSequence"`
	PostingDate     string  `json:"postingDate"`
	PeriodKey       string  `json:"periodKey"`
	LineCount       int     `json:"lineCount"`
	Totals          Amounts `json:"totals"`
	Source          Source  `json:"source"`
	TraceID         string  `json:"traceId"`
}

// JournalReversedData is the payload of dev.passage.ledger.journal.reversed.v1.
type JournalReversedData struct {
	OriginalEntryID  string `json:"originalEntryId"`
	ReversingEntryID string `json:"reversingEntryId"`
	LedgerID         string `json:"ledgerId"`
	PostingDate      string `json:"postingDate"`
}

// BalanceRebuiltData is the payload of dev.passage.ledger.balance.rebuilt.v1.
type BalanceRebuiltData struct {
	LedgerID          string `json:"ledgerId"`
	HighWaterSequence int64  `json:"highWaterSequence"`
	RowsWritten       int64  `json:"rowsWritten"`
	RowsDiffering     int64  `json:"rowsDiffering"`
	DurationMs        int64  `json:"durationMs"`
}
