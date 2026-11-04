package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/api"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

// The package moves a whole organization model through PostgreSQL under row-level security:
// sites, warehouses and locations are legal-entity scoped, so import and export must scope
// per entity for an administrator without a legal-entity claim.
func TestOrgPackage_Postgres_ImportExportRoundTrip(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, admin, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	r, _ := memRouterFor(store.New(app))
	tenantTwo := []string{"X-Passage-Tenant", testkit.TenantID(2)}

	first := do(r, http.MethodPost, "/v1/org/import", packageBody, packageDuties, true)
	if first.Code != http.StatusOK {
		t.Fatalf("first import = %d %s", first.Code, first.Body)
	}
	var report api.ImportReport
	if err := json.Unmarshal(first.Body.Bytes(), &report); err != nil || report.Created != packageRecords {
		t.Fatalf("first report = %+v %v", report, err)
	}
	second := do(r, http.MethodPost, "/v1/org/import", packageBody, packageDuties, true)
	var again api.ImportReport
	if err := json.Unmarshal(second.Body.Bytes(), &again); err != nil || second.Code != http.StatusOK || again.Created != 0 || again.Unchanged != packageRecords {
		t.Fatalf("second import = %d %s", second.Code, second.Body)
	}

	exported := do(r, http.MethodPost, "/v1/org/export", "", packageDuties, true)
	if exported.Code != http.StatusOK {
		t.Fatalf("export = %d %s", exported.Code, exported.Body)
	}
	var pkg map[string]any
	if err := json.Unmarshal(exported.Body.Bytes(), &pkg); err != nil {
		t.Fatal(err)
	}
	if len(pkg["legalEntities"].([]any)) != 2 || len(pkg["sites"].([]any)) != 1 || len(pkg["teams"].([]any)) != 1 {
		t.Fatalf("export = %s", exported.Body)
	}
	site := pkg["sites"].([]any)[0].(map[string]any)
	if site["legalEntity"] != "USMF" || site["warehouses"].([]any)[0].(map[string]any)["code"] != "DET-MAIN" {
		t.Fatalf("site export = %v", site)
	}

	other := do(r, http.MethodPost, "/v1/org/import", exported.Body.String(), packageDuties, true, tenantTwo...)
	var otherReport api.ImportReport
	if err := json.Unmarshal(other.Body.Bytes(), &otherReport); err != nil || other.Code != http.StatusOK || otherReport.Created != packageRecords {
		t.Fatalf("import into another tenant = %d %s", other.Code, other.Body)
	}
	dry := do(r, http.MethodPost, "/v1/org/import?dryRun=true", packageBody, packageDuties, true, "X-Passage-Tenant", testkit.TenantID(3))
	var dryReport api.ImportReport
	if err := json.Unmarshal(dry.Body.Bytes(), &dryReport); err != nil || dryReport.Created != packageRecords || !dryReport.DryRun {
		t.Fatalf("dry run = %d %s", dry.Code, dry.Body)
	}
	empty := do(r, http.MethodPost, "/v1/org/export", "", packageDuties, true, "X-Passage-Tenant", testkit.TenantID(3))
	if empty.Code != http.StatusOK || len(mustList(t, empty.Body.Bytes(), "legalEntities")) != 0 {
		t.Fatalf("dry run wrote rows: %s", empty.Body)
	}
}

func mustList(t *testing.T, raw []byte, key string) []any {
	t.Helper()
	var pkg map[string]any
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	list, _ := pkg[key].([]any)
	return list
}

// Two imports of one package racing in one tenant create each record once: the per-code
// idempotency key serializes the creates and the later one replays the earlier row.
func TestOrgPackage_Postgres_ConcurrentImportsCreateEachRecordOnce(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, admin, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	r, _ := memRouterFor(store.New(app))
	var wg sync.WaitGroup
	codes := make([]int, 3)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := do(r, http.MethodPost, "/v1/org/import", packageBody, packageDuties, true)
			codes[i] = rec.Code
			if rec.Code != http.StatusOK {
				t.Errorf("import %d = %d %s", i, rec.Code, rec.Body)
			}
		}(i)
	}
	wg.Wait()
	after := do(r, http.MethodPost, "/v1/org/import", packageBody, packageDuties, true)
	var report api.ImportReport
	if err := json.Unmarshal(after.Body.Bytes(), &report); err != nil || report.Created != 0 || report.Unchanged != packageRecords {
		t.Fatalf("import after the race = %d %s (codes %v)", after.Code, after.Body, codes)
	}
}

// Hierarchy versions and fiscal calendars go through the real store: the draft, node replace
// and publish of each version and the year generation of each calendar, per tenant.
func TestOrgPackage_Postgres_VersionsAndCalendarsRoundTrip(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, admin, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	r, _ := memRouterFor(store.New(app))
	first := do(r, http.MethodPost, "/v1/org/import", timeBody, packageDuties, true)
	var report api.ImportReport
	if err := json.Unmarshal(first.Body.Bytes(), &report); err != nil || first.Code != http.StatusOK || report.Created != timeRecords {
		t.Fatalf("first import = %d %s", first.Code, first.Body)
	}
	second := do(r, http.MethodPost, "/v1/org/import", timeBody, packageDuties, true)
	var again api.ImportReport
	if err := json.Unmarshal(second.Body.Bytes(), &again); err != nil || second.Code != http.StatusOK || again.Created != 0 || again.Unchanged != timeRecords || len(again.Differences) != 0 {
		t.Fatalf("second import = %d %s", second.Code, second.Body)
	}
	exported := do(r, http.MethodPost, "/v1/org/export", "", packageDuties, true)
	if exported.Code != http.StatusOK {
		t.Fatalf("export = %d %s", exported.Code, exported.Body)
	}
	types := mustList(t, exported.Body.Bytes(), "hierarchyTypes")
	versions := types[0].(map[string]any)["versions"].([]any)
	if len(versions) != 2 || len(versions[1].(map[string]any)["nodes"].([]any)) != 3 || len(mustList(t, exported.Body.Bytes(), "fiscalCalendars")) != 2 {
		t.Fatalf("export = %s", exported.Body)
	}
	other := do(r, http.MethodPost, "/v1/org/import", exported.Body.String(), packageDuties, true, "X-Passage-Tenant", testkit.TenantID(2))
	var otherReport api.ImportReport
	if err := json.Unmarshal(other.Body.Bytes(), &otherReport); err != nil || other.Code != http.StatusOK || otherReport.Created != timeRecords {
		t.Fatalf("import of the export into another tenant = %d %s", other.Code, other.Body)
	}
}

