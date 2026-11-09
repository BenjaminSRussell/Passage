package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	keyPrefix         = "orgpkg:"
	kindLegalEntity   = "legalEntities"
	kindOperatingUnit = "operatingUnits"
	kindSite          = "sites"
	kindWarehouse     = "warehouses"
	kindLocation      = "locations"
	kindHierarchyType = "hierarchyTypes"
	kindTeamType      = "teamTypes"
	kindTeam          = "teams"
	// kindCalendarAssignment counts the fiscal calendars the package assigns to legal entities and sites.
	kindCalendarAssignment = "fiscalCalendarAssignments"
	statusActive           = "active"
)

func strPtr(s string) *string { return &s }

// record is a row the tenant already has (or that this import created), keyed by natural code.
// parent is the site id of a warehouse; empty for every other kind.
type record struct {
	id, name, status, parent string
	// version and calendar (a fiscal calendar id) are read for legal entities and sites, which an
	// import patches once to assign a calendar.
	version  int
	calendar string
	// purposes and backdate describe a hierarchy type.
	purposes []string
	backdate bool
}

type importer struct {
	ctx      context.Context
	st       store.Store
	dryRun   bool
	report   ImportReport
	entities map[string]record // legal entity code
	units    map[string]record // type/code
	sites    map[string]record // entity id/code
	houses   map[string]record // entity id/code
	locs     map[string]record // warehouse id/code
	hier     map[string]record // code
	types    map[string]record // code
	teams    map[string]record // code
	siteCode map[string]string // site id -> code, for messages
	cals     map[string]store.FiscalCalendar
	calIDs   map[string]string // fiscal calendar code -> id, "dry:" + code for one a dry run would create
	// refUnits, refConvs, roundings, trans and currencies hold the tenant's units, conversions,
	// rounding rules (key currency/purpose/legal entity id), name translations and currencies.
	refUnits   map[string]store.Unit
	refConvs   map[string]store.Conversion
	roundings  map[string]store.CurrencyRounding
	trans      map[string]store.NameTranslation
	currencies map[string]store.ReferenceItem
	// today is the UTC date as YYYY-MM-DD; a hierarchy version dated before it is backdated.
	today    string
	versions map[string]versionState // hierarchy type code
	// backdated holds type code/effectiveFrom of each version published as a backdate (BR-6.3).
	backdated map[string]bool
}

func (im *importer) tally(kind string, existed bool) {
	c := im.report.Kinds[kind]
	if existed {
		c.Unchanged++
		im.report.Unchanged++
	} else {
		c.Created++
		im.report.Created++
	}
	im.report.Kinds[kind] = c
}

// unchanged counts an existing record and lists it when its name differs from the package.
func (im *importer) unchanged(kind, code string, existing record, wanted string) {
	if strings.TrimSpace(wanted) != existing.name {
		im.report.Differences = append(im.report.Differences, ImportDifference{Kind: kind, Code: code, Field: "name"})
	}
	im.tally(kind, true)
}

func importOrgPackage(ctx context.Context, st store.Store, pkg OrgPackage, dryRun bool, today string) (ImportReport, error) {
	im := &importer{ctx: ctx, st: st, dryRun: dryRun, today: today, report: ImportReport{DryRun: dryRun, Kinds: map[string]ImportCounts{}, Differences: []ImportDifference{}, Skipped: []ImportSkip{}}}
	if pkg.SchemaVersion != PackageSchemaVersion {
		return im.report, httpx.NewProblem(http.StatusUnprocessableEntity, codePackageVersion, "schemaVersion must be 1")
	}
	if err := packageWithinCap(pkg); err != nil {
		return im.report, err
	}
	problems := packageProblems{}
	validatePackage(pkg, problems, st.TreeLimits())
	if len(problems) > 0 {
		return im.report, invalidPackage(problems)
	}
	if err := im.load(); err != nil {
		return im.report, err
	}
	if err := im.resolve(pkg, problems); err != nil {
		return im.report, err
	}
	if len(problems) > 0 {
		return im.report, invalidPackage(problems)
	}
	steps := []func(OrgPackage) error{im.legalEntities, im.operatingUnits, im.sitesAndStock, im.hierarchyTypes, im.hierarchyVersions, im.fiscalCalendars, im.calendarAssignments, im.teamTypes, im.teamsOf, im.unitsOf, im.conversionsOf, im.roundingOf, im.translationsOf}
	for _, step := range steps {
		if err := step(pkg); err != nil {
			return im.report, err
		}
	}
	return im.report, nil
}

