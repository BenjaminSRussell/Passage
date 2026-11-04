package api_test

import (
	"context"
	"encoding/json"
	"net/http"
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
