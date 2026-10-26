package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	warehousesPath = "/v1/org/warehouses"
	warehouseID1   = "00000000-0000-0000-0001-000000000001"
	siteOtherLE    = "00000000-0000-0000-0000-000000000002"
)

func seededWarehouses() *fakeStore {
	f := seededSites()
	f.warehouses = []store.Warehouse{
		{ID: warehouseID1, LegalEntityID: siteLE1, SiteID: siteID1, Code: "W1", Name: "One", Type: "standard", TimeZone: "UTC", Status: "active", Version: 1},
		{ID: "00000000-0000-0000-0001-000000000002", LegalEntityID: siteLE2, SiteID: siteOtherLE, Code: "W1", Name: "Two", Type: "standard", TimeZone: "UTC", Status: "active", Version: 1},
	}
	return f
}

func warehouseBody(siteID, code string) string {
	return `{"siteId":"` + siteID + `","code":"` + code + `","name":"Dock"}`
}

func TestWarehouses(t *testing.T) {
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
		{"create upper-cases code and copies legal entity", seededSites(), http.MethodPost, warehousesPath, warehouseBody(siteID1, "w1"), siteDuties, true, http.StatusCreated, `"code":"W1","name":"Dock","type":"standard"`},
		{"create takes the site legal entity", seededSites(), http.MethodPost, warehousesPath, warehouseBody(siteID1, "w1"), siteDuties, true, http.StatusCreated, `"legalEntityId":"` + siteLE1 + `"`},
		{"create legalEntityId rejected", seededSites(), http.MethodPost, warehousesPath, `{"legalEntityId":"` + siteLE1 + `","siteId":"` + siteID1 + `","code":"W1","name":"x"}`, siteDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create missing site", seededSites(), http.MethodPost, warehousesPath, `{"code":"W1","name":"x"}`, siteDuties, true, http.StatusBadRequest, `"siteId"`},
		{"create malformed site", seededSites(), http.MethodPost, warehousesPath, warehouseBody("x", "W1"), siteDuties, true, http.StatusBadRequest, `"siteId"`},
		{"create bad type", seededSites(), http.MethodPost, warehousesPath, `{"siteId":"` + siteID1 + `","code":"W1","name":"x","type":"cold"}`, siteDuties, true, http.StatusBadRequest, `"type"`},
		{"create bad code", seededSites(), http.MethodPost, warehousesPath, warehouseBody(siteID1, "w 1"), siteDuties, true, http.StatusBadRequest, `"code"`},
		{"create unknown site", &fakeStore{}, http.MethodPost, warehousesPath, warehouseBody(siteID1, "W1"), siteDuties, true, http.StatusUnprocessableEntity, "ORG-WH-SITE-INVALID"},
		{"create duplicate code", seededWarehouses(), http.MethodPost, warehousesPath, warehouseBody(siteID1, "W1"), siteDuties, true, http.StatusConflict, "ORG-WH-CODE-DUPLICATE"},
		{"create without duty", seededSites(), http.MethodPost, warehousesPath, `{}`, "org.read", true, http.StatusForbidden, "AUTH-0003"},
		{"create without tenant", seededSites(), http.MethodPost, warehousesPath, `{}`, "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"get ok", seededWarehouses(), http.MethodGet, warehousesPath + "/" + warehouseID1, "", siteDuties, true, http.StatusOK, `"code":"W1"`},
		{"get unknown", seededWarehouses(), http.MethodGet, warehousesPath + "/00000000-0000-0000-0001-0000000000ff", "", siteDuties, true, http.StatusNotFound, "ORG-WH-NOT-FOUND"},
		{"get malformed", seededWarehouses(), http.MethodGet, warehousesPath + "/nope", "", siteDuties, true, http.StatusNotFound, "ORG-WH-NOT-FOUND"},
		{"list bad type", seededWarehouses(), http.MethodGet, warehousesPath + "?type=cold", "", siteDuties, true, http.StatusBadRequest, `"type"`},
		{"list bad site", seededWarehouses(), http.MethodGet, warehousesPath + "?siteId=x", "", siteDuties, true, http.StatusBadRequest, `"siteId"`},
		{"list bad status", seededWarehouses(), http.MethodGet, warehousesPath + "?status=deleted", "", siteDuties, true, http.StatusBadRequest, `"status"`},
		{"list bad cursor", seededWarehouses(), http.MethodGet, warehousesPath + "?cursor=Zm9v", "", siteDuties, true, http.StatusBadRequest, `"cursor"`},
		{"patch name", seededWarehouses(), http.MethodPatch, warehousesPath + "/" + warehouseID1, `{"version":1,"name":"Renamed"}`, siteDuties, true, http.StatusOK, `"version":2`},
		{"patch code rejected", seededWarehouses(), http.MethodPatch, warehousesPath + "/" + warehouseID1, `{"version":1,"code":"X1"}`, siteDuties, true, http.StatusBadRequest, "COM-0001"},
		{"patch bad address id", seededWarehouses(), http.MethodPatch, warehousesPath + "/" + warehouseID1, `{"version":1,"addressId":"abc"}`, siteDuties, true, http.StatusBadRequest, `"addressId"`},
		{"patch clearing address id accepted", seededWarehouses(), http.MethodPatch, warehousesPath + "/" + warehouseID1, `{"version":1,"addressId":""}`, siteDuties, true, http.StatusOK, `"version":1`},
		{"patch missing version", seededWarehouses(), http.MethodPatch, warehousesPath + "/" + warehouseID1, `{"name":"x"}`, siteDuties, true, http.StatusBadRequest, `"version"`},
		{"patch malformed site", seededWarehouses(), http.MethodPatch, warehousesPath + "/" + warehouseID1, `{"version":1,"siteId":"x"}`, siteDuties, true, http.StatusBadRequest, `"siteId"`},
		{"patch site in another legal entity", seededWarehouses(), http.MethodPatch, warehousesPath + "/" + warehouseID1, `{"version":1,"siteId":"` + siteOtherLE + `"}`, siteDuties, true, http.StatusUnprocessableEntity, "ORG-WH-LE-MISMATCH"},
		{"create uppercase site id", seededSites(), http.MethodPost, warehousesPath, warehouseBody(strings.ToUpper(siteID1), "w1"), siteDuties, true, http.StatusCreated, `"siteId":"` + siteID1 + `"`},
		{"patch uppercase site in another legal entity", seededWarehouses(), http.MethodPatch, warehousesPath + "/" + warehouseID1, `{"version":1,"siteId":"` + strings.ToUpper(siteOtherLE) + `"}`, siteDuties, true, http.StatusUnprocessableEntity, "ORG-WH-LE-MISMATCH"},
		{"patch without duty", seededWarehouses(), http.MethodPatch, warehousesPath + "/" + warehouseID1, `{"version":1}`, "org.read", true, http.StatusForbidden, "AUTH-0003"},
		{"patch deactivate default", defaultSeeded(), http.MethodPatch, warehousesPath + "/" + warehouseID1, `{"version":1,"status":"inactive"}`, siteDuties, true, http.StatusUnprocessableEntity, "ORG-WH-IS-DEFAULT"},
		{"patch move default", defaultSeeded(), http.MethodPatch, warehousesPath + "/" + warehouseID1, `{"version":1,"siteId":"` + siteID1[:35] + `9"}`, siteDuties, true, http.StatusUnprocessableEntity, "ORG-WH-IS-DEFAULT"},
		{"patch deactivate non-default", seededWarehouses(), http.MethodPatch, warehousesPath + "/" + warehouseID1, `{"version":1,"status":"inactive"}`, siteDuties, true, http.StatusOK, `"code":"W1"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("%s = %d %s, want %d containing %q", tc.name, rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
			if strings.HasSuffix(tc.path, "/nope") && tc.store.warehouseCalls != 0 {
				t.Fatalf("malformed id reached the store")
			}
		})
	}
}

func TestWarehouses_ListWalksEveryPageOnce(t *testing.T) {
	r, _ := newRouter(seededWarehouses())
	seen := map[string]bool{}
	path := warehousesPath + "?limit=1"
	for pages := 0; pages < 3; pages++ {
		rec := do(r, http.MethodGet, path, "", siteDuties, true)
		var body struct {
			Items      []store.Warehouse `json:"items"`
			NextCursor string            `json:"nextCursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || len(body.Items) != 1 {
			t.Fatalf("page %d = %d %s", pages, rec.Code, rec.Body)
		}
		seen[body.Items[0].ID] = true
		if body.NextCursor == "" {
			break
		}
		path = warehousesPath + "?limit=1&cursor=" + body.NextCursor
	}
	if len(seen) != 2 {
		t.Fatalf("walk saw %v; want both warehouses once", seen)
	}
}

func TestWarehouses_MetricsCountCreatesAndVersionBumps(t *testing.T) {
	r, a := newRouter(seededSites())
	created := do(r, http.MethodPost, warehousesPath, warehouseBody(siteID1, "W1"), siteDuties, true)
	var warehouse store.Warehouse
	if err := json.Unmarshal(created.Body.Bytes(), &warehouse); err != nil {
		t.Fatal(err)
	}
	do(r, http.MethodPatch, warehousesPath+"/"+warehouse.ID, `{"version":1,"name":"Renamed"}`, siteDuties, true)
	do(r, http.MethodPatch, warehousesPath+"/"+warehouse.ID, `{"version":2,"name":"Renamed"}`, siteDuties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"org_warehouses_created_total 1", "org_warehouses_updated_total 1"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body.String())
		}
	}
}
