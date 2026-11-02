package events

const (
	BomVersionActivated = "dev.passage.engineering.bomversion.activated.v1"
	BomVersionExpired   = "dev.passage.engineering.bomversion.expired.v1"
	EcoSubmitted        = "dev.passage.engineering.eco.submitted.v1"
	EcoApproved         = "dev.passage.engineering.eco.approved.v1"
	EcoReleased         = "dev.passage.engineering.eco.released.v1"
	EcrSubmitted        = "dev.passage.engineering.ecr.submitted.v1"
	EcrApproved         = "dev.passage.engineering.ecr.approved.v1"
	EcrRejected         = "dev.passage.engineering.ecr.rejected.v1"
	EcrCancelled        = "dev.passage.engineering.ecr.cancelled.v1"
)

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
}

const (
	ResourceChanged   = "dev.passage.engineering.resource.changed.v1"
	CalendarGenerated = "dev.passage.engineering.calendar.generated.v1"
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

const RouteVersionActivated = "dev.passage.engineering.routeversion.activated.v1"

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