// packageWithinCap refuses a package holding more rows of one kind than export writes, before
// any database read.
func packageWithinCap(pkg OrgPackage) error {
	warehouses, locations := 0, 0
	for _, s := range pkg.Sites {
		warehouses += len(s.Warehouses)
		for _, w := range s.Warehouses {
			locations += len(w.Locations)
		}
	}
	for _, c := range []struct {
		kind string
		n    int
	}{{"legal entities", len(pkg.LegalEntities)}, {"operating units", len(pkg.OperatingUnits)}, {"sites", len(pkg.Sites)}, {"warehouses", warehouses},
		{"locations", locations}, {"hierarchy types", len(pkg.HierarchyTypes)}, {"fiscal calendars", len(pkg.FiscalCalendars)}, {"team types", len(pkg.TeamTypes)}, {"teams", len(pkg.Teams)},
		{"units", len(pkg.Units)}, {"conversions", len(pkg.Conversions)}, {"rounding rules", len(pkg.CurrencyRoundingRules)}, {"name translations", len(pkg.NameTranslations)}} {
		if c.n > packageRowCap {
			return tooLarge(c.kind)
		}
	}
	return nil
}

func invalidPackage(problems packageProblems) error {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, codePackageInvalid, "the package failed validation; nothing was imported")
	p.Errors = problems
	return p
}

// loadStock reads the sites, warehouses and locations visible under one legal-entity scope.
func (im *importer) loadStock(ctx context.Context) error {
	sites, err := im.st.ListSites(ctx, store.SiteFilter{}, store.SiteKey{}, packageRowCap)
	if err != nil {
		return err
	}
	houses, err := im.st.ListWarehouses(ctx, store.WarehouseFilter{}, store.SiteKey{}, packageRowCap)
	if err != nil {
		return err
	}
	locs, err := im.st.ListLocations(ctx, store.LocationFilter{}, store.SiteKey{}, packageRowCap)
	if err != nil {
		return err
	}
	switch {
	case sites.Next != nil:
		return tooLarge("sites")
	case houses.Next != nil:
		return tooLarge("warehouses")
	case locs.Next != nil:
		return tooLarge("locations")
	}
	for _, s := range sites.Items {
		im.sites[s.LegalEntityID+"/"+s.Code] = record{id: s.ID, name: s.Name, status: s.Status, version: s.Version, calendar: deref(s.FiscalCalendarID)}
		im.siteCode[s.ID] = s.Code
	}
	for _, w := range houses.Items {
		im.houses[w.LegalEntityID+"/"+w.Code] = record{id: w.ID, name: w.Name, status: w.Status, parent: w.SiteID}
	}
	for _, l := range locs.Items {
		im.locs[l.WarehouseID+"/"+l.Code] = record{id: l.ID, name: l.Name, status: l.Status}
	}
	return nil
}

// load reads every record of the tenant the package can match; a tenant past the row cap of a
// kind is refused, since a record beyond the cap would look missing and fail its create.
func (im *importer) load() error {
	im.entities, im.units, im.sites, im.houses, im.locs = map[string]record{}, map[string]record{}, map[string]record{}, map[string]record{}, map[string]record{}
	im.hier, im.types, im.teams, im.siteCode = map[string]record{}, map[string]record{}, map[string]record{}, map[string]string{}
	entities, err := im.st.ListLegalEntities(im.ctx, store.LegalEntityFilter{}, "", packageRowCap)
	if err != nil {
		return err
	}
	if entities.NextCode != "" {
		return tooLarge("legal entities")
	}
	for _, e := range entities.Items {
		im.entities[e.Code] = record{id: e.ID, name: e.Name, status: e.Status, version: e.Version, calendar: deref(e.FiscalCalendarID)}
	}
	units, err := im.st.ListOperatingUnits(im.ctx, store.OperatingUnitFilter{}, store.OperatingUnitKey{}, packageRowCap)
	if err != nil {
		return err
	}
	if units.Next != nil {
		return tooLarge("operating units")
	}
	for _, u := range units.Items {
		im.units[u.Type+"/"+u.Code] = record{id: u.ID, name: u.Name, status: u.Status}
	}
	for _, e := range im.entities {
		if err := im.loadStock(scopedTo(im.ctx, e.id)); err != nil {
			return err
		}
	}
	hier, err := im.st.ListHierarchyTypes(im.ctx, store.HierarchyTypeFilter{}, "", packageRowCap)
	if err != nil {
		return err
	}
	types, err := im.st.ListTeamTypes(im.ctx, store.TeamTypeFilter{}, "", packageRowCap)
	if err != nil {
		return err
	}
	teams, err := im.st.ListTeams(im.ctx, store.TeamFilter{}, "", packageRowCap)
	if err != nil {
		return err
	}
	if hier.NextCode != "" || types.NextCode != "" || teams.NextCode != "" {
		return tooLarge("hierarchy types, team types or teams")
	}
	for _, t := range hier.Items {
		im.hier[t.Code] = record{id: t.ID, name: t.Name, status: t.Status, purposes: t.Purposes, backdate: t.BackdateAllowed}
	}
	for _, t := range types.Items {
		im.types[t.Code] = record{id: t.ID, name: t.Name}
	}
	for _, t := range teams.Items {
		im.teams[t.Code] = record{id: t.ID, name: t.Name, status: t.Status}
	}
	return im.loadCalendars()
}

