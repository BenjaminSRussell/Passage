package events

const (
	BomVersionActivated    = "dev.depot.engineering.bomversion.activated.v1"
	BomVersionExpired      = "dev.depot.engineering.bomversion.expired.v1"
	EcoSubmitted           = "dev.depot.engineering.eco.submitted.v1"
	EcoApproved            = "dev.depot.engineering.eco.approved.v1"
	EcoReleased            = "dev.depot.engineering.eco.released.v1"
	EcrSubmitted           = "dev.depot.engineering.ecr.submitted.v1"
	EcrApproved            = "dev.depot.engineering.ecr.approved.v1"
	EcrRejected            = "dev.depot.engineering.ecr.rejected.v1"
	EcrCancelled           = "dev.depot.engineering.ecr.cancelled.v1"
	ReleaseSessionCreated  = "dev.depot.engineering.releasesession.created.v1"
	ReleaseLineAccepted    = "dev.depot.engineering.releaseline.accepted.v1"
	ReleasedProductChanged = "dev.depot.engineering.releasedproduct.changed.v1"
	DocumentSigned         = "dev.depot.engineering.document.signed.v1"
	DocumentReleased       = "dev.depot.engineering.document.released.v1"
	EbomImported           = "dev.depot.engineering.ebom.imported.v1"
)

type ReleaseSessionCreatedData struct {
	SessionID string `json:"sessionId"`
	Number    string `json:"number"`
	Source    string `json:"source"`
}

type ReleaseLineAcceptedData struct {
	LineID        string `json:"lineId"`
	SessionID     string `json:"sessionId"`
	LegalEntityID string `json:"legalEntityId"`
	AcceptedBy    string `json:"acceptedBy"`
}

type ReleasedProductChangedData struct {
	ReleasedProductID string `json:"releasedProductId"`
	ProductID         string `json:"productId"`
	LegalEntityID     string `json:"legalEntityId"`
	State             string `json:"state"`
}

type DocumentSignedData struct {
	DocumentID string `json:"documentId"`
	RecordID   string `json:"recordId"`
	SignerID   string `json:"signerId"`
	Meaning    string `json:"meaning"`
	SignedAt   string `json:"signedAt"`
}

type DocumentReleasedData struct {
	DocumentID string `json:"documentId"`
	Number     string `json:"number"`
	Version    string `json:"version"`
}

type EbomImportedData struct {
	ImportID   string `json:"importId"`
	PartNumber string `json:"partNumber"`
	Revision   string `json:"revision"`
	Added      int    `json:"added"`
	Exceptions int    `json:"exceptions"`
}

type BomVersionActivatedData struct {
	BomVersionID        string  `json:"bomVersionId"`
	ProductID           string  `json:"productId"`
	SiteID              string  `json:"siteId,omitempty"`
	EffectiveFrom       string  `json:"effectiveFrom"`
	EffectiveTo         *string `json:"effectiveTo,omitempty"`
	LineCount           int     `json:"lineCount"`
	SupersedesVersionID string  `json:"supersedesVersionId,omitempty"`
	// Usage and Category (FR-9.19) tell a consumer which kind of BOM changed: production order change
	// management reacts to a production BOM, not to a costing BOM activated beside it.
	Usage    string `json:"usage"`
	Category string `json:"category"`
}

type BomVersionExpiredData struct {
	BomVersionID string `json:"bomVersionId"`
	EffectiveTo  string `json:"effectiveTo"`
	EcoID        string `json:"ecoId"`
}

type EcoData struct {
	EcoID            string   `json:"ecoId"`
	EcoNumber        string   `json:"ecoNumber"`
	AffectedVersions []string `json:"affectedVersions"`
	ReleaseDate      string   `json:"releaseDate,omitempty"`
	ApproverID       string   `json:"approverId,omitempty"`
	EcrID            string   `json:"ecrId,omitempty"`
	LegalEntityID    string   `json:"legalEntityId,omitempty"`
}

const (
	ResourceChanged   = "dev.depot.engineering.resource.changed.v1"
	CalendarGenerated = "dev.depot.engineering.calendar.generated.v1"
)

type ResourceChangedData struct {
	ResourceID    string   `json:"resourceId"`
	SiteID        string   `json:"siteId,omitempty"`
	CalendarID    string   `json:"calendarId,omitempty"`
	EfficiencyPct string   `json:"efficiencyPct"`
	ChangedFields []string `json:"changedFields"`
}

type CalendarGeneratedData struct {
	CalendarID string `json:"calendarId"`
	From       string `json:"from"`
	To         string `json:"to"`
	OpenDays   int    `json:"openDays"`
}

const RouteVersionActivated = "dev.depot.engineering.routeversion.activated.v1"

type RouteVersionActivatedData struct {
	RouteVersionID string `json:"routeVersionId"`
	ProductID      string `json:"productId"`
	SiteID         string `json:"siteId"`
	EffectiveFrom  string `json:"effectiveFrom"`
}

type EcrData struct {
	EcrID       string   `json:"ecrId"`
	EcrNumber   string   `json:"ecrNumber"`
	Title       string   `json:"title"`
	Priority    string   `json:"priority"`
	Products    []string `json:"products"`
	RequestedBy string   `json:"requestedBy"`
	DecidedBy   string   `json:"decidedBy,omitempty"`
}
