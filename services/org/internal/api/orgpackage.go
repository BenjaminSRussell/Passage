package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	dutyPackage = "org.package.manage"

	// PackageSchemaVersion is the only package schema this service reads and writes.
	PackageSchemaVersion = 1

	// packageMaxBytes bounds the import body; the limit is enforced while the JSON is read, so
	// a larger body is refused with 413 before it is decoded.
	packageMaxBytes = 8 << 20

	packageExport       = "/v1/org/export"
	packageImport       = "/v1/org/import"
	packageRowCap       = 10000
	packageMaxProblems  = 50
	codePackageVersion  = "ORG-PKG-VERSION-UNSUPPORTED"
	codePackageInvalid  = "ORG-PKG-INVALID"
	codePackageTooLarge = "ORG-PKG-TOO-LARGE"
)

// OrgPackage is the versioned JSON package of the organization model (FR-6.12).
// Records name each other by code, never by id, so a package moves between tenants.
type OrgPackage struct {
	SchemaVersion   int                     `json:"schemaVersion"`
	LegalEntities   []packageLegalEntity    `json:"legalEntities"`
	OperatingUnits  []packageOperatingUnit  `json:"operatingUnits"`
	Sites           []packageSite           `json:"sites"`
	HierarchyTypes  []packageHierarchyType  `json:"hierarchyTypes"`
	FiscalCalendars []packageFiscalCalendar `json:"fiscalCalendars"`
	TeamTypes       []packageTeamType       `json:"teamTypes"`
	Teams           []packageTeam           `json:"teams"`
	// The four kinds below are optional: a package written before they existed has none.
	Units                 []packageUnit        `json:"units"`
	Conversions           []packageConversion  `json:"conversions"`
	CurrencyRoundingRules []packageRounding    `json:"currencyRoundingRules"`
	NameTranslations      []packageTranslation `json:"nameTranslations"`
	// ExportedAt is written by export and ignored by import, so an exported file imports as is.
	ExportedAt string `json:"exportedAt,omitempty"`
}

type packageLegalEntity struct {
	Code               string  `json:"code"`
	Name               string  `json:"name"`
	Country            string  `json:"country"`
	Currency           string  `json:"currency"`
	TimeZone           string  `json:"timeZone"`
	DefaultLanguage    string  `json:"defaultLanguage"`
	RegistrationNumber *string `json:"registrationNumber,omitempty"`
	// FiscalCalendar is the code of the assigned fiscal calendar, empty for none.
	FiscalCalendar string `json:"fiscalCalendar,omitempty"`
}

type packageOperatingUnit struct {
	Type string `json:"type"`
	Code string `json:"code"`
	Name string `json:"name"`
	// LegalEntity is the code of the default legal entity, empty for none.
	LegalEntity string `json:"legalEntity,omitempty"`
}

type packageSite struct {
	LegalEntity string `json:"legalEntity"`
	Code        string `json:"code"`
	Name        string `json:"name"`
	TimeZone    string `json:"timeZone,omitempty"`
	// FiscalCalendar is the code of the site's own fiscal calendar, empty when it follows its legal entity.
	FiscalCalendar string             `json:"fiscalCalendar,omitempty"`
	Warehouses     []packageWarehouse `json:"warehouses"`
}

type packageWarehouse struct {
	Code      string            `json:"code"`
	Name      string            `json:"name"`
	Type      string            `json:"type,omitempty"`
	TimeZone  string            `json:"timeZone,omitempty"`
	Locations []packageLocation `json:"locations"`
}

type packageLocation struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Type string `json:"type,omitempty"`
}

type packageHierarchyType struct {
	Code            string   `json:"code"`
	Name            string   `json:"name"`
	Purposes        []string `json:"purposes"`
	BackdateAllowed bool     `json:"backdateAllowed"`
	// Versions are the published versions, oldest first; a draft is not exported.
	Versions []packageHierarchyVersion `json:"versions,omitempty"`
}

type packageTeamType struct {
	Code        string   `json:"code"`
	Name        string   `json:"name"`
	MemberKinds []string `json:"memberKinds"`
}

