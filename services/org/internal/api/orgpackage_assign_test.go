package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/api"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

func (m *memOrg) UpdateLegalEntity(_ context.Context, id string, version int, in store.LegalEntityPatch) (store.LegalEntity, error) {
	for i := range m.entities {
		if m.entities[i].ID == id && m.entities[i].Version == version {
			m.creates++
			m.entities[i].FiscalCalendarID = in.FiscalCalendarID
			m.entities[i].Version++
			return m.entities[i], nil
		}
	}
	return store.LegalEntity{}, errors.New("version conflict")
}

func (m *memOrg) UpdateSite(_ context.Context, id string, version int, in store.SitePatch) (store.Site, error) {
	for i := range m.sites {
		if m.sites[i].ID == id && m.sites[i].Version == version {
			m.creates++
			m.sites[i].FiscalCalendarID = in.FiscalCalendarID
			m.sites[i].Version++
			return m.sites[i], nil
		}
	}
	return store.Site{}, errors.New("version conflict")
}

const assignBody = `{"schemaVersion":1,
 "legalEntities":[{"code":"USMF","name":"Demo USA","country":"US","currency":"USD","timeZone":"America/Chicago","defaultLanguage":"en-US","fiscalCalendar":"std"}],
 "sites":[{"legalEntity":"USMF","code":"DET","name":"Detroit","fiscalCalendar":"ALT","warehouses":[{"code":"DET-MAIN","name":"Main","locations":[]}]}],
 "fiscalCalendars":[{"code":"STD","name":"Standard","pattern":"4-4-5","years":[{"fiscalYear":2026}]},
                    {"code":"ALT","name":"Alternate","pattern":"calendar","years":[{"fiscalYear":2026}]}]}`

// 1 legal entity, 1 site, 1 warehouse, 2 calendars, 2 years, 2 assignments
const assignRecords = 9

func TestImportPackage_FiscalCalendarAssignments(t *testing.T) {
	mem := &memOrg{}
	r, _ := memRouter(mem)
	code, first, raw := importPackage(t, r, "", assignBody)
	if code != http.StatusOK || first.Created != assignRecords || first.Kinds["fiscalCalendarAssignments"].Created != 2 {
		t.Fatalf("first import = %d %+v %v", code, first, raw)
	}
	if mem.entities[0].FiscalCalendarID == nil || *mem.entities[0].FiscalCalendarID != mem.cals[0].ID {
		t.Fatalf("legal entity calendar = %v, want %s", mem.entities[0].FiscalCalendarID, mem.cals[0].ID)
	}
	if mem.sites[0].FiscalCalendarID == nil || *mem.sites[0].FiscalCalendarID != mem.cals[1].ID {
		t.Fatalf("site calendar = %v, want %s", mem.sites[0].FiscalCalendarID, mem.cals[1].ID)
	}
	creates := mem.creates
	code, second, _ := importPackage(t, r, "", assignBody)
	if code != http.StatusOK || second.Created != 0 || second.Unchanged != assignRecords || len(second.Differences) != 0 || mem.creates != creates {
		t.Fatalf("second import = %d %+v (wrote %d)", code, second, mem.creates-creates)
	}
}

func TestImportPackage_FiscalCalendarAssignmentsDryRunMatchesRealRun(t *testing.T) {
	mem := &memOrg{}
	r, _ := memRouter(mem)
	code, dry, raw := importPackage(t, r, "?dryRun=true", assignBody)
	if code != http.StatusOK || mem.creates != 0 {
		t.Fatalf("dry run = %d %+v writes=%d %v", code, dry, mem.creates, raw)
	}
	_, real, _ := importPackage(t, r, "", assignBody)
	if dry.Created != real.Created || dry.Kinds["fiscalCalendarAssignments"] != real.Kinds["fiscalCalendarAssignments"] {
		t.Fatalf("dry %+v != real %+v", dry, real)
	}
}

