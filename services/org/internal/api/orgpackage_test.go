package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/api"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

// memOrg keeps the records an organization package touches; every other Store method panics.
type memOrg struct {
	store.Store
	n          int
	creates    int
	entities   []store.LegalEntity
	units      []store.OperatingUnit
	sites      []store.Site
	warehouses []store.Warehouse
	locations  []store.Location
	hier       []store.HierarchyType
	teamTypes  []store.TeamType
	teams      []store.Team
	// inactive lists reference codes (country, currency, language) the tenant has deactivated.
	inactive map[string]bool
	// failWarehouse makes CreateWarehouse fail, standing in for a store error part-way.
	failWarehouse bool
}

func (m *memOrg) ListReference(_ context.Context, kind store.ReferenceKind, _ store.ReferenceFilter) ([]store.ReferenceItem, error) {
	codes := map[store.ReferenceKind][]string{store.KindCountry: {"US", "DE"}, store.KindCurrency: {"USD", "EUR"}, store.KindLanguage: {"en", "de"}}[kind]
	items := []store.ReferenceItem{}
	for _, code := range codes {
		if !m.inactive[code] {
			items = append(items, store.ReferenceItem{Code: code, Status: "active"})
		}
	}
	return items, nil
}

func (m *memOrg) id() string {
	m.n++
	m.creates++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", m.n)
}

func (m *memOrg) CreateLegalEntity(_ context.Context, in store.LegalEntity, _ string) (store.LegalEntity, error) {
	in.ID, in.Status, in.Version = m.id(), "active", 1
	m.entities = append(m.entities, in)
	return in, nil
}

func (m *memOrg) ListLegalEntities(context.Context, store.LegalEntityFilter, string, int) (store.LegalEntityPage, error) {
	return store.LegalEntityPage{Items: m.entities}, nil
}

func (m *memOrg) CreateOperatingUnit(_ context.Context, in store.OperatingUnit, _ string) (store.OperatingUnit, error) {
	in.ID, in.Status = m.id(), "active"
	m.units = append(m.units, in)
	return in, nil
}

func (m *memOrg) ListOperatingUnits(context.Context, store.OperatingUnitFilter, store.OperatingUnitKey, int) (store.OperatingUnitPage, error) {
	return store.OperatingUnitPage{Items: m.units}, nil
}

func (m *memOrg) CreateSite(_ context.Context, in store.Site, _ string) (store.Site, error) {
	in.ID, in.Status = m.id(), "active"
	if in.DefaultWarehouse != nil {
		w := *in.DefaultWarehouse
		w.ID, w.SiteID, w.LegalEntityID, w.Status = m.id(), in.ID, in.LegalEntityID, "active"
		m.warehouses = append(m.warehouses, w)
		in.DefaultWarehouseID = &w.ID
	}
	m.sites = append(m.sites, in)
	return in, nil
}

// scopeOf returns the legal entity the caller is scoped to, empty when unscoped.
func scopeOf(ctx context.Context) string {
	p, _ := tenant.FromContext(ctx)
	return p.LegalEntityID
}

func (m *memOrg) ListSites(ctx context.Context, _ store.SiteFilter, _ store.SiteKey, _ int) (store.SitePage, error) {
	page := store.SitePage{}
	for _, s := range m.sites {
		if s.LegalEntityID == scopeOf(ctx) {
			page.Items = append(page.Items, s)
		}
	}
	return page, nil
}

func (m *memOrg) CreateWarehouse(ctx context.Context, in store.Warehouse, _ string) (store.Warehouse, error) {
	if m.failWarehouse {
		return store.Warehouse{}, httpx.NewProblem(http.StatusConflict, "ORG-WAREHOUSE-CODE-DUPLICATE", "a warehouse with this code already exists")
	}
	in.ID, in.LegalEntityID, in.Status = m.id(), scopeOf(ctx), "active"
	m.warehouses = append(m.warehouses, in)
	return in, nil
}

func (m *memOrg) ListWarehouses(ctx context.Context, _ store.WarehouseFilter, _ store.SiteKey, _ int) (store.WarehousePage, error) {
	page := store.WarehousePage{}
	for _, w := range m.warehouses {
		if w.LegalEntityID == scopeOf(ctx) {
			page.Items = append(page.Items, w)
		}
	}
	return page, nil
}