// resolve checks every code reference against the package and the tenant's rows, and every rule
// the store applies when it creates a record (active parents, active reference data, tenant
// limits, warehouse codes unique per legal entity), so a valid package is refused before the
// first write rather than part-way. A change committed by someone else after this read can still
// fail a create; the import then stops and a second run resumes.
func (im *importer) resolve(pkg OrgPackage, p packageProblems) error {
	// active reports whether a referenced record exists (in the tenant or the package) and is
	// active; a record the package creates is active.
	inPackage := map[string]bool{}
	newEntities := 0
	for _, e := range pkg.LegalEntities {
		code := strings.ToUpper(e.Code)
		inPackage[code] = true
		if _, ok := im.entities[code]; !ok {
			newEntities++
		}
	}
	entity := func(code string) (known, active bool) {
		code = strings.ToUpper(code)
		if e, ok := im.entities[code]; ok {
			return true, e.status == statusActive
		}
		return inPackage[code], inPackage[code]
	}
	if newEntities > 0 {
		if len(im.entities)+newEntities > store.MaxLegalEntitiesPerTenant {
			p.add(kindLegalEntity, fmt.Sprintf("a tenant can have at most %d legal entities", store.MaxLegalEntitiesPerTenant))
		}
		if err := im.resolveReferenceData(pkg, p); err != nil {
			return err
		}
	}
	for i, u := range pkg.OperatingUnits {
		if u.LegalEntity == "" {
			continue
		}
		_, exists := im.units[u.Type+"/"+strings.ToUpper(u.Code)]
		if known, active := entity(u.LegalEntity); !known {
			p.add(indexPath(kindOperatingUnit, i)+".legalEntity", "unknown legal entity "+u.LegalEntity)
		} else if !active && !exists {
			p.add(indexPath(kindOperatingUnit, i)+".legalEntity", "legal entity "+u.LegalEntity+" is inactive")
		}
	}
	for i, s := range pkg.Sites {
		known, active := entity(s.LegalEntity)
		if !known {
			p.add(indexPath(kindSite, i)+".legalEntity", "unknown legal entity "+s.LegalEntity)
			continue
		}
		im.resolveSite(indexPath(kindSite, i), s, active, p)
	}
	im.resolveTeams(pkg, p)
	im.resolveCalendars(pkg, p)
	im.resolveAssignments(pkg, p)
	if err := im.resolveVersions(pkg, p); err != nil {
		return err
	}
	return im.resolveReferenceKinds(pkg, p)
}

