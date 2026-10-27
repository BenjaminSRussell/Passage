// Package events holds the billing event type names and payloads (AsyncAPI in
// events/asyncapi.yaml). Payload fields are snake_case per the catalog.
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// InvoicePosted is dev.passage.billing.invoice.posted.v1.
var InvoicePosted = events.Type("billing", "invoice", "posted", 1)

// CreditNotePosted is dev.passage.billing.creditnote.posted.v1. The PRD writes
// the entity as "credit-note"; the platform event grammar allows lowercase
// letters and digits only, so the entity is "creditnote" (like "packingslip").
var CreditNotePosted = events.Type("billing", "creditnote", "posted", 1)

// InvoicePostedLine is one posted line. A shipment or consolidated invoice line
// also names its order line, product and packing slip line, so sales and
// fulfillment can mark them invoiced (AC-29.5) and revrec can find the
// contract; the fields are absent on free-text lines and credit notes.
type InvoicePostedLine struct {
	LineNo            int    `json:"line_no"`
	Description       string `json:"description"`
	Qty               string `json:"qty"`
	Net               string `json:"net"`
	Tax               string `json:"tax"`
	TaxCode           string `json:"tax_code"`
	RevenueAccount    string `json:"revenue_account"`
	OrderLineID       string `json:"order_line_id,omitempty"`
	Product           string `json:"product,omitempty"`
	PackingSlipLineID string `json:"packing_slip_line_id,omitempty"`
}

// InvoicePostedTotals are the header amounts as decimal strings.
type InvoicePostedTotals struct {
	Net     string `json:"net"`
	Charges string `json:"charges"`
	Tax     string `json:"tax"`
	Gross   string `json:"gross"`
}

// InvoicePostedInstallment is one open item the receivables context opens at
// post (FR-29.18, BR-29.11). The amounts of an invoice's installments sum to
// its gross exactly; tax is reported once on the invoice, not per installment.
type InvoicePostedInstallment struct {
	No      int    `json:"no"`
	Amount  string `json:"amount"`
	DueDate string `json:"due_date"`
}

// InvoicePostedData is the data member of invoice.posted.
type InvoicePostedData struct {
	InvoiceID      string              `json:"invoice_id"`
	Number         string              `json:"number"`
	Type           string              `json:"type"`
	LegalEntityID  string              `json:"legal_entity_id"`
	Customer       string              `json:"customer"`
	InvoiceAccount string              `json:"invoice_account"`
	Currency       string              `json:"currency"`
	InvoiceDate    string              `json:"invoice_date"`
	PostingDate    string              `json:"posting_date"`
	DueDate        string              `json:"due_date"`
	Lines          []InvoicePostedLine `json:"lines"`
	Totals         InvoicePostedTotals `json:"totals"`
	// Installments is empty when the invoice has no installment plan: the
	// gross is then one open item due on due_date.
	Installments     []InvoicePostedInstallment `json:"installments"`
	JournalPostingID *string                    `json:"journal_posting_id"`
	Intercompany     bool                       `json:"intercompany"`
}

// CreditNotePostedLine is one credited line with the original line it reverses.
type CreditNotePostedLine struct {
	InvoicePostedLine
	OriginalLineNo int `json:"original_line_no"`
}

// CreditNotePostedData is the data member of creditnote.posted (FR-29.5).
// Amounts are positive magnitudes: the credit note reduces AR, revenue and tax
// by these amounts. Receivables settles the original's open items with it;
// revrec reverses recognized and deferred revenue (BR-29.6).
type CreditNotePostedData struct {
	CreditNoteID      string                 `json:"credit_note_id"`
	Number            string                 `json:"number"`
	OriginalInvoiceID string                 `json:"original_invoice_id"`
	OriginalNumber    string                 `json:"original_number"`
	Reason            string                 `json:"reason"`
	LegalEntityID     string                 `json:"legal_entity_id"`
	Customer          string                 `json:"customer"`
	InvoiceAccount    string                 `json:"invoice_account"`
	Currency          string                 `json:"currency"`
	CreditDate        string                 `json:"credit_date"`
	PostingDate       string                 `json:"posting_date"`
	Lines             []CreditNotePostedLine `json:"lines"`
	Totals            InvoicePostedTotals    `json:"totals"`
	JournalPostingID  *string                `json:"journal_posting_id"`
}
