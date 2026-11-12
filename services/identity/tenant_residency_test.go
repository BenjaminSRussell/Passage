package identity_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/cloudprofile"
	"github.com/BenjaminSRussell/passage/services/identity"
)

type residentTenant struct {
	TenantID  string `json:"tenantId"`
	Residency string `json:"residency"`
	Region    string `json:"region"`
}

func TestTenantResidencyIsCheckedAtProvisioning(t *testing.T) {
	f := newAdminFixture(t)
	create := func(body map[string]string) *httptest.ResponseRecorder {
		return f.do(f.operatorCaller(), http.MethodPost, "/v1/tenants", body)
	}
	count := func(realm string) int {
		var n int
		if err := f.admin.QueryRow(context.Background(), `select count(*) from identity_tenants where realm_name = $1`, realm).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("an EU tenant is placed in an EU region, named either way", func(t *testing.T) {
		for i, region := range []string{"westeurope", "eu-west", "SwedenCentral"} {
			realm := []string{"eu-one", "eu-two", "eu-three"}[i]
			got := decode[residentTenant](t, create(map[string]string{"realmName": realm, "displayName": "EU", "residency": "EU", "region": region}), http.StatusCreated)
			if got.Residency != "EU" || got.Region == "" {
				t.Fatalf("created = %+v", got)
			}
			read := decode[residentTenant](t, f.do(f.operatorCaller(), http.MethodGet, "/v1/tenants/"+got.TenantID, nil), http.StatusOK)
			if read != got {
				t.Fatalf("get = %+v, want %+v", read, got)
			}
		}
	})

	t.Run("an EU tenant is refused a region outside the EU and nothing is created", func(t *testing.T) {
		for i, region := range []string{"eastus2", "us-east", "uksouth"} {
			realm := []string{"eu-bad-one", "eu-bad-two", "eu-bad-three"}[i]
			wantProblem(t, create(map[string]string{"realmName": realm, "displayName": "EU", "residency": "EU", "region": region}), http.StatusUnprocessableEntity, "OPS-1705")
			if n := count(realm); n != 0 {
				t.Fatalf("%s: %d tenant rows after a refused placement", region, n)
			}
		}
	})

	t.Run("a US tenant is refused an EU region and the default residency allows any", func(t *testing.T) {
		wantProblem(t, create(map[string]string{"realmName": "us-bad", "displayName": "US", "residency": "US", "region": "westeurope"}), http.StatusUnprocessableEntity, "OPS-1705")
		got := decode[residentTenant](t, create(map[string]string{"realmName": "any-co", "displayName": "Any", "region": "westeurope"}), http.StatusCreated)
		if got.Residency != "GLOBAL" || got.Region != "westeurope" {
			t.Fatalf("created = %+v, want GLOBAL in westeurope", got)
		}
	})

	t.Run("a restricted residency needs a region, so no tenant is left unplaced", func(t *testing.T) {
		p := wantProblem(t, create(map[string]string{"realmName": "no-region", "displayName": "None", "residency": "eu"}), http.StatusBadRequest, "COM-0001")
		if p.Errors["region"] == "" || count("no-region") != 0 {
			t.Fatalf("errors = %v", p.Errors)
		}
		global := decode[residentTenant](t, create(map[string]string{"realmName": "global-later", "displayName": "Later"}), http.StatusCreated)
		if global.Residency != "GLOBAL" || global.Region != "" {
			t.Fatalf("created = %+v, want GLOBAL with no region yet", global)
		}
	})

	t.Run("under the eu-data-boundary overlay the default residency is EU and regions follow the profile", func(t *testing.T) {
		profile, err := cloudprofile.FromEnv(func(k string) string {
			return map[string]string{"PASSAGE_CLOUD_PROFILE": "commercial+eu-data-boundary"}[k]
		})
		if err != nil {
			t.Fatal(err)
		}
		saved := f.router
		t.Cleanup(func() { f.router = saved })
		r := chi.NewRouter()
		identity.NewTenantsAPI(f.app, f.operator).WithCloudProfile(profile).Register(r)
		f.router = r

		got := decode[residentTenant](t, create(map[string]string{"realmName": "eudb-default", "displayName": "EU", "region": "westeurope"}), http.StatusCreated)
		if got.Residency != "EU" {
			t.Fatalf("created = %+v, want EU by default under the overlay", got)
		}
		wantProblem(t, create(map[string]string{"realmName": "eudb-us", "displayName": "US", "region": "eastus2"}), http.StatusUnprocessableEntity, "OPS-1705")
		// GLOBAL allows any region by residency, but the deployment's profile still pins the regions.
		wantProblem(t, create(map[string]string{"realmName": "eudb-global-us", "displayName": "G", "residency": "GLOBAL", "region": "eastus2"}), http.StatusUnprocessableEntity, "OPS-1705")
		wantProblem(t, create(map[string]string{"realmName": "eudb-legacy", "displayName": "L", "region": "eu-west"}), http.StatusUnprocessableEntity, "OPS-1705")
		if n := count("eudb-us") + count("eudb-global-us") + count("eudb-legacy"); n != 0 {
			t.Fatalf("%d tenant rows after refused placements", n)
		}
	})

	t.Run("an unknown residency is a field error", func(t *testing.T) {
		p := wantProblem(t, create(map[string]string{"realmName": "odd-co", "displayName": "Odd", "residency": "ATLANTIS"}), http.StatusBadRequest, "COM-0001")
		if p.Errors["residency"] == "" {
			t.Fatalf("errors = %v", p.Errors)
		}
	})
}
