// Package events holds the budgeting event types and payloads.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

const Context = "budget"

var (
	RegisterPosted      = events.Type(Context, "register", "posted", 1)
	CheckCompleted      = events.Type(Context, "check", "completed", 1)
	EncumbranceRelieved = events.Type(Context, "encumbrance", "relieved", 1)
)

// All lists every event type this service publishes.
func All() []string {
	return []string{RegisterPosted, CheckCompleted, EncumbranceRelieved}
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
