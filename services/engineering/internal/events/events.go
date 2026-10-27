package events

const (
	BomVersionActivated = "dev.passage.engineering.bomversion.activated.v1"
	BomVersionExpired   = "dev.passage.engineering.bomversion.expired.v1"
	EcoSubmitted        = "dev.passage.engineering.eco.submitted.v1"
	EcoApproved         = "dev.passage.engineering.eco.approved.v1"
	EcoReleased         = "dev.passage.engineering.eco.released.v1"
)

type BomVersionActivatedData struct {
	BomVersionID        string  `json:"bomVersionId"`
	ProductID           string  `json:"productId"`
	SiteID              string  `json:"siteId,omitempty"`
	EffectiveFrom       string  `json:"effectiveFrom"`
	EffectiveTo         *string `json:"effectiveTo,omitempty"`
	LineCount           int     `json:"lineCount"`
	SupersedesVersionID string  `json:"supersedesVersionId,omitempty"`
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
