package events

// OwnershipChanged is emitted when an OWNERSHIP_CHANGE journal posts
// (FR-16.17); wave 18 (FR-18.16) builds the consignment settlement invoice from
// it.
const OwnershipChanged = "dev.passage.inventory.ownership.changed.v1"

// OwnershipLineData is one line of an ownership change: the vendor-owned
// dimension set it moved (owner included), the quantity and unit, the
// consignment price and the accounting amount (quantity x price, rounded to
// the currency's minor unit).
type OwnershipLineData struct {
	JournalLineID    string            `json:"journalLineId"`
	ProductID        string            `json:"productId"`
	DimensionSet     map[string]string `json:"dimensionSet"`
	Qty              string            `json:"qty"`
	Unit             string            `json:"unit"`
	ConsignmentPrice string            `json:"consignmentPrice"`
	AmountAcct       string            `json:"amountAcct"`
}

// OwnershipChangedData is the payload of ownership.changed. TotalAmount is the
// sum of the line amounts, the Dr inventory / Cr consignment accrual amount.
// LedgerEntryID stays null until the wave 11 ledger Post exists.
type OwnershipChangedData struct {
	JournalID     string              `json:"journalId"`
	LegalEntityID string              `json:"legalEntityId"`
	VendorID      string              `json:"vendorId"`
	PostingDate   string              `json:"postingDate"`
	Currency      string              `json:"currency"`
	TotalAmount   string              `json:"totalAmount"`
	LedgerEntryID *string             `json:"ledgerEntryId"`
	Lines         []OwnershipLineData `json:"lines"`
}