func (m *memOrg) CreateLocation(ctx context.Context, in store.Location, _ string) (store.Location, error) {
	in.ID, in.LegalEntityID = m.id(), scopeOf(ctx)
	m.locations = append(m.locations, in)
	return in, nil
}

func (m *memOrg) ListLocations(ctx context.Context, _ store.LocationFilter, _ store.SiteKey, _ int) (store.LocationPage, error) {
	page := store.LocationPage{}
	for _, l := range m.locations {
		if l.LegalEntityID == scopeOf(ctx) {
			page.Items = append(page.Items, l)
		}
	}
	return page, nil
}

func (m *memOrg) CreateHierarchyType(_ context.Context, in store.HierarchyType, _ string) (store.HierarchyType, error) {
	in.ID = m.id()
	m.hier = append(m.hier, in)
	return in, nil
}

func (m *memOrg) ListHierarchyTypes(context.Context, store.HierarchyTypeFilter, string, int) (store.HierarchyTypePage, error) {
	return store.HierarchyTypePage{Items: m.hier}, nil
}

func (m *memOrg) CreateTeamType(_ context.Context, in store.TeamType, _ string) (store.TeamType, bool, error) {
	in.ID = m.id()
	m.teamTypes = append(m.teamTypes, in)
	return in, true, nil
}

func (m *memOrg) ListTeamTypes(context.Context, store.TeamTypeFilter, string, int) (store.TeamTypePage, error) {
	return store.TeamTypePage{Items: m.teamTypes}, nil
}

func (m *memOrg) CreateTeam(_ context.Context, in store.Team, _ string) (store.Team, bool, error) {
	in.ID = m.id()
	m.teams = append(m.teams, in)
	return in, true, nil
}

func (m *memOrg) ListTeams(context.Context, store.TeamFilter, string, int) (store.TeamPage, error) {
	return store.TeamPage{Items: m.teams}, nil
}

const packageBody = `{"schemaVersion":1,
 "legalEntities":[{"code":"usmf","name":"Demo USA","country":"US","currency":"USD","timeZone":"America/Chicago","defaultLanguage":"en-US"},
                  {"code":"DEMF","name":"Demo DE","country":"DE","currency":"EUR","timeZone":"Europe/Berlin","defaultLanguage":"de-DE"}],
 "operatingUnits":[{"type":"department","code":"FIN","name":"Finance","legalEntity":"USMF"},{"type":"cost_center","code":"CC-1100","name":"Assembly"}],
 "sites":[{"legalEntity":"USMF","code":"DET","name":"Detroit","timeZone":"America/Detroit","warehouses":[
    {"code":"DET-MAIN","name":"Main","type":"standard","locations":[{"code":"RCV-01","name":"Receiving","type":"receiving"},{"code":"STO-A1","name":"Aisle","type":"storage"}]},
    {"code":"DET-QA","name":"Quarantine","type":"quarantine","locations":[]}]}],
 "hierarchyTypes":[{"code":"REPORTING","name":"Reporting","purposes":["reporting"],"backdateAllowed":false}],
 "teamTypes":[{"code":"PLANT","name":"Plant crew","memberKinds":["user"]}],
 "teams":[{"code":"DET-LEADS","name":"Detroit leads","teamType":"PLANT","operatingUnit":{"type":"department","code":"FIN"},"effectiveFrom":"2026-01-01"}]}`

const packageRecords = 12 // 2 legal entities, 2 units, 1 site, 2 warehouses, 2 locations, 1 hierarchy type, 1 team type, 1 team

const packageDuties = "org.package.manage"

func importPackage(t *testing.T, r http.Handler, query, body string) (int, api.ImportReport, map[string]any) {
	t.Helper()
	rec := do(r, http.MethodPost, "/v1/org/import"+query, body, packageDuties, true)
	var report api.ImportReport
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &report)
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	return rec.Code, report, raw
}

func TestImportPackage_SecondImportCreatesNothing(t *testing.T) {
	mem := &memOrg{}
	r, _ := memRouter(mem)
	code, first, raw := importPackage(t, r, "", packageBody)
	if code != http.StatusOK {
		t.Fatalf("first import = %d %v", code, raw)
	}
	if first.Created != packageRecords || first.Unchanged != 0 {
		t.Fatalf("first report = %+v", first)
	}
	if first.Kinds["warehouses"].Created != 2 || first.Kinds["locations"].Created != 2 || first.Kinds["sites"].Created != 1 {
		t.Fatalf("kinds = %+v", first.Kinds)
	}
	creates := mem.creates
	code, second, _ := importPackage(t, r, "", packageBody)
	if code != http.StatusOK || second.Created != 0 || second.Unchanged != first.Created {
		t.Fatalf("second import = %d %+v", code, second)
	}
	if mem.creates != creates {
		t.Fatalf("second import wrote %d records", mem.creates-creates)
	}
}