type packageUnitRef struct {
	Type string `json:"type"`
	Code string `json:"code"`
}

type packageTeam struct {
	Code          string          `json:"code"`
	Name          string          `json:"name"`
	Description   *string         `json:"description,omitempty"`
	TeamType      string          `json:"teamType"`
	OperatingUnit *packageUnitRef `json:"operatingUnit,omitempty"`
	EffectiveFrom string          `json:"effectiveFrom"`
	EffectiveTo   *string         `json:"effectiveTo,omitempty"`
}

// ImportCounts is the per-kind tally of an import.
type ImportCounts struct {
	Created   int `json:"created"`
	Unchanged int `json:"unchanged"`
}

// ImportDifference names an existing record whose attribute differs from the package;
// import never updates existing records.
type ImportDifference struct {
	Kind  string `json:"kind"`
	Code  string `json:"code"`
	Field string `json:"field"`
}

// ImportSkip names records the import left alone on purpose, with the reason.
type ImportSkip struct {
	Kind   string `json:"kind"`
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// ImportReport is the answer of POST /v1/org/import.
type ImportReport struct {
	DryRun      bool                    `json:"dryRun"`
	Created     int                     `json:"created"`
	Unchanged   int                     `json:"unchanged"`
	Kinds       map[string]ImportCounts `json:"kinds"`
	Differences []ImportDifference      `json:"differences"`
	Skipped     []ImportSkip            `json:"skipped"`
}

func (a *API) registerPackage(r chi.Router) {
	r.With(authz.RequireDuty(dutyPackage)).Post(packageExport, a.exportPackage)
	r.With(authz.RequireDuty(dutyPackage)).Post(packageImport, a.importPackage)
}

// packageCaller admits only a tenant administrator without a legal-entity claim: the package
// spans every legal entity.
func packageCaller(w http.ResponseWriter, r *http.Request, action string) bool {
	return validTenant(w, r) && !rejectScopedCaller(w, r, action)
}

// scopedTo returns ctx narrowed to one legal entity, the scope that site, warehouse and
// location rows are visible and writable under.
func scopedTo(ctx context.Context, legalEntityID string) context.Context {
	p, _ := tenant.FromContext(ctx)
	p.LegalEntityID = legalEntityID
	return tenant.With(ctx, p)
}

func (a *API) exportPackage(w http.ResponseWriter, r *http.Request) {
	if !packageCaller(w, r, "export the organization model") {
		return
	}
	pkg, err := exportOrgPackage(r.Context(), a.store)
	if err != nil {
		slog.Error("export organization package failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.packageExported.Add(1)
	w.Header().Set("Content-Disposition", `attachment; filename="org-package.json"`)
	pkg.ExportedAt = a.now().UTC().Format("2006-01-02T15:04:05Z")
	httpx.JSON(w, http.StatusOK, pkg)
}

func (a *API) importPackage(w http.ResponseWriter, r *http.Request) {
	if !packageCaller(w, r, "import the organization model") {
		return
	}
	dryRun := r.URL.Query().Get("dryRun")
	if dryRun != "" && dryRun != "true" && dryRun != "false" {
		badQuery(w, r, "dryRun", "must be true or false")
		return
	}
	var pkg OrgPackage
	if err := httpx.DecodeLimit(r, &pkg, packageMaxBytes); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	report, err := importOrgPackage(r.Context(), a.store, pkg, dryRun == "true", a.now().UTC().Format(dateLayout))
	if err != nil {
		slog.Error("import organization package failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if !report.DryRun {
		a.packageImported.Add(1)
		a.packageRecordsCreated.Add(int64(report.Created))
	}
	httpx.JSON(w, http.StatusOK, report)
}

func tooLarge(kind string) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, codePackageTooLarge, fmt.Sprintf("more than %d %s; export them in parts", packageRowCap, kind))
}

func exportOrgPackage(ctx context.Context, st store.Store) (OrgPackage, error) {
	pkg := OrgPackage{SchemaVersion: PackageSchemaVersion, LegalEntities: []packageLegalEntity{}, OperatingUnits: []packageOperatingUnit{},
		Sites: []packageSite{}, HierarchyTypes: []packageHierarchyType{}, FiscalCalendars: []packageFiscalCalendar{}, TeamTypes: []packageTeamType{}, Teams: []packageTeam{}}
	calendarCodes, err := exportCalendarCodes(ctx, st)
	if err != nil {
		return pkg, err
	}
	entities, err := st.ListLegalEntities(ctx, store.LegalEntityFilter{}, "", packageRowCap)
	if err != nil {
		return pkg, err
	}
	if entities.NextCode != "" {
		return pkg, tooLarge("legal entities")
	}
	entityCodes := map[string]string{}
	nodeRefs := map[string]packageNodeRef{}
	refs := map[string]string{} // ownerType/id -> natural key, for translations and rounding rules
	for _, e := range entities.Items {
		entityCodes[e.ID] = e.Code
		refs[ownerLegalEntity+"/"+e.ID] = e.Code
		nodeRefs[e.ID] = packageNodeRef{Kind: nodeKindLegalEntity, Code: e.Code}
		pkg.LegalEntities = append(pkg.LegalEntities, packageLegalEntity{Code: e.Code, Name: e.Name, Country: e.Country, Currency: e.Currency,
			TimeZone: e.TimeZone, DefaultLanguage: e.DefaultLanguage, RegistrationNumber: e.RegistrationNumber, FiscalCalendar: calendarCodes[deref(e.FiscalCalendarID)]})
	}
	units, err := st.ListOperatingUnits(ctx, store.OperatingUnitFilter{}, store.OperatingUnitKey{}, packageRowCap)
	if err != nil {
		return pkg, err
	}
	if units.Next != nil {
		return pkg, tooLarge("operating units")
	}
	unitRefs := map[string]packageUnitRef{}
	for _, u := range units.Items {
		unitRefs[u.ID] = packageUnitRef{Type: u.Type, Code: u.Code}
		refs[ownerOperatingUnit+"/"+u.ID] = u.Type + "/" + u.Code
		nodeRefs[u.ID] = packageNodeRef{Kind: nodeKindOperatingUnit, Type: u.Type, Code: u.Code}
		out := packageOperatingUnit{Type: u.Type, Code: u.Code, Name: u.Name}
		if u.DefaultLegalEntityID != nil {
			out.LegalEntity = entityCodes[*u.DefaultLegalEntityID]
		}
		pkg.OperatingUnits = append(pkg.OperatingUnits, out)
	}
	if err := exportSites(ctx, st, entityCodes, calendarCodes, refs, &pkg); err != nil {
		return pkg, err
	}
	types, err := st.ListHierarchyTypes(ctx, store.HierarchyTypeFilter{}, "", packageRowCap)
	if err != nil {
		return pkg, err
	}
	if types.NextCode != "" {
		return pkg, tooLarge("hierarchy types")
	}
	for _, t := range types.Items {
		out := packageHierarchyType{Code: t.Code, Name: t.Name, Purposes: t.Purposes, BackdateAllowed: t.BackdateAllowed}
		if out.Versions, err = exportVersions(ctx, st, t.ID, nodeRefs); err != nil {
			return pkg, err
		}
		pkg.HierarchyTypes = append(pkg.HierarchyTypes, out)
	}
	if err := exportFiscalCalendars(ctx, st, &pkg); err != nil {
		return pkg, err
	}
	if err := exportTeams(ctx, st, unitRefs, &pkg); err != nil {
		return pkg, err
	}
	return pkg, exportReferenceData(ctx, st, entityCodes, refs, &pkg)
}

// exportCalendarCodes maps each fiscal calendar id to its code, for the assignments of legal
// entities and sites.
func exportCalendarCodes(ctx context.Context, st store.Store) (map[string]string, error) {
	page, err := st.ListFiscalCalendars(ctx, store.FiscalCalendarFilter{}, "", packageRowCap)
	if err != nil {
		return nil, err
	}
	if page.NextCode != "" {
		return nil, tooLarge("fiscal calendars")
	}
	codes := map[string]string{}
	for _, c := range page.Items {
		codes[c.ID] = c.Code
	}
	return codes, nil
}

func exportSites(ctx context.Context, st store.Store, entityCodes, calendarCodes, refs map[string]string, pkg *OrgPackage) error {
	ids := make([]string, 0, len(entityCodes))
	for id := range entityCodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return entityCodes[ids[i]] < entityCodes[ids[j]] })
	for _, entityID := range ids {
		scoped := scopedTo(ctx, entityID)
		sites, err := st.ListSites(scoped, store.SiteFilter{}, store.SiteKey{}, packageRowCap)
		if err != nil {
			return err
		}
		warehouses, err := st.ListWarehouses(scoped, store.WarehouseFilter{}, store.SiteKey{}, packageRowCap)
		if err != nil {
			return err
		}
		locations, err := st.ListLocations(scoped, store.LocationFilter{}, store.SiteKey{}, packageRowCap)
		if err != nil {
			return err
		}
		switch {
		case sites.Next != nil:
			return tooLarge("sites")
		case warehouses.Next != nil:
			return tooLarge("warehouses")
		case locations.Next != nil:
			return tooLarge("locations")
		}
		for _, s := range sites.Items {
			refs[ownerSite+"/"+s.ID] = entityCodes[entityID] + "/" + s.Code
		}
		for _, w := range warehouses.Items {
			refs[ownerWarehouse+"/"+w.ID] = entityCodes[entityID] + "/" + w.Code
		}
		appendSites(entityCodes[entityID], calendarCodes, sites.Items, warehouses.Items, locations.Items, pkg)
	}
	return nil
}

