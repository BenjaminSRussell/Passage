package events

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/events"
)

// OpenItemsImported is published once per opening-item import batch that created
// or changed at least one item (integration handoff H5).
var OpenItemsImported = events.Type(Context, "openitem", "imported", 1)

// OpenItemImportedRow is one created or updated item of an import batch.
// Amount and OpenAmount are signed: negative for a credit-side item.
type OpenItemImportedRow struct {
	OpenItemID string          `json:"open_item_id"`
	LegacyKey  string          `json:"legacy_key"`
	Outcome    string          `json:"outcome"`
	CustomerID string          `json:"customer_id"`
	Currency   string          `json:"currency"`
	Amount     decimal.Decimal `json:"amount"`
	OpenAmount decimal.Decimal `json:"open_amount"`
}

// OpenItemsImportedData is the payload of dev.passage.receivables.openitem.imported.v1.
type OpenItemsImportedData struct {
	BatchID       string                `json:"batch_id"`
	LegalEntityID string                `json:"legal_entity_id"`
	ImportSource  string                `json:"import_source"`
	Created       int                   `json:"created"`
	Updated       int                   `json:"updated"`
	Unchanged     int                   `json:"unchanged"`
	Rejected      int                   `json:"rejected"`
	Items         []OpenItemImportedRow `json:"items"`
}
