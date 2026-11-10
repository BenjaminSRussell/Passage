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

// CreditNoteBooked is dev.passage.billing.creditnote.booked.v1: the credit
// note's journal was booked in the general ledger (POST /v1/billing/gl-postings).
// It carries, per line, the allocated amount and the deferred part (Dr 2400)
// the journal reversed, which billing only knows once the original invoice is
// booked; revrec reverses its schedules by exactly these amounts (BR-29.6).
var CreditNoteBooked = events.Type("billing", "creditnote", "booked", 1)

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

// CreditNoteBookedLine is one line of a booked credit note. OrderID and
// Product are those of the credited original line (a credit note line has no
// source of its own) and are absent on free-text lines. Allocated is the
// revenue the journal debited for the line at the revrec allocation, Deferred
// the part of it debited to 2400 (deferred revenue), both positive magnitudes
// at the currency's minor unit.
type CreditNoteBookedLine struct {
	LineNo         int    `json:"line_no"`
	OriginalLineNo int    `json:"original_line_no"`
	OrderID        string `json:"order_id,omitempty"`
	Product        string `json:"product,omitempty"`
	Net            string `json:"net"`
	Allocated      string `json:"allocated"`
	Deferred       string `json:"deferred"`
}

// CreditNoteBookedData is the data member of creditnote.booked. JournalEntryID
// is the ledger entry of the credit note (invoice.journal_posting_id).
type CreditNoteBookedData struct {
	CreditNoteID      string                 `json:"credit_note_id"`
	Number            string                 `json:"number"`
	OriginalInvoiceID string                 `json:"original_invoice_id"`
	OriginalNumber    string                 `json:"original_number"`
	LegalEntityID     string                 `json:"legal_entity_id"`
	Currency          string                 `json:"currency"`
	PostingDate       string                 `json:"posting_date"`
	JournalEntryID    string                 `json:"journal_entry_id"`
	Lines             []CreditNoteBookedLine `json:"lines"`
}