func appendSites(entityCode string, calendarCodes map[string]string, sites []store.Site, warehouses []store.Warehouse, locations []store.Location, pkg *OrgPackage) {
	locationsByWarehouse := map[string][]packageLocation{}
	for _, l := range locations {
		locationsByWarehouse[l.WarehouseID] = append(locationsByWarehouse[l.WarehouseID], packageLocation{Code: l.Code, Name: l.Name, Type: l.Type})
	}
	warehousesBySite := map[string][]store.Warehouse{}
	for _, w := range warehouses {
		warehousesBySite[w.SiteID] = append(warehousesBySite[w.SiteID], w)
	}
	for _, s := range sites {
		out := packageSite{LegalEntity: entityCode, Code: s.Code, Name: s.Name, TimeZone: s.TimeZone, FiscalCalendar: calendarCodes[deref(s.FiscalCalendarID)], Warehouses: []packageWarehouse{}}
		own := warehousesBySite[s.ID]
		// The default warehouse comes first: import creates a site with its first warehouse as the default.
		sort.SliceStable(own, func(i, j int) bool {
			return s.DefaultWarehouseID != nil && own[i].ID == *s.DefaultWarehouseID && own[j].ID != *s.DefaultWarehouseID
		})
		for _, w := range own {
			locs := locationsByWarehouse[w.ID]
			if locs == nil {
				locs = []packageLocation{}
			}
			out.Warehouses = append(out.Warehouses, packageWarehouse{Code: w.Code, Name: w.Name, Type: w.Type, TimeZone: w.TimeZone, Locations: locs})
		}
		pkg.Sites = append(pkg.Sites, out)
	}
}