// resolveReferenceData checks that each legal entity the import creates names a country,
// currency and language active for this tenant.
func (im *importer) resolveReferenceData(pkg OrgPackage, p packageProblems) error {
	active := map[store.ReferenceKind]map[string]bool{}
	for _, kind := range []store.ReferenceKind{store.KindCountry, store.KindCurrency, store.KindLanguage} {
		items, err := im.st.ListReference(im.ctx, kind, store.ReferenceFilter{Status: statusActive})
		if err != nil {
			return err
		}
		active[kind] = map[string]bool{}
		for _, item := range items {
			active[kind][item.Code] = true
		}
	}
	for i, e := range pkg.LegalEntities {
		if _, ok := im.entities[strings.ToUpper(e.Code)]; ok {
			continue
		}
		language := e.DefaultLanguage
		if language == "" {
			language = store.DefaultLanguage
		}
		primary, _, _ := strings.Cut(language, "-")
		for _, c := range []struct {
			kind        store.ReferenceKind
			code, field string
		}{{store.KindCountry, e.Country, "country"}, {store.KindCurrency, e.Currency, "currency"}, {store.KindLanguage, primary, "defaultLanguage"}} {
			if !active[c.kind][c.code] {
				p.add(indexPath(kindLegalEntity, i)+"."+c.field, fmt.Sprintf("%s %s is unknown or inactive in this tenant", c.kind, c.code))
			}
		}
	}
	return nil
}

func (im *importer) resolveSite(path string, s packageSite, entityActive bool, p packageProblems) {
	entityCode := strings.ToUpper(s.LegalEntity)
	entityID := im.entities[entityCode].id // empty for a legal entity the package creates
	site, siteExists := im.sites[entityID+"/"+strings.ToUpper(s.Code)]
	if entityID == "" {
		siteExists = false
	}
	if !siteExists && !entityActive {
		p.add(path+".legalEntity", "legal entity "+s.LegalEntity+" is inactive; a site cannot be created in it")
	}
	for j, w := range s.Warehouses {
		wpath := fmt.Sprintf("%s.warehouses[%d]", path, j)
		house, houseExists := im.houses[entityID+"/"+strings.ToUpper(w.Code)]
		houseExists = houseExists && entityID != ""
		switch {
		case houseExists && (!siteExists || house.parent != site.id):
			p.add(wpath+".code", fmt.Sprintf("warehouse %s already exists at site %s of legal entity %s", w.Code, im.siteCode[house.parent], entityCode))
			continue
		case !houseExists && siteExists && site.status != statusActive:
			p.add(wpath+".code", "site "+s.Code+" is inactive; a warehouse cannot be created at it")
		}
		if !houseExists || house.status == statusActive {
			continue
		}
		for k, l := range w.Locations {
			if _, ok := im.locs[house.id+"/"+strings.ToUpper(l.Code)]; !ok {
				p.add(fmt.Sprintf("%s.locations[%d].code", wpath, k), "warehouse "+w.Code+" is inactive; a location cannot be created in it")
			}
		}
	}
}

func (im *importer) resolveTeams(pkg OrgPackage, p packageProblems) {
	teamType := map[string]bool{}
	for code := range im.types {
		teamType[code] = true
	}
	for _, t := range pkg.TeamTypes {
		teamType[strings.ToUpper(t.Code)] = true
	}
	unit := map[string]bool{}
	for _, u := range pkg.OperatingUnits {
		unit[u.Type+"/"+strings.ToUpper(u.Code)] = true
	}
	newTeams := 0
	for i, t := range pkg.Teams {
		_, exists := im.teams[strings.ToUpper(t.Code)]
		if !exists {
			newTeams++
		}
		if !teamType[strings.ToUpper(t.TeamType)] {
			p.add(indexPath(kindTeam, i)+".teamType", "unknown team type "+t.TeamType)
		}
		if t.OperatingUnit == nil {
			continue
		}
		key := t.OperatingUnit.Type + "/" + strings.ToUpper(t.OperatingUnit.Code)
		existing, inTenant := im.units[key]
		switch {
		case !inTenant && !unit[key]:
			p.add(indexPath(kindTeam, i)+".operatingUnit", "unknown operating unit "+t.OperatingUnit.Type+"/"+t.OperatingUnit.Code)
		case inTenant && existing.status != statusActive && !exists:
			p.add(indexPath(kindTeam, i)+".operatingUnit", "operating unit "+t.OperatingUnit.Type+"/"+t.OperatingUnit.Code+" is inactive")
		}
	}
	if newTeams > 0 && len(im.teams)+newTeams > store.MaxTeamsPerTenant {
		p.add(kindTeam, fmt.Sprintf("a tenant can have at most %d teams", store.MaxTeamsPerTenant))
	}
}

func indexPath(name string, i int) string { return name + "[" + strconv.Itoa(i) + "]" }

