package seed

import (
	"context"
	"fmt"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	DemoManufacturer = "demo-manufacturer"
	validFrom        = "2026-01-01"
	keyPrefix        = "seed:" + DemoManufacturer + ":"
	listAll          = 10000
	operatingUnitSum = 40
)

type Result struct{ Created, Present int }

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

type teamSeed struct{ code, name, teamType, operatingUnit, operatingUnitType string }

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
	baseOperatingUnits = []operatingUnitSeed{
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
	hierarchyTypes = []store.HierarchyType{
		{Code: "REPORTING", Name: "Reporting hierarchy", Purposes: []string{"reporting"}},
		{Code: "PROCUREMENT", Name: "Procurement hierarchy", Purposes: []string{"procurement"}},
		{Code: "SECURITY", Name: "Security hierarchy", Purposes: []string{"security"}},
	}
	teamTypes = []store.TeamType{
		{Code: "STEWARDSHIP", Name: "Data stewardship", MemberKinds: []string{"user", "party"}},
		{Code: "PLANT", Name: "Plant crew", MemberKinds: []string{"user"}},
	}
	teams = []teamSeed{
		{"MD-STEWARDS", "Master data stewards", "STEWARDSHIP", "", ""},
		{"DET-LEADS", "Detroit shift leads", "PLANT", "DET-ASSY", "value_stream"},
		{"AUS-LEADS", "Austin shift leads", "PLANT", "AUS-MACH", "value_stream"},
	}
)

func ptr(s string) *string { return &s }

func demoOperatingUnits() []operatingUnitSeed {
	units := append([]operatingUnitSeed{}, baseOperatingUnits...)
	owners := []string{"USMF", "DEMF", "FRMF"}
	for i := 1; len(units) < operatingUnitSum; i++ {
		code := fmt.Sprintf("CC-%04d", 2000+i)
		units = append(units, operatingUnitSeed{code, "Cost Center " + code[3:], "cost_center", owners[i%len(owners)]})
	}
	return units
}

type runner struct {
	ctx context.Context
	st  store.Store
	res Result
}

func (r *runner) tally(existed bool) {
	if existed {
		r.res.Present++
	} else {
		r.res.Created++
	}
}

func Run(ctx context.Context, st store.Store, tenantID, name string) (Result, error) {
	if name != DemoManufacturer {
		return Result{}, fmt.Errorf("unknown seed package %q; known packages: %s", name, DemoManufacturer)
	}
	r := &runner{ctx: tenant.With(ctx, tenant.Principal{TenantID: tenantID}), st: st}
	err := r.run()
	return r.res, err
}

func (r *runner) run() error {
	entityIDs, err := r.legalEntities()
	if err != nil {
		return err
	}
	unitIDs, err := r.operatingUnits(entityIDs)
	if err != nil {
		return err
	}
	if err := r.sites(entityIDs); err != nil {
		return err
	}
	if err := r.hierarchyTypes(); err != nil {
		return err
	}
	return r.teams(unitIDs)
}

func (r *runner) legalEntities() (map[string]string, error) {
	page, err := r.st.ListLegalEntities(r.ctx, store.LegalEntityFilter{}, "", listAll)
	if err != nil {
		return nil, err
	}
	idsByCode := map[string]string{}
	for _, e := range page.Items {
		idsByCode[e.Code] = e.ID
	}
	for _, e := range legalEntities {
		_, existed := idsByCode[e.code]
		if !existed {
			created, err := r.st.CreateLegalEntity(r.ctx, store.LegalEntity{Code: e.code, Name: e.name, Country: e.country, Currency: e.currency, TimeZone: e.timeZone, DefaultLanguage: e.language}, keyPrefix+"le:"+e.code)
			if err != nil {
				return nil, fmt.Errorf("legal entity %s: %w", e.code, err)
			}
			idsByCode[e.code] = created.ID
		}
		r.tally(existed)
	}
	return idsByCode, nil
}

