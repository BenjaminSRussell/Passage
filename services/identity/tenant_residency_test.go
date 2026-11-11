package identity_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
		none := decode[residentTenant](t, create(map[string]string{"realmName": "no-region", "displayName": "None", "residency": "eu"}), http.StatusCreated)
		if none.Residency != "EU" || none.Region != "" {
			t.Fatalf("created = %+v, want EU with no region yet", none)
		}
	})

	t.Run("an unknown residency is a field error", func(t *testing.T) {
		p := wantProblem(t, create(map[string]string{"realmName": "odd-co", "displayName": "Odd", "residency": "ATLANTIS"}), http.StatusBadRequest, "COM-0001")
		if p.Errors["residency"] == "" {
			t.Fatalf("errors = %v", p.Errors)
		}
	})
}