func (im *importer) legalEntities(pkg OrgPackage) error {
	for _, e := range pkg.LegalEntities {
		code := strings.ToUpper(e.Code)
		if existing, ok := im.entities[code]; ok {
			im.unchanged(kindLegalEntity, code, existing, e.Name)
			continue
		}
		language := e.DefaultLanguage
		if language == "" {
			language = store.DefaultLanguage
		}
		id, version := "dry:"+code, 0
		if !im.dryRun {
			created, err := im.st.CreateLegalEntity(im.ctx, store.LegalEntity{Code: code, Name: strings.TrimSpace(e.Name), Country: e.Country, Currency: e.Currency,
				TimeZone: e.TimeZone, DefaultLanguage: language, RegistrationNumber: e.RegistrationNumber}, keyPrefix+"le:"+code)
			if err != nil {
				return err
			}
			id, version = created.ID, created.Version
		}
		im.entities[code] = record{id: id, status: statusActive, version: version}
		im.tally(kindLegalEntity, false)
	}
	return nil
}

func (im *importer) operatingUnits(pkg OrgPackage) error {
	for _, u := range pkg.OperatingUnits {
		code := strings.ToUpper(u.Code)
		key := u.Type + "/" + code
		if existing, ok := im.units[key]; ok {
			im.unchanged(kindOperatingUnit, key, existing, u.Name)
			continue
		}
		id := "dry:" + key
		if !im.dryRun {
			in := store.OperatingUnit{Code: code, Name: strings.TrimSpace(u.Name), Type: u.Type}
			if u.LegalEntity != "" {
				entityID := im.entities[strings.ToUpper(u.LegalEntity)].id
				in.DefaultLegalEntityID = &entityID
			}
			created, err := im.st.CreateOperatingUnit(im.ctx, in, keyPrefix+"ou:"+key)
			if err != nil {
				return err
			}
			id = created.ID
		}
		im.units[key] = record{id: id, status: statusActive}
		im.tally(kindOperatingUnit, false)
	}
	return nil
}

func (im *importer) sitesAndStock(pkg OrgPackage) error {
	for _, s := range pkg.Sites {
		entityCode := strings.ToUpper(s.LegalEntity)
		entityID := im.entities[entityCode].id
		code := strings.ToUpper(s.Code)
		siteKey := entityID + "/" + code
		site, existed := im.sites[siteKey]
		if existed {
			im.unchanged(kindSite, entityCode+"/"+code, site, s.Name)
		} else {
			created, err := im.createSite(s, entityID, entityCode, code)
			if err != nil {
				return err
			}
			site = record{id: created.ID, status: statusActive, version: created.Version}
			im.sites[siteKey] = site
			im.tally(kindSite, false)
		}
		if err := im.warehouses(s, site.id, entityID, entityCode, !existed); err != nil {
			return err
		}
	}
	return nil
}

// createSite creates the site with the package's first warehouse as its default, records that
// warehouse, and returns the site.
func (im *importer) createSite(s packageSite, entityID, entityCode, code string) (store.Site, error) {
	first := s.Warehouses[0]
	firstCode := strings.ToUpper(first.Code)
	created := store.Site{ID: "dry:" + entityCode + "/" + code}
	siteID, houseID := created.ID, "dry:"+entityCode+"/"+firstCode
	if !im.dryRun {
		site, err := im.st.CreateSite(scopedTo(im.ctx, entityID), store.Site{LegalEntityID: entityID, Code: code, Name: strings.TrimSpace(s.Name), TimeZone: s.TimeZone,
			DefaultWarehouse: &store.Warehouse{Code: firstCode, Name: strings.TrimSpace(first.Name), Type: first.Type, TimeZone: first.TimeZone}}, keyPrefix+"site:"+entityCode+"/"+code)
		if err != nil {
			return created, err
		}
		if site.DefaultWarehouseID == nil {
			return created, fmt.Errorf("site %s/%s was created without its default warehouse", entityCode, code)
		}
		created, siteID, houseID = site, site.ID, *site.DefaultWarehouseID
	}
	im.houses[entityID+"/"+firstCode] = record{id: houseID, status: statusActive, parent: siteID}
	return created, nil
}

