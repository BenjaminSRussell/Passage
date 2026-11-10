package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/api"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
	"github.com/BenjaminSRussell/passage/services/org/migrations"
)

func TestDemoCompany_Postgres_LoadsOnceAndIsRepeatable(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	if err := store.SeedReference(ctx, admin, migrations.Reference); err != nil {
		t.Fatal(err)
	}
	r, _ := memRouterFor(store.New(app))
	call := func(duties string) (int, api.DemoCompanyResult) {
		rec := doWithoutTenantAdmin(r, http.MethodPost, "/v1/org/demo-company", "", duties, true)
		var out api.DemoCompanyResult
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}

	if code, _ := call("org.demo-company.manage"); code != http.StatusForbidden {
		t.Fatalf("without tenant.administer got %d, want 403", code)
	}
	if code, _ := call("org.read,tenant.administer"); code != http.StatusForbidden {
		t.Fatalf("without the duty got %d, want 403", code)
	}
	code, first := call("org.demo-company.manage,tenant.administer")
	if code != http.StatusOK || first.Created == 0 || first.Package != "demo-manufacturer" {
		t.Fatalf("first load = %d %+v", code, first)
	}
	code, second := call("org.demo-company.manage,tenant.administer")
	if code != http.StatusOK || second.Created != 0 || second.Present != first.Created+first.Present {
		t.Fatalf("second load = %d %+v, want everything present and nothing created", code, second)
	}
}