func TestImportPackage_DryRunWritesNothingAndMatchesRealRun(t *testing.T) {
	mem := &memOrg{}
	r, _ := memRouter(mem)
	code, dry, raw := importPackage(t, r, "?dryRun=true", packageBody)
	if code != http.StatusOK || !dry.DryRun || mem.creates != 0 {
		t.Fatalf("dry run = %d %+v writes=%d %v", code, dry, mem.creates, raw)
	}
	_, real, _ := importPackage(t, r, "", packageBody)
	if dry.Created != real.Created || dry.Kinds["locations"] != real.Kinds["locations"] || dry.Kinds["teams"] != real.Kinds["teams"] {
		t.Fatalf("dry %+v != real %+v", dry, real)
	}
}

func TestExportPackage_RoundTripsIntoAnotherTenant(t *testing.T) {
	source := &memOrg{}
	r, _ := memRouter(source)
	if code, _, raw := importPackage(t, r, "", packageBody); code != http.StatusOK {
		t.Fatal(raw)
	}
	rec := do(r, http.MethodPost, "/v1/org/export", "", packageDuties, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Header().Get("Content-Disposition"), "org-package.json") {
		t.Fatalf("export = %d %s", rec.Code, rec.Body)
	}
	var exported map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	if exported["schemaVersion"] != float64(1) || exported["exportedAt"] == "" {
		t.Fatalf("export header = %v", exported)
	}
	sites := exported["sites"].([]any)
	warehouses := sites[0].(map[string]any)["warehouses"].([]any)
	if warehouses[0].(map[string]any)["code"] != "DET-MAIN" {
		t.Fatalf("default warehouse must come first: %v", warehouses)
	}
	target := &memOrg{}
	r2, _ := memRouter(target)
	code, report, raw := importPackage(t, r2, "", rec.Body.String()) // the exported file as is, exportedAt included
	if code != http.StatusOK || report.Created != packageRecords {
		t.Fatalf("import of export = %d %+v %v", code, report, raw)
	}
	if len(target.teams) != 1 || target.teams[0].OperatingUnitID == nil {
		t.Fatalf("team lost its operating unit: %+v", target.teams)
	}
}

func TestImportPackage_ExistingRecordsAreNotUpdated(t *testing.T) {
	mem := &memOrg{}
	r, _ := memRouter(mem)
	importPackage(t, r, "", packageBody)
	renamed := strings.Replace(packageBody, `"Demo USA"`, `"Renamed"`, 1)
	code, report, _ := importPackage(t, r, "", renamed)
	if code != http.StatusOK || report.Created != 0 || len(report.Differences) != 1 || report.Differences[0].Field != "name" || report.Differences[0].Code != "USMF" {
		t.Fatalf("report = %d %+v", code, report)
	}
	if mem.entities[0].Name != "Demo USA" {
		t.Fatalf("import changed the existing name: %q", mem.entities[0].Name)
	}
}