func TestExportPackage_FiscalCalendarAssignmentsRoundTrip(t *testing.T) {
	source := &memOrg{}
	r, _ := memRouter(source)
	if code, _, raw := importPackage(t, r, "", assignBody); code != http.StatusOK {
		t.Fatal(raw)
	}
	exported := do(r, http.MethodPost, "/v1/org/export", "", packageDuties, true)
	var pkg struct {
		LegalEntities []struct{ FiscalCalendar string }
		Sites         []struct{ FiscalCalendar string }
	}
	if err := json.Unmarshal(exported.Body.Bytes(), &pkg); err != nil || exported.Code != http.StatusOK {
		t.Fatalf("export = %d %s %v", exported.Code, exported.Body, err)
	}
	if pkg.LegalEntities[0].FiscalCalendar != "STD" || pkg.Sites[0].FiscalCalendar != "ALT" {
		t.Fatalf("exported assignments = %+v", pkg)
	}
	target := &memOrg{}
	r2, _ := memRouter(target)
	code, report, raw := importPackage(t, r2, "", exported.Body.String())
	if code != http.StatusOK || report.Created != assignRecords {
		t.Fatalf("import of export = %d %+v %v", code, report, raw)
	}
}

func TestImportPackage_FiscalCalendarOfExistingRecordIsReportedNotChanged(t *testing.T) {
	mem := &memOrg{}
	r, _ := memRouter(mem)
	importPackage(t, r, "", assignBody)
	swapped := strings.Replace(strings.Replace(assignBody, `"fiscalCalendar":"std"`, `"fiscalCalendar":"ALT"`, 1), `"fiscalCalendar":"ALT","warehouses"`, `"fiscalCalendar":"STD","warehouses"`, 1)
	creates := mem.creates
	code, report, raw := importPackage(t, r, "", swapped)
	if code != http.StatusOK || report.Created != 0 || mem.creates != creates || len(report.Differences) != 2 {
		t.Fatalf("report = %d %+v %v", code, report, raw)
	}
	for _, d := range report.Differences {
		if d.Field != "fiscalCalendar" {
			t.Fatalf("difference = %+v", d)
		}
	}
}

func TestImportPackage_FiscalCalendarFillsARecordAnEarlierRunLeftUnassigned(t *testing.T) {
	mem := &memOrg{}
	r, _ := memRouter(mem)
	bare := strings.NewReplacer(`,"fiscalCalendar":"std"`, ``, `"fiscalCalendar":"ALT",`, ``).Replace(assignBody)
	if code, _, raw := importPackage(t, r, "", bare); code != http.StatusOK {
		t.Fatal(raw)
	}
	code, report, raw := importPackage(t, r, "", assignBody)
	if code != http.StatusOK || report.Kinds["fiscalCalendarAssignments"].Created != 2 || report.Created != 2 {
		t.Fatalf("report = %d %+v %v", code, report, raw)
	}
}

func TestImportPackage_FiscalCalendarRejections(t *testing.T) {
	cases := []struct{ name, body, path string }{
		{"unknown calendar", strings.Replace(assignBody, `"fiscalCalendar":"std"`, `"fiscalCalendar":"NOPE"`, 1), "legalEntities[0].fiscalCalendar"},
		{"calendar without a year", strings.Replace(assignBody, `"fiscalCalendar":"ALT"`, `"fiscalCalendar":"EMPTY"`, 1), "sites[0].fiscalCalendar"},
		{"bad code", strings.Replace(assignBody, `"fiscalCalendar":"std"`, `"fiscalCalendar":"no good"`, 1), "legalEntities[0].fiscalCalendar"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := c.body
			if c.name == "calendar without a year" {
				body = strings.Replace(body, `"fiscalCalendars":[`, `"fiscalCalendars":[{"code":"EMPTY","name":"Empty","pattern":"calendar","years":[]},`, 1)
			}
			mem := &memOrg{}
			r, _ := memRouter(mem)
			code, _, raw := importPackage(t, r, "", body)
			errs, _ := raw["errors"].(map[string]any)
			if code != http.StatusUnprocessableEntity || errs[c.path] == nil || mem.creates != 0 {
				t.Fatalf("%s = %d %v (writes %d)", c.name, code, raw, mem.creates)
			}
		})
	}
}