func (r *runner) operatingUnits(entityIDs map[string]string) (map[string]string, error) {
	page, err := r.st.ListOperatingUnits(r.ctx, store.OperatingUnitFilter{}, store.OperatingUnitKey{}, listAll)
	if err != nil {
		return nil, err
	}
	idsByTypeAndCode := map[string]string{}
	for _, u := range page.Items {
		idsByTypeAndCode[operatingUnitKey(u.Type, u.Code)] = u.ID
	}
	for _, u := range demoOperatingUnits() {
		key := operatingUnitKey(u.kind, u.code)
		_, existed := idsByTypeAndCode[key]
		if !existed {
			entity := entityIDs[u.legalEntity]
			created, err := r.st.CreateOperatingUnit(r.ctx, store.OperatingUnit{Code: u.code, Name: u.name, Type: u.kind, DefaultLegalEntityID: &entity}, keyPrefix+"ou:"+u.code)
			if err != nil {
				return nil, fmt.Errorf("operating unit %s: %w", u.code, err)
			}
			idsByTypeAndCode[key] = created.ID
		}
		r.tally(existed)
	}
	return idsByTypeAndCode, nil
}

func operatingUnitKey(kind, code string) string { return kind + "/" + code }

func (r *runner) sites(entityIDs map[string]string) error {
	page, err := r.st.ListSites(r.ctx, store.SiteFilter{}, store.SiteKey{}, listAll)
	if err != nil {
		return err
	}
	existing := map[string]store.Site{}
	for _, s := range page.Items {
		existing[s.LegalEntityID+"/"+s.Code] = s
	}
	warehousePage, err := r.st.ListWarehouses(r.ctx, store.WarehouseFilter{}, store.SiteKey{}, listAll)
	if err != nil {
		return err
	}
	warehouseIDs := map[string]string{}
	for _, w := range warehousePage.Items {
		warehouseIDs[w.LegalEntityID+"/"+w.Code] = w.ID
	}
	locationPage, err := r.st.ListLocations(r.ctx, store.LocationFilter{}, store.SiteKey{}, listAll)
	if err != nil {
		return err
	}
	locations := map[string]bool{}
	for _, l := range locationPage.Items {
		locations[l.WarehouseID+"/"+l.Code] = true
	}
	for _, s := range sites {
		legalEntityID := entityIDs[s.legalEntity]
		site, existed := existing[legalEntityID+"/"+s.code]
		newDefault := ""
		if !existed {
			first := s.warehouses[0]
			if site, err = r.st.CreateSite(r.ctx, store.Site{LegalEntityID: legalEntityID, Code: s.code, Name: s.name, TimeZone: s.timeZone,
				DefaultWarehouse: &store.Warehouse{Code: first.code, Name: first.name, Type: first.kind}}, keyPrefix+"site:"+s.code); err != nil {
				return fmt.Errorf("site %s: %w", s.code, err)
			}
			if site.DefaultWarehouseID != nil {
				newDefault = *site.DefaultWarehouseID
				warehouseIDs[site.LegalEntityID+"/"+first.code] = newDefault
			}
		}
		r.tally(existed)
		if err := r.warehouses(site, s, newDefault, warehouseIDs, locations); err != nil {
			return err
		}
		if err := r.siteAddress(site, s); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) warehouses(site store.Site, s siteSeed, newDefault string, warehouseIDs map[string]string, locations map[string]bool) error {
	for _, w := range s.warehouses {
		key := site.LegalEntityID + "/" + w.code
		id, existed := warehouseIDs[key]
		if !existed {
			created, err := r.st.CreateWarehouse(r.ctx, store.Warehouse{SiteID: site.ID, Code: w.code, Name: w.name, Type: w.kind}, keyPrefix+"wh:"+w.code)
			if err != nil {
				return fmt.Errorf("warehouse %s: %w", w.code, err)
			}
			id = created.ID
			warehouseIDs[key] = id
		}
		r.tally(existed && id != newDefault)
		for _, l := range w.locations {
			locationExists := locations[id+"/"+l.code]
			if !locationExists {
				if _, err := r.st.CreateLocation(r.ctx, store.Location{WarehouseID: id, Code: l.code, Name: l.name, Type: l.kind}, keyPrefix+"loc:"+w.code+":"+l.code); err != nil {
					return fmt.Errorf("location %s/%s: %w", w.code, l.code, err)
				}
			}
			r.tally(locationExists)
		}
	}
	return nil
}

func (r *runner) siteAddress(site store.Site, s siteSeed) error {
	current, err := r.st.GetSite(r.ctx, site.ID)
	if err != nil {
		return err
	}
	if current.AddressID != nil {
		r.tally(true)
		return nil
	}
	addresses, err := r.st.ListAddresses(r.ctx, store.AddressFilter{OwnerType: "site", OwnerID: site.ID, Purpose: "business"}, "", listAll)
	if err != nil {
		return err
	}
	if addressID := primaryBusinessAddressID(addresses.Items); addressID != "" {
		if _, err := r.st.UpdateSite(r.ctx, site.ID, current.Version, store.SitePatch{AddressID: &addressID}); err != nil {
			return fmt.Errorf("site %s address pointer: %w", s.code, err)
		}
		r.tally(true)
		return nil
	}
	address := s.address
	address.OwnerType, address.OwnerID, address.ValidFrom, address.GeoStatus = "site", site.ID, validFrom, "unverified"
	address.Purposes = []store.AddressPurpose{{Purpose: "business", Primary: true}}
	created, err := r.st.CreateAddress(r.ctx, address, keyPrefix+"addr:"+s.code)
	if err != nil {
		return fmt.Errorf("site %s address: %w", s.code, err)
	}
	if _, err := r.st.UpdateSite(r.ctx, site.ID, current.Version, store.SitePatch{AddressID: &created.ID}); err != nil {
		return fmt.Errorf("site %s address pointer: %w", s.code, err)
	}
	r.tally(false)
	return nil
}

func primaryBusinessAddressID(addresses []store.Address) string {
	for _, a := range addresses {
		for _, p := range a.Purposes {
			if p.Purpose == "business" && p.Primary {
				return a.ID
			}
		}
	}
	return ""
}

func (r *runner) hierarchyTypes() error {
	page, err := r.st.ListHierarchyTypes(r.ctx, store.HierarchyTypeFilter{}, "", listAll)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, t := range page.Items {
		existing[t.Code] = true
	}
	for _, t := range hierarchyTypes {
		if !existing[t.Code] {
			if _, err := r.st.CreateHierarchyType(r.ctx, t, keyPrefix+"hier-type:"+t.Code); err != nil {
				return fmt.Errorf("hierarchy type %s: %w", t.Code, err)
			}
		}
		r.tally(existing[t.Code])
	}
	return nil
}

func (r *runner) teams(unitIDs map[string]string) error {
	typePage, err := r.st.ListTeamTypes(r.ctx, store.TeamTypeFilter{}, "", listAll)
	if err != nil {
		return err
	}
	typeIDs := map[string]string{}
	for _, t := range typePage.Items {
		typeIDs[t.Code] = t.ID
	}
	for _, t := range teamTypes {
		_, existed := typeIDs[t.Code]
		if !existed {
			created, _, err := r.st.CreateTeamType(r.ctx, t, keyPrefix+"team-type:"+t.Code)
			if err != nil {
				return fmt.Errorf("team type %s: %w", t.Code, err)
			}
			typeIDs[t.Code] = created.ID
		}
		r.tally(existed)
	}
	teamPage, err := r.st.ListTeams(r.ctx, store.TeamFilter{}, "", listAll)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, t := range teamPage.Items {
		existing[t.Code] = true
	}
	for _, t := range teams {
		if !existing[t.code] {
			team := store.Team{Code: t.code, Name: t.name, TeamTypeID: typeIDs[t.teamType], EffectiveFrom: validFrom}
			if t.operatingUnit != "" {
				unit := unitIDs[operatingUnitKey(t.operatingUnitType, t.operatingUnit)]
				team.OperatingUnitID = &unit
			}
			if _, _, err := r.st.CreateTeam(r.ctx, team, keyPrefix+"team:"+t.code); err != nil {
				return fmt.Errorf("team %s: %w", t.code, err)
			}
		}
		r.tally(existing[t.code])
	}
	return nil
}
