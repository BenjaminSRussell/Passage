package seed

import (
	"context"
	"fmt"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const DemoManufacturer = "demo-manufacturer"

type legalEntitySeed struct {
	code, name, country, currency, timeZone, language string
}

type siteSeed struct {
	legalEntity, code, name, timeZone string
	address                           store.Address
	warehouses                        []warehouseSeed
}

type warehouseSeed struct {
	code, name, kind string
	locations        []locationSeed
}

type locationSeed struct{ code, name, kind string }

type operatingUnitSeed struct{ code, name, kind, legalEntity string }

type teamSeed struct{ code, name, teamType, operatingUnit string }

var (
	legalEntities = []legalEntitySeed{
		{"USMF", "Demo Manufacturing USA", "US", "USD", "America/Chicago", "en-US"},
		{"DEMF", "Demo Fertigung Deutschland", "DE", "EUR", "Europe/Berlin", "de-DE"},
		{"FRMF", "Demo Fabrication France", "FR", "EUR", "Europe/Paris", "fr-FR"},
	}
	sites = []siteSeed{
		{"USMF", "DET", "Detroit Plant", "America/Detroit",
			store.Address{CountryCode: "US", City: "Detroit", Street1: "1200 Industrial Way", RegionCode: ptr("MI"), PostalCode: ptr("48201")},
			[]warehouseSeed{
				{"DET-MAIN", "Detroit Main Warehouse", "standard", []locationSeed{{"RCV-01", "Receiving Dock", "receiving"}, {"STO-A1", "Storage Aisle A1", "storage"}, {"SHP-01", "Shipping Dock", "shipping"}}},
				{"DET-QA", "Detroit Quarantine", "quarantine", []locationSeed{{"QA-01", "Quality Hold", "quarantine"}}},
				{"DET-SUB", "Detroit Subcontractor Stock", "subcontractor", nil},
			}},
		{"USMF", "AUS", "Austin Plant", "America/Chicago",
			store.Address{CountryCode: "US", City: "Austin", Street1: "88 Fabrication Blvd", RegionCode: ptr("TX"), PostalCode: ptr("78701")},
			[]warehouseSeed{
				{"AUS-MAIN", "Austin Main Warehouse", "standard", []locationSeed{{"RCV-01", "Receiving Dock", "receiving"}, {"STO-B1", "Storage Aisle B1", "storage"}}},
				{"AUS-TRN", "Austin Transit", "transit", nil},
			}},
	}
	operatingUnits = []operatingUnitSeed{
		{"OPS", "Operations", "business_unit", "USMF"},
		{"FIN", "Finance", "department", "USMF"},
		{"PROC", "Procurement", "department", "USMF"},
		{"QUAL", "Quality", "department", "USMF"},
		{"DET-ASSY", "Detroit Assembly", "value_stream", "USMF"},
		{"AUS-MACH", "Austin Machining", "value_stream", "USMF"},
		{"CC-1100", "Detroit Assembly Cost Center", "cost_center", "USMF"},
		{"CC-1200", "Austin Machining Cost Center", "cost_center", "USMF"},
		{"DE-OPS", "Fertigung Betrieb", "business_unit", "DEMF"},
		{"DE-FIN", "Finanzen", "department", "DEMF"},
		{"FR-OPS", "Exploitation", "business_unit", "FRMF"},
		{"FR-FIN", "Finance France", "department", "FRMF"},
	}
	teamTypes = []store.TeamType{
		{Code: "STEWARDSHIP", Name: "Data stewardship", MemberKinds: []string{"user", "party"}},
		{Code: "PLANT", Name: "Plant crew", MemberKinds: []string{"user"}},
	}
	teams = []teamSeed{
		{"MD-STEWARDS", "Master data stewards", "STEWARDSHIP", ""},
		{"DET-LEADS", "Detroit shift leads", "PLANT", "DET-ASSY"},
		{"AUS-LEADS", "Austin shift leads", "PLANT", "AUS-MACH"},
	}
)

const (
	validFrom = "2026-01-01"
	keyPrefix = "seed:" + DemoManufacturer + ":"
)

func ptr(s string) *string { return &s }

func Run(ctx context.Context, st store.Store, tenantID, name string) error {
	if name != DemoManufacturer {
		return fmt.Errorf("unknown seed package %q; known packages: %s", name, DemoManufacturer)
	}
	ctx = tenant.With(ctx, tenant.Principal{TenantID: tenantID})
	entityIDs := map[string]string{}
	for _, e := range legalEntities {
		created, err := st.CreateLegalEntity(ctx, store.LegalEntity{Code: e.code, Name: e.name, Country: e.country, Currency: e.currency, TimeZone: e.timeZone, DefaultLanguage: e.language}, keyPrefix+"le:"+e.code)
		if err != nil {
			return fmt.Errorf("legal entity %s: %w", e.code, err)
		}
		entityIDs[e.code] = created.ID
	}
	unitIDs := map[string]string{}
	for _, u := range operatingUnits {
		entity := entityIDs[u.legalEntity]
		created, err := st.CreateOperatingUnit(ctx, store.OperatingUnit{Code: u.code, Name: u.name, Type: u.kind, DefaultLegalEntityID: &entity}, keyPrefix+"ou:"+u.code)
		if err != nil {
			return fmt.Errorf("operating unit %s: %w", u.code, err)
		}
		unitIDs[u.code] = created.ID
	}
	for _, s := range sites {
		if err := seedSite(ctx, st, entityIDs[s.legalEntity], s); err != nil {
			return fmt.Errorf("site %s: %w", s.code, err)
		}
	}
	return seedTeams(ctx, st, unitIDs)
}

func seedSite(ctx context.Context, st store.Store, legalEntityID string, s siteSeed) error {
	first := s.warehouses[0]
	site, err := st.CreateSite(ctx, store.Site{LegalEntityID: legalEntityID, Code: s.code, Name: s.name, TimeZone: s.timeZone,
		DefaultWarehouse: &store.Warehouse{Code: first.code, Name: first.name, Type: first.kind}}, keyPrefix+"site:"+s.code)
	if err != nil {
		return err
	}
	warehouseIDs := map[string]string{first.code: deref(site.DefaultWarehouseID)}
	for _, w := range s.warehouses[1:] {
		created, err := st.CreateWarehouse(ctx, store.Warehouse{SiteID: site.ID, Code: w.code, Name: w.name, Type: w.kind}, keyPrefix+"wh:"+w.code)
		if err != nil {
			return fmt.Errorf("warehouse %s: %w", w.code, err)
		}
		warehouseIDs[w.code] = created.ID
	}
	for _, w := range s.warehouses {
		for _, l := range w.locations {
			if _, err := st.CreateLocation(ctx, store.Location{WarehouseID: warehouseIDs[w.code], Code: l.code, Name: l.name, Type: l.kind}, keyPrefix+"loc:"+w.code+":"+l.code); err != nil {
				return fmt.Errorf("location %s/%s: %w", w.code, l.code, err)
			}
		}
	}
	return seedSiteAddress(ctx, st, site, s.address, s.code)
}

func seedSiteAddress(ctx context.Context, st store.Store, site store.Site, address store.Address, code string) error {
	address.OwnerType, address.OwnerID, address.ValidFrom, address.GeoStatus = "site", site.ID, validFrom, "unverified"
	address.Purposes = []store.AddressPurpose{{Purpose: "business", Primary: true}}
	created, err := st.CreateAddress(ctx, address, keyPrefix+"addr:"+code)
	if err != nil {
		return fmt.Errorf("address: %w", err)
	}
	current, err := st.GetSite(ctx, site.ID)
	if err != nil || current.AddressID != nil {
		return err
	}
	_, err = st.UpdateSite(ctx, site.ID, current.Version, store.SitePatch{AddressID: &created.ID})
	return err
}

func seedTeams(ctx context.Context, st store.Store, unitIDs map[string]string) error {
	typeIDs := map[string]string{}
	for _, t := range teamTypes {
		created, _, err := st.CreateTeamType(ctx, t, keyPrefix+"team-type:"+t.Code)
		if err != nil {
			return fmt.Errorf("team type %s: %w", t.Code, err)
		}
		typeIDs[t.Code] = created.ID
	}
	for _, t := range teams {
		team := store.Team{Code: t.code, Name: t.name, TeamTypeID: typeIDs[t.teamType], EffectiveFrom: validFrom}
		if t.operatingUnit != "" {
			unit := unitIDs[t.operatingUnit]
			team.OperatingUnitID = &unit
		}
		if _, _, err := st.CreateTeam(ctx, team, keyPrefix+"team:"+t.code); err != nil {
			return fmt.Errorf("team %s: %w", t.code, err)
		}
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