// The assignments are written by PATCH after the create, under row-level security, and the
// export names the calendars again by code.
func TestOrgPackage_Postgres_FiscalCalendarAssignmentsRoundTrip(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, admin, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	r, _ := memRouterFor(store.New(app))
	first := do(r, http.MethodPost, "/v1/org/import", assignBody, packageDuties, true)
	var report api.ImportReport
	if err := json.Unmarshal(first.Body.Bytes(), &report); err != nil || first.Code != http.StatusOK || report.Created != assignRecords {
		t.Fatalf("first import = %d %s", first.Code, first.Body)
	}
	second := do(r, http.MethodPost, "/v1/org/import", assignBody, packageDuties, true)
	var again api.ImportReport
	if err := json.Unmarshal(second.Body.Bytes(), &again); err != nil || second.Code != http.StatusOK || again.Created != 0 || again.Unchanged != assignRecords || len(again.Differences) != 0 {
		t.Fatalf("second import = %d %s", second.Code, second.Body)
	}
	exported := do(r, http.MethodPost, "/v1/org/export", "", packageDuties, true)
	if exported.Code != http.StatusOK || !strings.Contains(exported.Body.String(), `"fiscalCalendar":"STD"`) || !strings.Contains(exported.Body.String(), `"fiscalCalendar":"ALT"`) {
		t.Fatalf("export = %d %s", exported.Code, exported.Body)
	}
	other := do(r, http.MethodPost, "/v1/org/import", exported.Body.String(), packageDuties, true, "X-Passage-Tenant", testkit.TenantID(2))
	if other.Code != http.StatusOK || !strings.Contains(other.Body.String(), `"fiscalCalendarAssignments":{"created":2`) {
		t.Fatalf("import into another tenant = %d %s", other.Code, other.Body)
	}
}

// A calendar the package only names (no years in the package) must already be assignable in the
// tenant: active with a generated year, the same rule UpdateLegalEntity applies.
func TestImportPackage_FiscalCalendarInTenantMustBeAssignable(t *testing.T) {
	onlyNames := `{"schemaVersion":1,
 "legalEntities":[{"code":"USMF","name":"Demo USA","country":"US","currency":"USD","timeZone":"America/Chicago","defaultLanguage":"en-US","fiscalCalendar":"OLD"}]}`
	cases := []struct {
		name string
		cal  store.FiscalCalendar
		want int
	}{
		{"inactive with a year", store.FiscalCalendar{ID: "cal-old", Code: "OLD", Status: "inactive", GeneratedYears: []int{2025}}, http.StatusUnprocessableEntity},
		{"active without a year", store.FiscalCalendar{ID: "cal-old", Code: "OLD", Status: "active"}, http.StatusUnprocessableEntity},
		{"active with a year", store.FiscalCalendar{ID: "cal-old", Code: "OLD", Status: "active", GeneratedYears: []int{2025}}, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mem := &memOrg{cals: []store.FiscalCalendar{c.cal}}
			r, _ := memRouter(mem)
			code, report, raw := importPackage(t, r, "", onlyNames)
			if code != c.want {
				t.Fatalf("import = %d %v", code, raw)
			}
			if code != http.StatusOK {
				if errs, _ := raw["errors"].(map[string]any); errs["legalEntities[0].fiscalCalendar"] == nil || mem.creates != 0 {
					t.Fatalf("refusal = %v (writes %d)", raw, mem.creates)
				}
				return
			}
			if report.Kinds["fiscalCalendarAssignments"].Created != 1 || deref(mem.entities[0].FiscalCalendarID) != "cal-old" {
				t.Fatalf("report = %+v, calendar = %v", report, mem.entities[0].FiscalCalendarID)
			}
		})
	}
}

func TestImportPackage_FiscalCalendarDifferencesDryRunMatchesRealRun(t *testing.T) {
	mem := &memOrg{}
	r, _ := memRouter(mem)
	importPackage(t, r, "", assignBody)
	swapped := strings.Replace(assignBody, `"fiscalCalendar":"std"`, `"fiscalCalendar":"ALT"`, 1)
	_, dry, _ := importPackage(t, r, "?dryRun=true", swapped)
	_, real, _ := importPackage(t, r, "", swapped)
	if len(dry.Differences) != 1 || len(real.Differences) != 1 || dry.Differences[0] != real.Differences[0] || dry.Kinds["fiscalCalendarAssignments"] != real.Kinds["fiscalCalendarAssignments"] {
		t.Fatalf("dry %+v != real %+v", dry, real)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
