package events

// SAReleaseCreated is written when a scheduling agreement release run creates a
// release (FR-17.15). The PRD names it sa.release.created.v1; it is published as
// sarelease.created.v1 because the platform type grammar allows one entity
// segment (as po.version.created is published as poversion.created). EDI
// DELFOR (FRC) and DELJIT (JIT) in wave 35, the supplier portal (wave 20) and
// planning (wave 22) consume it.
const SAReleaseCreated = "dev.passage.procurement.sarelease.created.v1"

// SAReleaseLine is one schedule row of the release in delivery-date order.
// Quantities are decimal strings; zone is FIRM, TRADEOFF or FORECAST on the run
// date and in_horizon marks rows inside the kind's horizon.
type SAReleaseLine struct {
	LineNo             int    `json:"line_no"`
	ScheduleLineNo     int    `json:"schedule_line_no"`
	ProductID          string `json:"product_id"`
	DeliveryDate       string `json:"delivery_date"`
	Qty                string `json:"qty"`
	CumulativeReleased string `json:"cumulative_released"`
	CumulativeReceived string `json:"cumulative_received"`
	Zone               string `json:"zone"`
	InHorizon          bool   `json:"in_horizon"`
}

// SAReleaseCreatedData is the data member of sarelease.created.v1.
type SAReleaseCreatedData struct {
	AgreementID        string          `json:"agreement_id"`
	AgreementNumber    string          `json:"agreement_number"`
	LegalEntityID      string          `json:"legal_entity_id"`
	ReleaseNumber      string          `json:"release_number"`
	ReleaseNo          int             `json:"release_no"`
	Kind               string          `json:"kind"`
	RunDate            string          `json:"run_date"`
	VendorID           string          `json:"vendor_id"`
	ProductID          string          `json:"product_id"`
	SiteID             string          `json:"site_id"`
	Unit               string          `json:"unit"`
	CumulativeReset    bool            `json:"cumulative_reset"`
	CumulativeReleased string          `json:"cumulative_released"`
	CumulativeReceived string          `json:"cumulative_received"`
	Lines              []SAReleaseLine `json:"lines"`
}
