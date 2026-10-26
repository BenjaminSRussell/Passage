package events

import "github.com/BenjaminSRussell/passage/platform/events"

const Context = "org"

var (
	LegalEntityCreated      = events.Type(Context, "legalentity", "created", 1)
	LegalEntityUpdated      = events.Type(Context, "legalentity", "updated", 1)
	OperatingUnitCreated    = events.Type(Context, "operatingunit", "created", 1)
	OperatingUnitUpdated    = events.Type(Context, "operatingunit", "updated", 1)
	SiteCreated             = events.Type(Context, "site", "created", 1)
	SiteUpdated             = events.Type(Context, "site", "updated", 1)
	WarehouseCreated        = events.Type(Context, "warehouse", "created", 1)
	WarehouseUpdated        = events.Type(Context, "warehouse", "updated", 1)
	LocationCreated         = events.Type(Context, "location", "created", 1)
	LocationUpdated         = events.Type(Context, "location", "updated", 1)
	CurrencyRoundingChanged = events.Type(Context, "currencyrounding", "changed", 1)
	AddressChanged          = events.Type(Context, "address", "changed", 1)
	FiscalCalendarGenerated = events.Type(Context, "fiscalcalendar", "generated", 1)

	HierarchyVersionActivated = events.Type(Context, "hierarchyversion", "activated", 1)
	TeamMembershipChanged     = events.Type(Context, "teammembership", "changed", 1)
)

func All() []string {
	return []string{LegalEntityCreated, LegalEntityUpdated, OperatingUnitCreated, OperatingUnitUpdated, SiteCreated, SiteUpdated, WarehouseCreated, WarehouseUpdated, LocationCreated, LocationUpdated, CurrencyRoundingChanged, AddressChanged, FiscalCalendarGenerated, HierarchyVersionActivated, TeamMembershipChanged}
}

type LegalEntityCreatedData struct {
	ID                 string `json:"id"`
	Code               string `json:"code"`
	Name               string `json:"name"`
	CountryCode        string `json:"country_code"`
	AccountingCurrency string `json:"accounting_currency"`
	TimeZone           string `json:"time_zone"`
}

type LegalEntityUpdatedData struct {
	ID            string   `json:"id"`
	ChangedFields []string `json:"changed_fields"`
	Version       int      `json:"version"`
}

type OperatingUnitCreatedData struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Code string `json:"code"`
	Name string `json:"name"`
}

type OperatingUnitUpdatedData struct {
	ID            string   `json:"id"`
	ChangedFields []string `json:"changed_fields"`
	Version       int      `json:"version"`
}

type SiteCreatedData struct {
	ID            string  `json:"id"`
	LegalEntityID string  `json:"legal_entity_id"`
	ParentID      *string `json:"parent_id"`
	Type          *string `json:"type"`
	AddressID     *string `json:"address_id"`

	DefaultWarehouseID *string `json:"default_warehouse_id,omitempty"`
}

type SiteUpdatedData struct {
	ID            string   `json:"id"`
	ChangedFields []string `json:"changed_fields"`
	Version       int      `json:"version"`
}

type WarehouseCreatedData struct {
	ID            string  `json:"id"`
	LegalEntityID string  `json:"legal_entity_id"`
	ParentID      *string `json:"parent_id"`
	Type          *string `json:"type"`
	AddressID     *string `json:"address_id"`
}

type LocationUpdatedData struct {
	ID            string   `json:"id"`
	ChangedFields []string `json:"changed_fields"`
	Version       int      `json:"version"`
}

type LocationCreatedData struct {
	ID            string  `json:"id"`
	LegalEntityID string  `json:"legal_entity_id"`
	ParentID      *string `json:"parent_id"`
	Type          *string `json:"type"`
	AddressID     *string `json:"address_id"`
}

type WarehouseUpdatedData struct {
	ID            string   `json:"id"`
	ChangedFields []string `json:"changed_fields"`
	Version       int      `json:"version"`
}

type CurrencyRoundingChangedData struct {
	CurrencyCode  string  `json:"currency_code"`
	Purpose       string  `json:"purpose"`
	LegalEntityID *string `json:"legal_entity_id"`
	Unit          string  `json:"unit"`
	Method        string  `json:"method"`
}

type AddressChangedData struct {
	AddressID string   `json:"address_id"`
	VersionID string   `json:"version_id"`
	OwnerType string   `json:"owner_type"`
	OwnerID   string   `json:"owner_id"`
	Purposes  []string `json:"purposes"`
	ValidFrom string   `json:"valid_from"`
}

type FiscalPeriodData struct {
	PeriodNo  int    `json:"period_no"`
	Name      string `json:"name"`
	StartDate string `json:"start_date"`
	EndDate   string `json:"end_date"`
	Type      string `json:"type"`
	Week53    bool   `json:"week53"`
}

type FiscalCalendarGeneratedData struct {
	CalendarID string             `json:"calendar_id"`
	FiscalYear int                `json:"fiscal_year"`
	Periods    []FiscalPeriodData `json:"periods"`
}

type HierarchyVersionActivatedData struct {
	HierarchyTypeID     string   `json:"hierarchy_type_id"`
	Purposes            []string `json:"purposes"`
	VersionID           string   `json:"version_id"`
	VersionNo           int      `json:"version_no"`
	EffectiveFrom       string   `json:"effective_from"`
	NodeCount           int      `json:"node_count"`
	SupersededVersionID *string  `json:"superseded_version_id"`
	BackdateReason      *string  `json:"backdate_reason"`
}

type TeamMemberRef struct {
	MemberKind string `json:"member_kind"`
	MemberID   string `json:"member_id"`
	MemberRole string `json:"member_role"`
}

type TeamMembershipChangedData struct {
	TeamID  string          `json:"team_id"`
	Added   []TeamMemberRef `json:"added"`
	Removed []TeamMemberRef `json:"removed"`
	AsOf    string          `json:"as_of"`
}