func TestImportPackage_Rejections(t *testing.T) {
	cases := []struct {
		name, body, code, path string
	}{
		{"version", `{"schemaVersion":2}`, "ORG-PKG-VERSION-UNSUPPORTED", ""},
		{"bad country", strings.Replace(packageBody, `"country":"US"`, `"country":"USA"`, 1), "ORG-PKG-INVALID", "legalEntities[0].country"},
		{"duplicate code", strings.Replace(packageBody, `"code":"DEMF"`, `"code":"USMF"`, 1), "ORG-PKG-INVALID", "legalEntities[1].code"},
		{"unknown entity", strings.Replace(packageBody, `"legalEntity":"USMF","code":"DET"`, `"legalEntity":"NOPE","code":"DET"`, 1), "ORG-PKG-INVALID", "sites[0].legalEntity"},
		{"unknown team type", strings.Replace(packageBody, `"teamType":"PLANT"`, `"teamType":"NOPE"`, 1), "ORG-PKG-INVALID", "teams[0].teamType"},
		{"unknown unit", strings.Replace(packageBody, `"code":"FIN"},"effectiveFrom"`, `"code":"ZZZ"},"effectiveFrom"`, 1), "ORG-PKG-INVALID", "teams[0].operatingUnit"},
		{"site without warehouse", `{"schemaVersion":1,"legalEntities":[],"sites":[{"legalEntity":"USMF","code":"DET","name":"Detroit","warehouses":[]}]}`, "ORG-PKG-INVALID", "sites[0].warehouses"},
		{"bad purpose", strings.Replace(packageBody, `"purposes":["reporting"]`, `"purposes":["nope"]`, 1), "ORG-PKG-INVALID", "hierarchyTypes[0].purposes"},
		{"bad date", strings.Replace(packageBody, `"2026-01-01"`, `"01/01/2026"`, 1), "ORG-PKG-INVALID", "teams[0].effectiveFrom"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mem := &memOrg{}
			r, _ := memRouter(mem)
			code, _, raw := importPackage(t, r, "", c.body)
			if code != http.StatusUnprocessableEntity || raw["code"] != c.code {
				t.Fatalf("= %d %v", code, raw)
			}
			if c.path != "" {
				if errs, _ := raw["errors"].(map[string]any); errs[c.path] == nil {
					t.Fatalf("no error at %s: %v", c.path, raw["errors"])
				}
			}
			if mem.creates != 0 {
				t.Fatalf("invalid package wrote %d records", mem.creates)
			}
		})
	}
}

func TestPackageRoutes_Authorization(t *testing.T) {
	r, _ := memRouter(&memOrg{})
	for _, route := range []string{"/v1/org/export", "/v1/org/import"} {
		t.Run(route, func(t *testing.T) {
			if rec := do(r, http.MethodPost, route, packageBody, "org.read", true); rec.Code != http.StatusForbidden {
				t.Fatalf("without the duty = %d", rec.Code)
			}
			if rec := doWithoutTenantAdmin(r, http.MethodPost, route, packageBody, packageDuties, true); rec.Code != http.StatusForbidden {
				t.Fatalf("without tenant.administer = %d", rec.Code)
			}
			if rec := do(r, http.MethodPost, route, packageBody, packageDuties, true, "X-Passage-Legal-Entity", "00000000-0000-4000-8000-000000000001"); rec.Code != http.StatusForbidden {
				t.Fatalf("scoped caller = %d", rec.Code)
			}
			if rec := do(r, http.MethodPost, route, packageBody, packageDuties, false); rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
				t.Fatalf("without a tenant = %d", rec.Code)
			}
		})
	}
	if rec := do(r, http.MethodPost, "/v1/org/import?dryRun=maybe", packageBody, packageDuties, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad dryRun = %d", rec.Code)
	}
}

func memRouter(m *memOrg) (http.Handler, *api.API) {
	a := api.New(m)
	router := httpx.NewRouter()
	a.Register(router)
	return router, a
}

func memRouterFor(s store.Store) (http.Handler, *api.API) {
	a := api.New(s)
	router := httpx.NewRouter()
	a.Register(router)
	return router, a
}

func TestImportPackage_RefusedBeforeTheFirstWrite(t *testing.T) {
	seeded := func() *memOrg {
		mem := &memOrg{}
		r, _ := memRouter(mem)
		importPackage(t, r, "", packageBody)
		return mem
	}
	otherSite := `{"schemaVersion":1,"sites":[{"legalEntity":"USMF","code":"FLT","name":"Flint","warehouses":[{"code":"NEW-1","name":"New"},{"code":"DET-QA","name":"Moved"}]}]}`
	cases := []struct {
		name  string
		mem   func() *memOrg
		body  string
		path  string
		error string
	}{
		{"warehouse code used at another site", seeded, otherSite, "sites[0].warehouses[1].code", "already exists at site DET"},
		{"site in an inactive legal entity", func() *memOrg {
			mem := seeded()
			mem.entities[0].Status = "inactive"
			return mem
		}, `{"schemaVersion":1,"sites":[{"legalEntity":"USMF","code":"FLT","name":"Flint","warehouses":[{"code":"FLT-1","name":"Flint"}]}]}`, "sites[0].legalEntity", "inactive"},
		{"warehouse at an inactive site", func() *memOrg {
			mem := seeded()
			mem.sites[0].Status = "inactive"
			return mem
		}, `{"schemaVersion":1,"legalEntities":[{"code":"NEW1","name":"New","country":"US","currency":"USD","timeZone":"UTC"}],"sites":[{"legalEntity":"USMF","code":"DET","name":"Detroit","warehouses":[{"code":"DET-MAIN","name":"Main"},{"code":"DET-NEW","name":"New"}]}]}`, "sites[0].warehouses[1].code", "inactive"},
		{"team on an inactive operating unit", func() *memOrg {
			mem := seeded()
			mem.units[0].Status = "inactive"
			return mem
		}, strings.Replace(packageBody, `"code":"DET-LEADS"`, `"code":"DET-NEW"`, 1), "teams[0].operatingUnit", "inactive"},
		{"currency inactive in the tenant", func() *memOrg { return &memOrg{inactive: map[string]bool{"EUR": true}} }, packageBody, "legalEntities[1].currency", "EUR"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mem := c.mem()
			r, _ := memRouter(mem)
			before := mem.creates
			code, _, raw := importPackage(t, r, "", c.body)
			errs, _ := raw["errors"].(map[string]any)
			if code != http.StatusUnprocessableEntity || raw["code"] != "ORG-PKG-INVALID" || !strings.Contains(fmt.Sprint(errs[c.path]), c.error) {
				t.Fatalf("= %d %v", code, raw)
			}
			if mem.creates != before {
				t.Fatalf("a refused package wrote %d records", mem.creates-before)
			}
		})
	}
}