func exportTeams(ctx context.Context, st store.Store, unitRefs map[string]packageUnitRef, pkg *OrgPackage) error {
	types, err := st.ListTeamTypes(ctx, store.TeamTypeFilter{}, "", packageRowCap)
	if err != nil {
		return err
	}
	teams, err := st.ListTeams(ctx, store.TeamFilter{}, "", packageRowCap)
	if err != nil {
		return err
	}
	if types.NextCode != "" || teams.NextCode != "" {
		return tooLarge("teams")
	}
	typeCodes := map[string]string{}
	for _, t := range types.Items {
		typeCodes[t.ID] = t.Code
		pkg.TeamTypes = append(pkg.TeamTypes, packageTeamType{Code: t.Code, Name: t.Name, MemberKinds: t.MemberKinds})
	}
	for _, t := range teams.Items {
		out := packageTeam{Code: t.Code, Name: t.Name, Description: t.Description, TeamType: typeCodes[t.TeamTypeID], EffectiveFrom: t.EffectiveFrom, EffectiveTo: t.EffectiveTo}
		if t.OperatingUnitID != nil {
			if ref, ok := unitRefs[*t.OperatingUnitID]; ok {
				out.OperatingUnit = &ref
			}
		}
		pkg.Teams = append(pkg.Teams, out)
	}
	return nil
}

