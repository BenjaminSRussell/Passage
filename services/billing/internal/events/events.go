// Package events holds the billing event type names and payloads (AsyncAPI in
// events/asyncapi.yaml). Payload fields are snake_case per the catalog.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// InvoicePosted is dev.passage.billing.invoice.posted.v1.
var InvoicePosted = events.Type("billing", "invoice", "posted", 1)

// InvoicePostedLine is one posted line.
type InvoicePostedLine struct {
	LineNo         int    `json:"line_no"`
	Description    string `json:"description"`
	Qty            string `json:"qty"`
	Net            string `json:"net"`
	Tax            string `json:"tax"`
	TaxCode        string `json:"tax_code"`
	RevenueAccount string `json:"revenue_account"`
}

// InvoicePostedTotals are the header amounts as decimal strings.
type InvoicePostedTotals struct {
	Net     string `json:"net"`
	Charges string `json:"charges"`
	Tax     string `json:"tax"`
	Gross   string `json:"gross"`
}

// InvoicePostedData is the data member of invoice.posted.
type InvoicePostedData struct {
	InvoiceID        string              `json:"invoice_id"`
	Number           string              `json:"number"`
	Type             string              `json:"type"`
	LegalEntityID    string              `json:"legal_entity_id"`
	Customer         string              `json:"customer"`
	InvoiceAccount   string              `json:"invoice_account"`
	Currency         string              `json:"currency"`
	InvoiceDate      string              `json:"invoice_date"`
	PostingDate      string              `json:"posting_date"`
	DueDate          string              `json:"due_date"`
	Lines            []InvoicePostedLine `json:"lines"`
	Totals           InvoicePostedTotals `json:"totals"`
	JournalPostingID *string             `json:"journal_posting_id"`
	Intercompany     bool                `json:"intercompany"`
}
