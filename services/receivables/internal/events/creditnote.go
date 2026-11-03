package events

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
)

// CreditNotePosted is published when an RMA credit note is stored and posted (AC-30.5).
var CreditNotePosted = events.Type(Context, "creditnote", "posted", 1)

// CreditNotePostedData is the payload of dev.passage.receivables.creditnote.posted.v1.
type CreditNotePostedData struct {
	OpenItemID       string           `json:"open_item_id"`
	ReturnOrderID    string           `json:"return_order_id"`
	RMANumber        string           `json:"rma_number"`
	LegalEntityID    string           `json:"legal_entity_id"`
	CustomerID       string           `json:"customer_id"`
	DocumentNumber   string           `json:"document_number"`
	Currency         string           `json:"currency"`
	Gross            decimal.Decimal  `json:"gross"`
	Restocking       decimal.Decimal  `json:"restocking"`
	Net              decimal.Decimal  `json:"net"`
	JournalPostingID *string          `json:"journal_posting_id"`
	Postings         []domain.Posting `json:"postings"`
}
