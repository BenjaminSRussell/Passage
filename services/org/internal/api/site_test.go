package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	sitesPath  = "/v1/org/sites"
	siteDuties = "org.read,org.site.write"
	siteLE1    = "00000000-0000-0000-0000-0000000000a1"
	siteLE2    = "00000000-0000-0000-0000-0000000000a2"
	siteID1    = "00000000-0000-0000-0000-000000000001"
)

func seededSites() *fakeStore {
	return &fakeStore{sites: []store.Site{
		{ID: siteID1, LegalEntityID: siteLE1, Code: "S1", Name: "One", TimeZone: "UTC", Status: "active", Version: 1},
		{ID: "00000000-0000-0000-0000-000000000002", LegalEntityID: siteLE2, Code: "S1", Name: "Two", TimeZone: "UTC", Status: "active", Version: 1},
	}}
}

func siteBody(legalEntityID, code string) string {
	return `{"legalEntityId":"` + legalEntityID + `","code":"` + code + `","name":"Plant"}`
}

func TestSites(t *testing.T) {
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
		{"create upper-cases code", &fakeStore{}, http.MethodPost, sitesPath, siteBody(siteLE1, "s1"), siteDuties, true, http.StatusCreated, `"code":"S1"`},
		{"create same code other legal entity", seededSites(), http.MethodPost, sitesPath, siteBody("00000000-0000-0000-0000-0000000000a3", "s1"), siteDuties, true, http.StatusCreated, `"code":"S1"`},
		{"create duplicate within legal entity", seededSites(), http.MethodPost, sitesPath, siteBody(siteLE1, "S1"), siteDuties, true, http.StatusConflict, "ORG-SITE-CODE-DUPLICATE"},
		{"create missing legal entity", &fakeStore{}, http.MethodPost, sitesPath, `{"code":"S1","name":"x"}`, siteDuties, true, http.StatusBadRequest, `"legalEntityId"`},
		{"create malformed legal entity", &fakeStore{}, http.MethodPost, sitesPath, siteBody("x", "S1"), siteDuties, true, http.StatusBadRequest, `"legalEntityId"`},
		{"create 11 character code", &fakeStore{}, http.MethodPost, sitesPath, siteBody(siteLE1, "ABCDEFGHIJK"), siteDuties, true, http.StatusBadRequest, `"code"`},
		{"create blank name", &fakeStore{}, http.MethodPost, sitesPath, `{"legalEntityId":"` + siteLE1 + `","code":"S1","name":"  "}`, siteDuties, true, http.StatusBadRequest, `"name"`},
		{"create unknown time zone", &fakeStore{}, http.MethodPost, sitesPath, `{"legalEntityId":"` + siteLE1 + `","code":"S1","name":"x","timeZone":"Mars/Base"}`, siteDuties, true, http.StatusBadRequest, `"timeZone"`},
		{"create unknown field", &fakeStore{}, http.MethodPost, sitesPath, `{"legalEntityId":"` + siteLE1 + `","code":"S1","name":"x","x":1}`, siteDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create without duty", &fakeStore{}, http.MethodPost, sitesPath, `{}`, "org.read", true, http.StatusForbidden, "AUTH-0003"},
		{"create without tenant", &fakeStore{}, http.MethodPost, sitesPath, `{}`, "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"get ok", seededSites(), http.MethodGet, sitesPath + "/" + siteID1, "", siteDuties, true, http.StatusOK, `"code":"S1"`},
		{"get unknown", seededSites(), http.MethodGet, sitesPath + "/00000000-0000-0000-0000-0000000000ff", "", siteDuties, true, http.StatusNotFound, "ORG-SITE-NOT-FOUND"},
		{"get malformed", seededSites(), http.MethodGet, sitesPath + "/nope", "", siteDuties, true, http.StatusNotFound, "ORG-SITE-NOT-FOUND"},
		{"get without tenant", seededSites(), http.MethodGet, sitesPath + "/" + siteID1, "", "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"list bad status", seededSites(), http.MethodGet, sitesPath + "?status=deleted", "", siteDuties, true, http.StatusBadRequest, `"status"`},
		{"list bad legal entity", seededSites(), http.MethodGet, sitesPath + "?legalEntityId=x", "", siteDuties, true, http.StatusBadRequest, `"legalEntityId"`},
		{"list cursor without slash", seededSites(), http.MethodGet, sitesPath + "?cursor=Zm9v", "", siteDuties, true, http.StatusBadRequest, `"cursor"`},
		{"list cursor with bad id", seededSites(), http.MethodGet, sitesPath + "?cursor=UzEveA", "", siteDuties, true, http.StatusBadRequest, `"cursor"`},
		{"list without duty", seededSites(), http.MethodGet, sitesPath, "", "", true, http.StatusForbidden, "AUTH-0003"},
		{"patch name", seededSites(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"name":"Renamed"}`, siteDuties, true, http.StatusOK, `"version":2`},
		{"patch code rejected", seededSites(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"code":"X1"}`, siteDuties, true, http.StatusBadRequest, "COM-0001"},
		{"patch legal entity rejected", seededSites(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"legalEntityId":"` + siteLE2 + `"}`, siteDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create bad calendar id", &fakeStore{}, http.MethodPost, sitesPath, `{"legalEntityId":"` + siteLE1 + `","code":"S1","name":"x","fiscalCalendarId":"abc"}`, siteDuties, true, http.StatusBadRequest, `"fiscalCalendarId"`},
		{"create empty calendar id", &fakeStore{}, http.MethodPost, sitesPath, `{"legalEntityId":"` + siteLE1 + `","code":"S1","name":"x","fiscalCalendarId":""}`, siteDuties, true, http.StatusBadRequest, `"fiscalCalendarId"`},
		{"create numeric calendar id", &fakeStore{}, http.MethodPost, sitesPath, `{"legalEntityId":"` + siteLE1 + `","code":"S1","name":"x","fiscalCalendarId":123}`, siteDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create lower-cases calendar id", &fakeStore{}, http.MethodPost, sitesPath, `{"legalEntityId":"` + siteLE1 + `","code":"S1","name":"x","fiscalCalendarId":"0190B6C0-0000-7000-8000-0000000000C1"}`, siteDuties, true, http.StatusCreated, `"fiscalCalendarId":"0190b6c0-0000-7000-8000-0000000000c1"`},
		{"patch bad calendar id", seededSites(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"fiscalCalendarId":"abc"}`, siteDuties, true, http.StatusBadRequest, `"fiscalCalendarId"`},
		{"patch clearing calendar id accepted", seededSites(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"fiscalCalendarId":""}`, siteDuties, true, http.StatusOK, `"version":1`},
		{"patch bad address id", seededSites(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"addressId":"abc"}`, siteDuties, true, http.StatusBadRequest, `"addressId"`},
		{"patch clearing address id accepted", seededSites(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"addressId":""}`, siteDuties, true, http.StatusOK, `"version":1`},
		{"patch missing version", seededSites(), http.MethodPatch, sitesPath + "/" + siteID1, `{"name":"x"}`, siteDuties, true, http.StatusBadRequest, `"version"`},
		{"patch bad status", seededSites(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"status":"deleted"}`, siteDuties, true, http.StatusBadRequest, `"status"`},
		{"patch stale version", seededSites(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":7,"name":"x"}`, siteDuties, true, http.StatusConflict, "ORG-VERSION-CONFLICT"},
		{"patch malformed id", seededSites(), http.MethodPatch, sitesPath + "/nope", `{"version":1}`, siteDuties, true, http.StatusNotFound, "ORG-SITE-NOT-FOUND"},
		{"patch without duty", seededSites(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1}`, "org.read", true, http.StatusForbidden, "AUTH-0003"},
		{"create with default warehouse", &fakeStore{}, http.MethodPost, sitesPath, withDefault(`{"code":"main","name":"Main"}`), siteDuties, true, http.StatusCreated, `"defaultWarehouseId":"00000000-0000-0000-0001-000000000001"`},
		{"create with default warehouse duplicate code", seededWarehouses(), http.MethodPost, sitesPath, strings.Replace(withDefault(`{"code":"w1","name":"Main"}`), `"S1"`, `"S9"`, 1), siteDuties, true, http.StatusConflict, "ORG-WH-CODE-DUPLICATE"},
		{"create default warehouse bad code", &fakeStore{}, http.MethodPost, sitesPath, withDefault(`{"code":"bad code","name":"Main"}`), siteDuties, true, http.StatusBadRequest, `"defaultWarehouse.code"`},
		{"create default warehouse empty object", &fakeStore{}, http.MethodPost, sitesPath, withDefault(`{}`), siteDuties, true, http.StatusBadRequest, `"defaultWarehouse.name"`},
		{"create default warehouse long name", &fakeStore{}, http.MethodPost, sitesPath, withDefault(`{"code":"M","name":"` + strings.Repeat("x", 121) + `"}`), siteDuties, true, http.StatusBadRequest, `"defaultWarehouse.name"`},
		{"create default warehouse bad type", &fakeStore{}, http.MethodPost, sitesPath, withDefault(`{"code":"M","name":"Main","type":"bulk"}`), siteDuties, true, http.StatusBadRequest, `"defaultWarehouse.type"`},
		{"create default warehouse bad time zone", &fakeStore{}, http.MethodPost, sitesPath, withDefault(`{"code":"M","name":"Main","timeZone":"Mars/Base"}`), siteDuties, true, http.StatusBadRequest, `"defaultWarehouse.timeZone"`},
		{"create default warehouse not an object", &fakeStore{}, http.MethodPost, sitesPath, withDefault(`"MAIN"`), siteDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create default warehouse unknown field", &fakeStore{}, http.MethodPost, sitesPath, withDefault(`{"code":"M","name":"Main","x":1}`), siteDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create null default warehouse", &fakeStore{}, http.MethodPost, sitesPath, withDefault(`null`), siteDuties, true, http.StatusCreated, `"code":"S1"`},
		{"patch default warehouse bad id", seededSites(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"defaultWarehouseId":"abc"}`, siteDuties, true, http.StatusBadRequest, `"defaultWarehouseId"`},
		{"patch default warehouse unknown", seededSites(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"defaultWarehouseId":"` + warehouseID1 + `"}`, siteDuties, true, http.StatusUnprocessableEntity, "ORG-SITE-DEFAULT-WAREHOUSE-INVALID"},
		{"patch default warehouse set", seededWarehouses(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"defaultWarehouseId":"` + strings.ToUpper(warehouseID1) + `"}`, siteDuties, true, http.StatusOK, `"defaultWarehouseId":"` + warehouseID1 + `"`},
		{"patch default warehouse clear", defaultSeeded(), http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"defaultWarehouseId":""}`, siteDuties, true, http.StatusOK, `"version":2`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("%s = %d %s, want %d containing %q", tc.name, rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
			if strings.HasSuffix(tc.path, "/nope") && tc.store.siteCalls != 0 {
				t.Fatalf("malformed id reached the store")
			}
		})
	}
}

func TestSites_ListWalksEveryPageOnce(t *testing.T) {
	r, _ := newRouter(seededSites())
	seen := map[string]bool{}
	path := sitesPath + "?limit=1"
	for pages := 0; pages < 3; pages++ {
		rec := do(r, http.MethodGet, path, "", siteDuties, true)
		var body struct {
			Items      []store.Site `json:"items"`
			NextCursor string       `json:"nextCursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || len(body.Items) != 1 {
			t.Fatalf("page %d = %d %s", pages, rec.Code, rec.Body)
		}
		seen[body.Items[0].ID] = true
		if body.NextCursor == "" {
			break
		}
		path = sitesPath + "?limit=1&cursor=" + body.NextCursor
	}
	if len(seen) != 2 {
		t.Fatalf("walk saw %v; want both sites once", seen)
	}
}

func TestSites_StoreErrorHidesInternals(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, sitesPath, siteBody(siteLE1, "S1")},
		{http.MethodGet, sitesPath, ""},
		{http.MethodGet, sitesPath + "/" + siteID1, ""},
		{http.MethodPatch, sitesPath + "/" + siteID1, `{"version":1,"name":"x"}`},
	} {
		r, _ := newRouter(&fakeStore{err: errors.New("pg secret detail")})
		rec := do(r, tc.method, tc.path, tc.body, siteDuties, true)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "COM-0500") || strings.Contains(rec.Body.String(), "secret") {
			t.Fatalf("%s %s = %d %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
}

func TestSites_MetricsCountCreatesAndVersionBumps(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	created := do(r, http.MethodPost, sitesPath, siteBody(siteLE1, "S1"), siteDuties, true)
	var site store.Site
	if err := json.Unmarshal(created.Body.Bytes(), &site); err != nil {
		t.Fatal(err)
	}
	do(r, http.MethodPatch, sitesPath+"/"+site.ID, `{"version":1,"name":"Renamed"}`, siteDuties, true)
	do(r, http.MethodPatch, sitesPath+"/"+site.ID, `{"version":2,"name":"Renamed"}`, siteDuties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"org_sites_created_total 1", "org_sites_updated_total 1"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body.String())
		}
	}
}

func withDefault(object string) string {
	return `{"legalEntityId":"` + siteLE1 + `","code":"S1","name":"Plant","defaultWarehouse":` + object + `}`
}

func defaultSeeded() *fakeStore {
	f := seededWarehouses()
	f.sites[0].DefaultWarehouseID = &f.warehouses[0].ID
	return f
}

func TestSites_MetricCountsOnlyFreshCreatesWithADefaultWarehouse(t *testing.T) {
	f := &fakeStore{}
	r, a := newRouter(f)
	do(r, http.MethodPost, sitesPath, siteBody(siteLE1, "S1"), siteDuties, true)
	do(r, http.MethodPost, sitesPath, `{"legalEntityId":"`+siteLE1+`","code":"S2","name":"Plant","defaultWarehouse":{"code":"M2","name":"Main"}}`, siteDuties, true)
	do(r, http.MethodPost, sitesPath, `{"legalEntityId":"`+siteLE1+`","code":"S3","name":"Plant","defaultWarehouse":{"code":"bad code","name":"Main"}}`, siteDuties, true)
	f.replayCreates = true
	do(r, http.MethodPost, sitesPath, `{"legalEntityId":"`+siteLE1+`","code":"S2","name":"Plant","defaultWarehouse":{"code":"M2","name":"Main"}}`, siteDuties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"# TYPE org_site_default_warehouses_total counter", "org_site_default_warehouses_total 1\n", "org_sites_created_total 3"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body.String())
		}
	}
}
