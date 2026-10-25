package api_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	locationsPath = "/v1/org/locations"
	locationID1   = "00000000-0000-0000-0002-000000000001"
)

func seededLocations() *fakeStore {
	f := seededWarehouses()
	f.locations = []store.Location{
		{ID: locationID1, LegalEntityID: siteLE1, WarehouseID: warehouseID1, Code: "L1", Name: "One", Type: "storage", Status: "active", Version: 1},
		{ID: "00000000-0000-0000-0002-000000000002", LegalEntityID: siteLE1, WarehouseID: warehouseID1, Code: "L2", Name: "Two", Type: "receiving", Status: "active", Version: 1},
	}
	return f
}

func locationBody(warehouseID, code string) string {
	return `{"warehouseId":"` + warehouseID + `","code":"` + code + `","name":"Bin"}`
}

func TestLocations(t *testing.T) {
	tests := []struct {
		name       string
		store      *fakeStore
		method     string
		path       string
		body       string
		duties     string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"create upper-cases code, defaults type, copies legal entity", seededWarehouses(), http.MethodPost, locationsPath, locationBody(warehouseID1, "a01.r02"), siteDuties, true, http.StatusCreated, `"legalEntityId":"` + siteLE1 + `","warehouseId":"` + warehouseID1 + `","code":"A01.R02","name":"Bin","type":"storage"`},
		{"create legalEntityId rejected", seededWarehouses(), http.MethodPost, locationsPath, `{"legalEntityId":"` + siteLE1 + `","warehouseId":"` + warehouseID1 + `","code":"L1","name":"x"}`, siteDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create missing warehouse", seededWarehouses(), http.MethodPost, locationsPath, `{"code":"L1","name":"x"}`, siteDuties, true, http.StatusBadRequest, `"warehouseId"`},
		{"create bad type", seededWarehouses(), http.MethodPost, locationsPath, `{"warehouseId":"` + warehouseID1 + `","code":"L1","name":"x","type":"picking"}`, siteDuties, true, http.StatusBadRequest, `"type"`},
		{"create 21-character code", seededWarehouses(), http.MethodPost, locationsPath, locationBody(warehouseID1, strings.Repeat("A", 21)), siteDuties, true, http.StatusBadRequest, `"code"`},
		{"create unknown warehouse", &fakeStore{}, http.MethodPost, locationsPath, locationBody(warehouseID1, "L1"), siteDuties, true, http.StatusUnprocessableEntity, "ORG-LOC-WAREHOUSE-INVALID"},
		{"create duplicate code", seededLocations(), http.MethodPost, locationsPath, locationBody(warehouseID1, "L1"), siteDuties, true, http.StatusConflict, "ORG-LOC-CODE-DUPLICATE"},
		{"create without duty", seededWarehouses(), http.MethodPost, locationsPath, `{}`, "org.read", true, http.StatusForbidden, "AUTH-0003"},
		{"create without tenant", seededWarehouses(), http.MethodPost, locationsPath, `{}`, "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"get ok", seededLocations(), http.MethodGet, locationsPath + "/" + locationID1, "", siteDuties, true, http.StatusOK, `"code":"L1"`},
		{"get unknown", seededLocations(), http.MethodGet, locationsPath + "/00000000-0000-0000-0002-0000000000ff", "", siteDuties, true, http.StatusNotFound, "ORG-LOC-NOT-FOUND"},
		{"get malformed", seededLocations(), http.MethodGet, locationsPath + "/nope", "", siteDuties, true, http.StatusNotFound, "ORG-LOC-NOT-FOUND"},
		{"list bad type", seededLocations(), http.MethodGet, locationsPath + "?type=picking", "", siteDuties, true, http.StatusBadRequest, `"type"`},
		{"list bad cursor", seededLocations(), http.MethodGet, locationsPath + "?cursor=Zm9v", "", siteDuties, true, http.StatusBadRequest, `"cursor"`},
		{"list without tenant", seededLocations(), http.MethodGet, locationsPath, "", "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"site deactivation refused while a warehouse is active", seededWarehouses(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"status":"inactive"}`, siteDuties, true, http.StatusConflict, "ORG-SITE-IN-USE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("%s = %d %s, want %d containing %q", tc.name, rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
			if strings.HasSuffix(tc.path, "/nope") && tc.store.locationCalls != 0 {
				t.Fatalf("malformed id reached the store")
			}
		})
	}
}

func TestLocations_MetricsCountCreates(t *testing.T) {
	r, a := newRouter(seededWarehouses())
	do(r, http.MethodPost, locationsPath, locationBody(warehouseID1, "L1"), siteDuties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "org_locations_created_total 1") {
		t.Fatalf("metrics missing the location counter in %s", rec.Body.String())
	}
}
