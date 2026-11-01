package events

// Procurement catalog event types (FR-17.18). Each is written to the outbox in the
// transaction that changes the catalog; the subject is the catalog id.
const (
	// CatalogPublished is written by :publish, when a DRAFT catalog opens to requesters.
	CatalogPublished = "dev.passage.procurement.catalog.published.v1"
	// CatalogRetired is written by :retire, when a PUBLISHED catalog closes.
	CatalogRetired = "dev.passage.procurement.catalog.retired.v1"
)

// CatalogData is the data member of catalog.published.v1 and catalog.retired.v1.
type CatalogData struct {
	CatalogID     string   `json:"catalog_id"`
	Number        string   `json:"number"`
	LegalEntityID string   `json:"legal_entity_id"`
	Name          string   `json:"name"`
	Status        string   `json:"status"`
	ValidFrom     string   `json:"valid_from"`
	ValidTo       string   `json:"valid_to,omitempty"`
	OrgUnitIDs    []string `json:"org_unit_ids"`
	EntryCount    int      `json:"entry_count"`
	Version       int      `json:"version"`
}
