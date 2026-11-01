package domain

import (
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Procurement catalog statuses and actions (FR-17.18).
const (
	CatalogDraft     = "DRAFT"
	CatalogPublished = "PUBLISHED"
	CatalogRetired   = "RETIRED"

	CatalogPublish = "publish"
	CatalogRetire  = "retire"
)

// Catalog message codes (PRD 3.17.18 and the service README).
const (
	CodeCatalogState       = "PRC-CAT-STATE"
	CodeCatalogEmpty       = "PRC-CAT-001"
	CodeCatalogUnavailable = "PRC-CAT-004"
	CodeCatalogCurrency    = "PRC-CAT-006"
)

// MaxCatalogEntries caps the entries of one catalog. The PRD sets no catalog cap, only
// the 50,000-row limit of one vendor file, so a catalog holds at least one full file;
// reads page the entries (GET /catalogs/{id}/entries) rather than return them all.
const MaxCatalogEntries = MaxImportRows

// Catalog is a procurement catalog (FR-17.18): vendor product entries published
// to operating units for a validity window. OrgUnitIDs empty means every unit.
type Catalog struct {
	ID            string   `json:"id"`
	Number        string   `json:"number"`
	LegalEntityID string   `json:"legalEntityId"`
	Name          string   `json:"name"`
	Description   string   `json:"description,omitempty"`
	Status        string   `json:"status"`
	ValidFrom     Day      `json:"validFrom"`
	ValidTo       *Day     `json:"validTo,omitempty"`
	OrgUnitIDs    []string `json:"orgUnitIds"`
	Version       int      `json:"version"`
	// EntryCount is the number of entries; Entries holds the first page of them in
	// entry order, and EntriesNextCursor, when set, pages on through GET .../entries.
	EntryCount        int            `json:"entryCount"`
	Entries           []CatalogEntry `json:"entries"`
	EntriesNextCursor string         `json:"entriesNextCursor,omitempty"`
	CreatedAt         time.Time      `json:"createdAt"`
	UpdatedAt         time.Time      `json:"updatedAt"`
}

// CatalogEntry is one orderable item: a vendor's product at a price and lead time.
// Preferred marks the buyer's first choice in the requisition picker.
type CatalogEntry struct {
	ID           string          `json:"id"`
	EntryNo      int             `json:"entryNo"`
	VendorID     string          `json:"vendorId"`
	VendorPartNo string          `json:"vendorPartNo,omitempty"`
	ProductID    string          `json:"productId,omitempty"`
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	Category     string          `json:"category"`
	Unit         string          `json:"unit"`
	UnitPrice    decimal.Decimal `json:"unitPrice"`
	Currency     string          `json:"currency"`
	LeadTimeDays int             `json:"leadTimeDays"`
	Preferred    bool            `json:"preferred"`
	ImageURL     string          `json:"imageUrl,omitempty"`
}

// NextCatalogStatus applies an action: publish takes a DRAFT with at least one
// entry to PUBLISHED, retire takes a PUBLISHED catalog to RETIRED. Anything else
// is PRC-CAT-STATE; publishing an empty catalog is PRC-CAT-001.
func NextCatalogStatus(status, action string, entries int) (string, error) {
	switch {
	case action == CatalogPublish && status == CatalogDraft:
		if entries == 0 {
			return "", fail(CodeCatalogEmpty, "a catalog needs at least one entry before it is published")
		}
		return CatalogPublished, nil
	case action == CatalogRetire && status == CatalogPublished:
		return CatalogRetired, nil
	}
	return "", fail(CodeCatalogState, "cannot %s a %s catalog", action, strings.ToLower(status))
}

// AvailableOn reports whether a requester in orgUnitID can pick from the catalog
// on date d: published, inside its validity, and published to the unit (or to all).
func (c Catalog) AvailableOn(d Day, orgUnitID string) bool {
	if c.Status != CatalogPublished || d.Before(c.ValidFrom.Time) || (c.ValidTo != nil && d.After(c.ValidTo.Time)) {
		return false
	}
	if len(c.OrgUnitIDs) == 0 {
		return true
	}
	for _, id := range c.OrgUnitIDs {
		if id == orgUnitID {
			return true
		}
	}
	return false
}

// CheckForRequisition verifies that an entry picked into a requisition line of the
// given currency, legal entity and unit can be ordered from this catalog on d.
func (c Catalog) CheckForRequisition(e CatalogEntry, legalEntityID, orgUnitID, currency string, d Day) error {
	if c.LegalEntityID != legalEntityID || !c.AvailableOn(d, orgUnitID) {
		return fail(CodeCatalogUnavailable, "catalog %s is not published to this legal entity and unit on %s", c.Number, d)
	}
	if e.Currency != currency {
		return fail(CodeCatalogCurrency, "catalog price is in %s but the requisition is in %s", e.Currency, currency)
	}
	return nil
}