func (im *importer) warehouses(s packageSite, siteID, entityID, entityCode string, siteCreated bool) error {
	for i, w := range s.Warehouses {
		code := strings.ToUpper(w.Code)
		key := entityID + "/" + code
		house, existed := im.houses[key]
		switch {
		case siteCreated && i == 0:
			im.tally(kindWarehouse, false) // created with the site
		case existed:
			im.unchanged(kindWarehouse, entityCode+"/"+code, house, w.Name)
		default:
			house = record{id: "dry:" + entityCode + "/" + code, status: statusActive, parent: siteID}
			if !im.dryRun {
				created, err := im.st.CreateWarehouse(scopedTo(im.ctx, entityID), store.Warehouse{SiteID: siteID, Code: code, Name: strings.TrimSpace(w.Name), Type: w.Type, TimeZone: w.TimeZone},
					keyPrefix+"wh:"+entityCode+"/"+code)
				if err != nil {
					return err
				}
				house.id = created.ID
			}
			im.houses[key] = house
			im.tally(kindWarehouse, false)
		}
		for _, l := range w.Locations {
			lcode := strings.ToUpper(l.Code)
			lkey := house.id + "/" + lcode
			if loc, ok := im.locs[lkey]; ok {
				im.unchanged(kindLocation, entityCode+"/"+code+"/"+lcode, loc, l.Name)
				continue
			}
			if !im.dryRun {
				if _, err := im.st.CreateLocation(scopedTo(im.ctx, entityID), store.Location{WarehouseID: house.id, Code: lcode, Name: strings.TrimSpace(l.Name), Type: l.Type},
					keyPrefix+"loc:"+entityCode+"/"+code+"/"+lcode); err != nil {
					return err
				}
			}
			im.locs[lkey] = record{status: statusActive}
			im.tally(kindLocation, false)
		}
	}
	return nil
}

func (im *importer) hierarchyTypes(pkg OrgPackage) error {
	for _, t := range pkg.HierarchyTypes {
		code := strings.ToUpper(t.Code)
		if existing, ok := im.hier[code]; ok {
			im.unchanged(kindHierarchyType, code, existing, t.Name)
			continue
		}
		id := "dry:" + code
		if !im.dryRun {
			created, err := im.st.CreateHierarchyType(im.ctx, store.HierarchyType{Code: code, Name: strings.TrimSpace(t.Name), Purposes: t.Purposes, BackdateAllowed: t.BackdateAllowed},
				keyPrefix+"hier-type:"+code)
			if err != nil {
				return err
			}
			id = created.ID
		}
		im.hier[code] = record{id: id, status: statusActive, purposes: t.Purposes, backdate: t.BackdateAllowed}
		im.tally(kindHierarchyType, false)
	}
	return nil
}

func (im *importer) teamTypes(pkg OrgPackage) error {
	for _, t := range pkg.TeamTypes {
		code := strings.ToUpper(t.Code)
		if existing, ok := im.types[code]; ok {
			im.unchanged(kindTeamType, code, existing, t.Name)
			continue
		}
		id := "dry:" + code
		if !im.dryRun {
			created, _, err := im.st.CreateTeamType(im.ctx, store.TeamType{Code: code, Name: strings.TrimSpace(t.Name), MemberKinds: t.MemberKinds}, keyPrefix+"team-type:"+code)
			if err != nil {
				return err
			}
			id = created.ID
		}
		im.types[code] = record{id: id}
		im.tally(kindTeamType, false)
	}
	return nil
}

func (im *importer) teamsOf(pkg OrgPackage) error {
	for _, t := range pkg.Teams {
		code := strings.ToUpper(t.Code)
		if existing, ok := im.teams[code]; ok {
			im.unchanged(kindTeam, code, existing, t.Name)
			continue
		}
		if !im.dryRun {
			in := store.Team{Code: code, Name: strings.TrimSpace(t.Name), Description: t.Description, TeamTypeID: im.types[strings.ToUpper(t.TeamType)].id,
				EffectiveFrom: t.EffectiveFrom, EffectiveTo: t.EffectiveTo}
			if t.OperatingUnit != nil {
				unitID := im.units[t.OperatingUnit.Type+"/"+strings.ToUpper(t.OperatingUnit.Code)].id
				in.OperatingUnitID = &unitID
			}
			if _, _, err := im.st.CreateTeam(im.ctx, in, keyPrefix+"team:"+code); err != nil {
				return err
			}
		}
		im.teams[code] = record{status: statusActive}
		im.tally(kindTeam, false)
	}
	return nil
}