// packageProblems collects validation findings by path, capped so a bad file stays readable.
type packageProblems map[string]string

func (p packageProblems) add(path, message string) {
	if len(p) < packageMaxProblems {
		p[path] = message
	}
}

func validatePackage(pkg OrgPackage, p packageProblems, limits domain.TreeLimits) {
	seen := map[string]bool{}
	dup := func(path, kind, key string) {
		if seen[kind+"/"+key] {
			p.add(path, "duplicate "+kind+" "+key)
		}
		seen[kind+"/"+key] = true
	}
	for i, e := range pkg.LegalEntities {
		path := fmt.Sprintf("legalEntities[%d]", i)
		in := store.LegalEntity{Code: strings.ToUpper(e.Code), Name: strings.TrimSpace(e.Name), Country: e.Country, Currency: e.Currency, TimeZone: e.TimeZone,
			DefaultLanguage: e.DefaultLanguage, RegistrationNumber: e.RegistrationNumber}
		if in.DefaultLanguage == "" {
			in.DefaultLanguage = store.DefaultLanguage
		}
		if reservedCodes[in.Code] {
			p.add(path+".code", "must not be DAT, SYS or ALL")
		}
		for field, rule := range validateLegalEntity(in) {
			p.add(path+"."+field, rule)
		}
		dup(path+".code", "legal entity", in.Code)
		if e.FiscalCalendar != "" && !siteCode.MatchString(strings.ToUpper(e.FiscalCalendar)) {
			p.add(path+".fiscalCalendar", "must be the code of a fiscal calendar")
		}
	}
	for i, u := range pkg.OperatingUnits {
		path := fmt.Sprintf("operatingUnits[%d]", i)
		code := strings.ToUpper(u.Code)
		if !slices.Contains(operatingUnitTypes, u.Type) {
			p.add(path+".type", "must be department, cost_center, business_unit or value_stream")
		}
		if !operatingUnitCode.MatchString(code) {
			p.add(path+".code", "must be 2 to 20 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9")
		}
		if name := strings.TrimSpace(u.Name); name == "" || utf8.RuneCountInString(name) > maxNameLength {
			p.add(path+".name", fmt.Sprintf("must be 1 to %d characters", maxNameLength))
		}
		dup(path+".code", "operating unit", u.Type+"/"+code)
	}
	for i, s := range pkg.Sites {
		validatePackageSite(fmt.Sprintf("sites[%d]", i), s, p, dup)
	}
	for i, t := range pkg.HierarchyTypes {
		path := fmt.Sprintf("hierarchyTypes[%d]", i)
		code := strings.ToUpper(t.Code)
		if !operatingUnitCode.MatchString(code) {
			p.add(path+".code", "must be 2 to 20 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9")
		}
		for field, rule := range validateShared(strPtr(strings.TrimSpace(t.Name)), nil, nil, nil) {
			p.add(path+"."+field, rule)
		}
		if msg := validatePurposes(t.Purposes); msg != "" {
			p.add(path+".purposes", msg)
		}
		dup(path+".code", "hierarchy type", code)
		validatePackageVersions(path, t, p, dup, limits)
	}
	for i, c := range pkg.FiscalCalendars {
		validatePackageCalendar(fmt.Sprintf("fiscalCalendars[%d]", i), c, p, dup)
	}
	for i, t := range pkg.TeamTypes {
		path := fmt.Sprintf("teamTypes[%d]", i)
		code := strings.ToUpper(t.Code)
		if !operatingUnitCode.MatchString(code) {
			p.add(path+".code", "must be 2 to 20 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9")
		}
		if name := strings.TrimSpace(t.Name); name == "" || utf8.RuneCountInString(name) > teamNameLength {
			p.add(path+".name", fmt.Sprintf("must be 1 to %d characters", teamNameLength))
		}
		if !validMemberKinds(t.MemberKinds) {
			p.add(path+".memberKinds", "must be one or two distinct values of user and party")
		}
		dup(path+".code", "team type", code)
	}
	for i, t := range pkg.Teams {
		validatePackageTeam(fmt.Sprintf("teams[%d]", i), t, p, dup)
	}
	validatePackageReferenceData(pkg, p, dup)
}

