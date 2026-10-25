package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/org/internal/domain"
)

const (
	addressesPath = "/v1/org/addresses"
	addressDuties = "org.read,org.address.write"
	ownerUUID     = "00000000-0000-0000-0004-000000000001"
)

type stubGeocoder struct {
	result domain.Geocode
	err    error
}

func (g stubGeocoder) Geocode(context.Context, domain.PostalAddress) (domain.Geocode, error) {
	return g.result, g.err
}

func addressBody(overrides ...string) string {
	fields := map[string]string{
		"ownerType": `"warehouse"`, "ownerId": `"` + ownerUUID + `"`, "countryCode": `"us"`, "regionCode": `"ca"`, "city": `"San Francisco"`,
		"postalCode": `"94105"`, "street1": `"1 Main St"`, "purposes": `[{"purpose":"business","primary":true}]`,
	}
	for i := 0; i+1 < len(overrides); i += 2 {
		if overrides[i+1] == "" {
			delete(fields, overrides[i])
		} else {
			fields[overrides[i]] = overrides[i+1]
		}
	}
	parts := make([]string, 0, len(fields))
	for k, v := range fields {
		parts = append(parts, `"`+k+`":`+v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestAddresses(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		duties     string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"create upper-cases codes and stays unverified", addressBody(), addressDuties, true, http.StatusCreated, `"countryCode":"US","regionCode":"CA"`},
		{"create geoStatus unverified", addressBody(), addressDuties, true, http.StatusCreated, `"geoStatus":"unverified"`},
		{"unknown ownerType", addressBody("ownerType", `"planet"`), addressDuties, true, http.StatusBadRequest, `"ownerType"`},
		{"malformed ownerId", addressBody("ownerId", `"nope"`), addressDuties, true, http.StatusBadRequest, `"ownerId"`},
		{"missing street1", addressBody("street1", ""), addressDuties, true, http.StatusBadRequest, `"street1"`},
		{"61-character city", addressBody("city", `"`+strings.Repeat("c", 61)+`"`), addressDuties, true, http.StatusBadRequest, `"city"`},
		{"validFrom 2026-13-01", addressBody("validFrom", `"2026-13-01"`), addressDuties, true, http.StatusBadRequest, `"validFrom"`},
		{"empty purposes", addressBody("purposes", `[]`), addressDuties, true, http.StatusBadRequest, `"purposes"`},
		{"purpose billing", addressBody("purposes", `[{"purpose":"billing"}]`), addressDuties, true, http.StatusBadRequest, `"purposes"`},
		{"duplicated purpose", addressBody("purposes", `[{"purpose":"invoice"},{"purpose":"invoice"}]`), addressDuties, true, http.StatusBadRequest, `"purposes"`},
		{"bad regionCode", addressBody("regionCode", `"toolong"`), addressDuties, true, http.StatusBadRequest, `"regionCode"`},
		{"unknown body field", addressBody("surprise", `1`), addressDuties, true, http.StatusBadRequest, "COM-0001"},
		{"without duty", addressBody(), "org.read", true, http.StatusForbidden, "AUTH-0003"},
		{"without tenant", addressBody(), "", false, http.StatusUnauthorized, "AUTH-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			r, _ := newRouter(f)
			rec := do(r, http.MethodPost, addressesPath, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("%s = %d %s, want %d containing %q", tc.name, rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus != http.StatusCreated && len(f.addresses) != 0 {
				t.Fatalf("rejected request reached the store")
			}
		})
	}
}

func TestAddresses_Geocoder(t *testing.T) {
	tests := []struct {
		name       string
		geocoder   stubGeocoder
		wantStatus string
		wantBody   string
	}{
		{"found sets coordinates", stubGeocoder{result: domain.Geocode{Latitude: decimal.RequireFromString("37.789"), Longitude: decimal.RequireFromString("-122.394"), Found: true}}, "geocoded", `"latitude":"37.789","longitude":"-122.394"`},
		{"error stays unverified", stubGeocoder{err: errors.New("down")}, "unverified", `"geoStatus":"unverified"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, a := newRouter(&fakeStore{})
			a.SetGeocoder(tc.geocoder)
			rec := do(r, http.MethodPost, addressesPath, addressBody(), addressDuties, true)
			if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"geoStatus":"`+tc.wantStatus+`"`) || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("%s = %d %s", tc.name, rec.Code, rec.Body)
			}
		})
	}
}

func TestAddresses_Get(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	created := do(r, http.MethodPost, addressesPath, addressBody(), addressDuties, true)
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	for _, tc := range []struct {
		name, path string
		status     int
		body       string
	}{
		{"ok", addressesPath + "/00000000-0000-0000-0003-000000000001", http.StatusOK, `"ownerType":"warehouse"`},
		{"unknown", addressesPath + "/00000000-0000-0000-0003-0000000000ff", http.StatusNotFound, "ORG-ADDR-NOT-FOUND"},
		{"malformed", addressesPath + "/nope", http.StatusNotFound, "ORG-ADDR-NOT-FOUND"},
	} {
		before := f.addressCalls
		rec := do(r, http.MethodGet, tc.path, "", addressDuties, true)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.body) {
			t.Fatalf("%s = %d %s", tc.name, rec.Code, rec.Body)
		}
		if tc.name == "malformed" && f.addressCalls != before {
			t.Fatalf("malformed id reached the store")
		}
	}
	if rec := do(r, http.MethodGet, addressesPath+"/00000000-0000-0000-0003-000000000001", "", "", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("get without tenant = %d", rec.Code)
	}
	if rec := do(r, http.MethodGet, addressesPath+"/00000000-0000-0000-0003-000000000001", "", "org.site.write", true); rec.Code != http.StatusForbidden {
		t.Fatalf("get without org.read = %d", rec.Code)
	}
}

func TestAddresses_MetricsCountCreates(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	do(r, http.MethodPost, addressesPath, addressBody(), addressDuties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "org_addresses_created_total 1") {
		t.Fatalf("metrics missing the address counter in %s", rec.Body.String())
	}
}