// calendarAssignments assigns the fiscal calendar a package names for a legal entity or a site.
// It runs after the calendars and their years exist, because the service refuses a calendar
// without a generated year. A record that already has another calendar is reported under
// differences and left alone; one with none (a record this import just created, or one an
// earlier run stopped before assigning) gets the package's calendar, so a re-run finishes.
// The import duty (org.package.manage, tenant administrator without a legal-entity claim) is
// intended to cover filling an unassigned calendar on an existing record; both writes run scoped
// to the record's own legal entity.
func (im *importer) calendarAssignments(pkg OrgPackage) error {
	for _, e := range pkg.LegalEntities {
		if e.FiscalCalendar == "" {
			continue
		}
		code := strings.ToUpper(e.Code)
		rec := im.entities[code]
		err := im.assign(kindLegalEntity, code, strings.ToUpper(e.FiscalCalendar), rec.calendar, func(calendarID string) error {
			updated, err := im.st.UpdateLegalEntity(scopedTo(im.ctx, rec.id), rec.id, rec.version, store.LegalEntityPatch{FiscalCalendarID: &calendarID})
			if err == nil {
				rec.version, rec.calendar = updated.Version, calendarID
				im.entities[code] = rec
			}
			return err
		})
		if err != nil {
			return err
		}
	}
	for _, s := range pkg.Sites {
		if s.FiscalCalendar == "" {
			continue
		}
		entityCode, code := strings.ToUpper(s.LegalEntity), strings.ToUpper(s.Code)
		entityID := im.entities[entityCode].id
		rec := im.sites[entityID+"/"+code]
		err := im.assign(kindSite, entityCode+"/"+code, strings.ToUpper(s.FiscalCalendar), rec.calendar, func(calendarID string) error {
			updated, err := im.st.UpdateSite(scopedTo(im.ctx, entityID), rec.id, rec.version, store.SitePatch{FiscalCalendarID: &calendarID})
			if err == nil {
				rec.version, rec.calendar = updated.Version, calendarID
				im.sites[entityID+"/"+code] = rec
			}
			return err
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (im *importer) assign(kind, key, calendarCode, current string, write func(calendarID string) error) error {
	calendarID := im.calIDs[calendarCode]
	switch {
	case current == calendarID:
		im.tally(kindCalendarAssignment, true)
	case current != "":
		im.report.Differences = append(im.report.Differences, ImportDifference{Kind: kind, Code: key, Field: "fiscalCalendar"})
		im.tally(kindCalendarAssignment, true)
	default:
		if !im.dryRun {
			if err := write(calendarID); err != nil {
				return fmt.Errorf("assign fiscal calendar %s to %s %s: %w", calendarCode, kind, key, err)
			}
		}
		im.tally(kindCalendarAssignment, false)
	}
	return nil
}

// resolveAssignments checks that each calendar a legal entity or site names is one the tenant
// can assign: an active calendar with a generated year, in the tenant or generated by the
// package. A record that already holds another calendar is only reported, so it is not checked.
func (im *importer) resolveAssignments(pkg OrgPackage, p packageProblems) {
	fromPackage := map[string]bool{}
	for _, c := range pkg.FiscalCalendars {
		if len(c.Years) > 0 {
			fromPackage[strings.ToUpper(c.Code)] = true
		}
	}
	usable := func(code string) bool {
		if c, ok := im.cals[code]; ok {
			return c.Status == statusActive && (len(c.GeneratedYears) > 0 || fromPackage[code])
		}
		return fromPackage[code]
	}
	check := func(path, code, current string) {
		code = strings.ToUpper(code)
		if current == "" && !usable(code) {
			p.add(path+".fiscalCalendar", "fiscal calendar "+code+" is unknown, inactive or has no generated year in the tenant or the package")
		}
	}
	for i, e := range pkg.LegalEntities {
		if e.FiscalCalendar != "" {
			check(indexPath(kindLegalEntity, i), e.FiscalCalendar, im.entities[strings.ToUpper(e.Code)].calendar)
		}
	}
	for i, s := range pkg.Sites {
		if s.FiscalCalendar == "" {
			continue
		}
		entityID := im.entities[strings.ToUpper(s.LegalEntity)].id
		check(indexPath(kindSite, i), s.FiscalCalendar, im.sites[entityID+"/"+strings.ToUpper(s.Code)].calendar)
	}
}