// pastHistory is timeBody with both versions in the past and a type that allows no backdating.
func pastHistory() string {
	body := strings.Replace(strings.Replace(timeBody, "2099-01-01", "2020-01-01", 1), "2099-07-01", "2021-06-01", 1)
	return strings.Replace(body, `"backdateAllowed":true`, `"backdateAllowed":false`, 1)
}

func hierarchyVersionsOf(t *testing.T, r http.Handler, tenantID string) (string, []map[string]any) {
	t.Helper()
	types := do(r, http.MethodGet, "/v1/org/hierarchy-types", "", "org.read", true, "X-Passage-Tenant", tenantID)
	var page struct{ Items []struct{ ID string } }
	if err := json.Unmarshal(types.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("hierarchy types = %d %s", types.Code, types.Body)
	}
	list := do(r, http.MethodGet, "/v1/org/hierarchy-types/"+page.Items[0].ID+"/versions", "", "org.read", true, "X-Passage-Tenant", tenantID)
	var versions struct{ Items []map[string]any }
	if err := json.Unmarshal(list.Body.Bytes(), &versions); err != nil {
		t.Fatalf("versions = %d %s", list.Code, list.Body)
	}
	return page.Items[0].ID, versions.Items
}

// A past-dated history loads into a fresh tenant without the backdate duty; a run that stopped
// between the draft and the publish of a version is finished by the next run; two runs racing
// publish each version and generate each year once.
func TestOrgPackage_Postgres_VersionHistoryResumeAndRace(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, admin, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	r, _ := memRouterFor(store.New(app))
	body := pastHistory()

	// Tenant 1: the first run published 2020-01-01 and left the 2021-06-01 draft open.
	firstOnly := strings.Replace(body, "2021-06-01", "2099-12-31", 1)
	firstOnly = firstOnly[:strings.Index(firstOnly, `,
   {"effectiveFrom":"2099-12-31"`)] + "]}]," + firstOnly[strings.Index(firstOnly, `"fiscalCalendars"`):]
	if rec := do(r, http.MethodPost, "/v1/org/import", firstOnly, packageDuties, true); rec.Code != http.StatusOK {
		t.Fatalf("first run = %d %s", rec.Code, rec.Body)
	}
	typeID, _ := hierarchyVersionsOf(t, r, testkit.TenantID(1))
	draft := do(r, http.MethodPost, "/v1/org/hierarchy-types/"+typeID+"/versions", "", "org.hierarchy.write", true, "Idempotency-Key", "orgpkg:hier-draft:REPORTING/2021-06-01")
	var open struct{ ID string }
	if err := json.Unmarshal(draft.Body.Bytes(), &open); err != nil || draft.Code != http.StatusCreated {
		t.Fatalf("draft = %d %s", draft.Code, draft.Body)
	}
	rec := do(r, http.MethodPost, "/v1/org/import", body, packageDuties, true)
	var report api.ImportReport
	if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil || rec.Code != http.StatusOK || report.Kinds["hierarchyVersions"] != (api.ImportCounts{Created: 1, Unchanged: 1}) {
		t.Fatalf("re-run = %d %s", rec.Code, rec.Body)
	}
	_, versions := hierarchyVersionsOf(t, r, testkit.TenantID(1))
	for _, v := range versions {
		if v["status"] == "draft" || (v["id"] == open.ID && (v["status"] != "published" || v["effectiveFrom"] != "2021-06-01")) {
			t.Fatalf("after the re-run versions = %v", versions)
		}
	}
	if len(versions) != 2 {
		t.Fatalf("after the re-run versions = %v", versions)
	}

	// Tenant 2: two runs race.
	tenant2 := testkit.TenantID(2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := do(r, http.MethodPost, "/v1/org/import", body, packageDuties, true, "X-Passage-Tenant", tenant2); rec.Code != http.StatusOK {
				t.Errorf("racing import = %d %s", rec.Code, rec.Body)
			}
		}()
	}
	wg.Wait()
	_, versions = hierarchyVersionsOf(t, r, tenant2)
	if len(versions) != 2 || versions[0]["status"] != "published" || versions[1]["status"] != "superseded" || versions[1]["effectiveTo"] != "2021-06-01" {
		t.Fatalf("after the race versions = %v", versions)
	}
	after := do(r, http.MethodPost, "/v1/org/import", body, packageDuties, true, "X-Passage-Tenant", tenant2)
	var again api.ImportReport
	if err := json.Unmarshal(after.Body.Bytes(), &again); err != nil || again.Created != 0 || again.Unchanged != timeRecords {
		t.Fatalf("import after the race = %d %s", after.Code, after.Body)
	}
}
