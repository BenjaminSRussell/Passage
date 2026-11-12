// Package events holds the event types and payloads the intercompany service
// publishes through the outbox (events/asyncapi.yaml).
package events

import "github.com/BenjaminSRussell/passage/platform/events"

// Context is the bounded-context segment of every event type.
const Context = "interco"

// Source is the CloudEvents source of every event the service publishes.
const Source = "/passage/intercompany"

// PartnerCreated and PartnerUpdated are not in the PRD 7.40.5 event table.
// They are added so the mirror-saga worker (interco-svc, a separate
// deployable) and the sales and procurement services see every pairing and
// policy version the saga will apply (FR-40.1: policies versioned).
var (
	PartnerCreated = events.Type(Context, "partner", "created", 1)
	PartnerUpdated = events.Type(Context, "partner", "updated", 1)
)

// PartnerCreatedData is the payload of PartnerCreated.
type PartnerCreatedData struct {
	ID              string `json:"id"`
	SellerEntity    string `json:"sellerEntity"`
	BuyerEntity     string `json:"buyerEntity"`
	CustomerAccount string `json:"customerAccount"`
	VendorAccount   string `json:"vendorAccount"`
	Active          bool   `json:"active"`
	Pricing         string `json:"pricing"`
	Version         int    `json:"version"`
}

// PartnerUpdatedData is the payload of PartnerUpdated. ChangedFields names the
// API fields that changed (customerAccount, vendorAccount, active, actions,
// pricing, markupPct, tolerance).
type PartnerUpdatedData struct {
	ID            string   `json:"id"`
	SellerEntity  string   `json:"sellerEntity"`
	BuyerEntity   string   `json:"buyerEntity"`
	Active        bool     `json:"active"`
	ChangedFields []string `json:"changedFields"`
	Version       int      `json:"version"`
}

// Events of the posting, charge and reconciliation slices. JournalCompensated
// is not in the PRD 7.40.5 event table: it tells consumers that an IC journal
// whose second side was refused has been reversed.
var (
	JournalPosted      = events.Type(Context, "journal", "posted", 1)
	JournalCompensated = events.Type(Context, "journal", "compensated", 1)
	ChargePosted       = events.Type(Context, "charge", "posted", 1)
	// ChargeInvoiceRequested asks the invoice documents of the pairing to settle a charge in Invoice mode; nothing is posted here.
	ChargeInvoiceRequested = events.Type(Context, "chargeinvoice", "requested", 1)
	ReconciliationFlagged  = events.Type(Context, "reconciliation", "flagged", 1)
)

// JournalEntryData is the ledger entry one entity's side made.
type JournalEntryData struct {
	Entity          string `json:"entity"`
	LegalEntityID   string `json:"legalEntityId"`
	LedgerEntryID   string `json:"ledgerEntryId,omitempty"`
	ReversalEntryID string `json:"reversalEntryId,omitempty"`
}

// JournalData is the payload of JournalPosted and JournalCompensated.
type JournalData struct {
	ID          string             `json:"id"`
	PairID      string             `json:"pairId"`
	Source      string             `json:"source"`
	SourceRef   string             `json:"sourceRef"`
	PostingDate string             `json:"postingDate"`
	Status      string             `json:"status"`
	FailureCode string             `json:"failureCode,omitempty"`
	Entries     []JournalEntryData `json:"entries"`
}

// ChargePostedData is the payload of ChargePosted.
type ChargePostedData struct {
	ID        string `json:"id"`
	SourceID  string `json:"sourceId"`
	Version   int    `json:"version"`
	Lender    string `json:"lender"`
	Borrower  string `json:"borrower"`
	Currency  string `json:"currency"`
	Cost      string `json:"cost"`
	Price     string `json:"price"`
	Margin    string `json:"margin"`
	JournalID string `json:"journalId"`
}

// ReconciliationFlaggedData is the payload of ReconciliationFlagged: one event
// per run that left items to act on.
type ReconciliationFlaggedData struct {
	RunID    string `json:"runId"`
	Period   string `json:"period"`
	Currency string `json:"currency"`
	Timing   int    `json:"timing"`
	Mismatch int    `json:"mismatch"`
}

// ChargeInvoiceRequestedData is the payload of ChargeInvoiceRequested: what the lender's sales invoice and the borrower's vendor
// invoice are raised for.
type ChargeInvoiceRequestedData struct {
	ID       string `json:"id"`
	SourceID string `json:"sourceId"`
	Version  int    `json:"version"`
	Lender   string `json:"lender"`
	Borrower string `json:"borrower"`
	Category string `json:"category"`
	Role     string `json:"resourceRole,omitempty"`
	Date     string `json:"transactionDate"`
	Currency string `json:"currency"`
	Cost     string `json:"cost"`
	Price    string `json:"price"`
	Margin   string `json:"margin"`
	Tax      string `json:"tax"`
	TaxCode  string `json:"taxCode,omitempty"`
}
