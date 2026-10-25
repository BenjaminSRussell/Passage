package events

import "github.com/BenjaminSRussell/passage/platform/events"

const Context = "org"

var (
	LegalEntityCreated      = events.Type(Context, "legalentity", "created", 1)
	LegalEntityUpdated      = events.Type(Context, "legalentity", "updated", 1)
	OperatingUnitCreated    = events.Type(Context, "operatingunit", "created", 1)
	OperatingUnitUpdated    = events.Type(Context, "operatingunit", "updated", 1)
	SiteCreated             = events.Type(Context, "site", "created", 1)
	WarehouseCreated        = events.Type(Context, "warehouse", "created", 1)
	LocationCreated         = events.Type(Context, "location", "created", 1)
	CurrencyRoundingChanged = events.Type(Context, "currencyrounding", "changed", 1)
)

func All() []string {
	return []string{LegalEntityCreated, LegalEntityUpdated, OperatingUnitCreated, OperatingUnitUpdated, SiteCreated, WarehouseCreated, LocationCreated, CurrencyRoundingChanged}
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