func validatePackageSite(path string, s packageSite, p packageProblems, dup func(path, kind, key string)) {
	code := strings.ToUpper(s.Code)
	if !siteCode.MatchString(code) {
		p.add(path+".code", "must be 1 to 10 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9")
	}
	var zone *string
	if s.TimeZone != "" {
		zone = &s.TimeZone
	}
	for field, rule := range validateShared(strPtr(strings.TrimSpace(s.Name)), zone, nil, nil) {
		p.add(path+"."+field, rule)
	}
	dup(path+".code", "site", strings.ToUpper(s.LegalEntity)+"/"+code)
	if s.FiscalCalendar != "" && !siteCode.MatchString(strings.ToUpper(s.FiscalCalendar)) {
		p.add(path+".fiscalCalendar", "must be the code of a fiscal calendar")
	}
	if len(s.Warehouses) == 0 {
		p.add(path+".warehouses", "a site needs at least one warehouse; the first is its default")
	}
	for j, w := range s.Warehouses {
		wpath := fmt.Sprintf("%s.warehouses[%d]", path, j)
		wcode := strings.ToUpper(w.Code)
		if !siteCode.MatchString(wcode) {
			p.add(wpath+".code", "must be 1 to 10 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9")
		}
		var wzone *string
		if w.TimeZone != "" {
			wzone = &w.TimeZone
		}
		for field, rule := range validateShared(strPtr(strings.TrimSpace(w.Name)), wzone, nil, nil) {
			p.add(wpath+"."+field, rule)
		}
		if w.Type != "" && !warehouseType(w.Type) {
			p.add(wpath+".type", invalidWarehouseTy)
		}
		dup(wpath+".code", "warehouse", strings.ToUpper(s.LegalEntity)+"/"+wcode)
		for k, l := range w.Locations {
			lpath := fmt.Sprintf("%s.locations[%d]", wpath, k)
			lcode := strings.ToUpper(l.Code)
			if !locationCode.MatchString(lcode) {
				p.add(lpath+".code", invalidLocationCode)
			}
			for field, rule := range validateShared(strPtr(strings.TrimSpace(l.Name)), nil, nil, nil) {
				p.add(lpath+"."+field, rule)
			}
			if l.Type != "" && !locationType(l.Type) {
				p.add(lpath+".type", invalidLocationTy)
			}
			dup(lpath+".code", "location", strings.ToUpper(s.LegalEntity)+"/"+wcode+"/"+lcode)
		}
	}
}

func validatePackageTeam(path string, t packageTeam, p packageProblems, dup func(path, kind, key string)) {
	code := strings.ToUpper(t.Code)
	if !operatingUnitCode.MatchString(code) {
		p.add(path+".code", "must be 2 to 20 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9")
	}
	if name := strings.TrimSpace(t.Name); name == "" || utf8.RuneCountInString(name) > teamNameLength {
		p.add(path+".name", fmt.Sprintf("must be 1 to %d characters", teamNameLength))
	}
	if t.Description != nil && utf8.RuneCountInString(*t.Description) > teamDescriptionLength {
		p.add(path+".description", fmt.Sprintf("must be at most %d characters", teamDescriptionLength))
	}
	from, fromOK := parseDate(t.EffectiveFrom)
	if !fromOK {
		p.add(path+".effectiveFrom", "must be a date as YYYY-MM-DD")
	}
	if t.EffectiveTo != nil {
		to, toOK := parseDate(*t.EffectiveTo)
		if !toOK {
			p.add(path+".effectiveTo", "must be a date as YYYY-MM-DD")
		} else if fromOK && to.Before(from) {
			p.add(path+".effectiveTo", "must not be before effectiveFrom")
		}
	}
	dup(path+".code", "team", code)
}