// A store failure part-way leaves the earlier records; importing the same package again creates
// only what is still missing.
func TestImportPackage_ResumesAfterAFailurePartWay(t *testing.T) {
	mem := &memOrg{failWarehouse: true}
	r, _ := memRouter(mem)
	code, _, raw := importPackage(t, r, "", packageBody)
	if code != http.StatusConflict || len(mem.entities) != 2 || len(mem.sites) != 1 || len(mem.teams) != 0 {
		t.Fatalf("failed import = %d %v; entities=%d sites=%d teams=%d", code, raw, len(mem.entities), len(mem.sites), len(mem.teams))
	}
	mem.failWarehouse = false
	code, report, raw := importPackage(t, r, "", packageBody)
	// The first run created 2 entities, 2 units, the site with its default warehouse and that
	// warehouse's 2 locations before the second warehouse failed.
	if code != http.StatusOK || report.Created != packageRecords-8 || report.Unchanged != 8 || len(mem.warehouses) != 2 || len(mem.teams) != 1 {
		t.Fatalf("resumed import = %d %+v %v", code, report, raw)
	}
}

func TestImportPackage_ReportsDifferingNamesOfEveryKind(t *testing.T) {
	mem := &memOrg{}
	r, _ := memRouter(mem)
	importPackage(t, r, "", packageBody)
	renamed := strings.NewReplacer(`"Detroit leads"`, `"Leads"`, `"Quarantine"`, `"QA"`, `"Receiving"`, `"Dock"`).Replace(packageBody)
	_, report, _ := importPackage(t, r, "", renamed)
	got := map[string]bool{}
	for _, d := range report.Differences {
		got[d.Kind+" "+d.Code] = true
	}
	for _, want := range []string{"teams DET-LEADS", "warehouses USMF/DET-QA", "locations USMF/DET-MAIN/RCV-01"} {
		if !got[want] {
			t.Fatalf("missing difference %q in %+v", want, report.Differences)
		}
	}
	if report.Created != 0 || mem.teams[0].Name != "Detroit leads" {
		t.Fatalf("import changed existing records: %+v %q", report, mem.teams[0].Name)
	}
}

func TestImportPackage_SizeLimits(t *testing.T) {
	r, _ := memRouter(&memOrg{})
	huge := `{"schemaVersion":1,"teamTypes":[{"code":"` + strings.Repeat("A", 9<<20) + `"}]}`
	if rec := do(r, http.MethodPost, "/v1/org/import", huge, packageDuties, true); rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("9 MiB body = %d", rec.Code)
	}
	locations := strings.TrimSuffix(strings.Repeat(`{"code":"L","name":"L"},`, 10001), ",")
	many := `{"schemaVersion":1,"sites":[{"legalEntity":"USMF","code":"S","name":"S","warehouses":[{"code":"W","name":"W","locations":[` + locations + `]}]}]}`
	code, _, raw := importPackage(t, r, "", many)
	if code != http.StatusUnprocessableEntity || raw["code"] != "ORG-PKG-TOO-LARGE" {
		t.Fatalf("10001 locations = %d %v", code, raw)
	}
}
