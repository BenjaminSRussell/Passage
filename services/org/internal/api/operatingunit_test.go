package api_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	operatingUnits = "/v1/org/operating-units"
	unitDuties     = "org.read,org.operating-unit.write"
	unitID1        = "00000000-0000-0000-0000-000000000001"
)

func seededUnits() *fakeStore {
	return &fakeStore{ous: []store.OperatingUnit{
		{ID: unitID1, Type: "cost_center", Code: "CC1", Name: "One", Status: "active", Version: 1},
		{ID: "00000000-0000-0000-0000-000000000002", Type: "department", Code: "D1", Name: "Two", Status: "active", Version: 1},
	}}
}

func TestOperatingUnits(t *testing.T) {
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
		{"create upper-cases code", &fakeStore{}, http.MethodPost, operatingUnits, `{"type":"department","code":"d100","name":" Ops "}`, unitDuties, true, http.StatusCreated, `"code":"D100","name":"Ops"`},
		{"create same code other type", seededUnits(), http.MethodPost, operatingUnits, `{"type":"business_unit","code":"d1","name":"x"}`, unitDuties, true, http.StatusCreated, `"type":"business_unit"`},
		{"create duplicate within type", seededUnits(), http.MethodPost, operatingUnits, `{"type":"department","code":"D1","name":"x"}`, unitDuties, true, http.StatusConflict, "ORG-OU-CODE-DUPLICATE"},
		{"create bad type", &fakeStore{}, http.MethodPost, operatingUnits, `{"type":"region","code":"D1","name":"x"}`, unitDuties, true, http.StatusBadRequest, `"type"`},
		{"create bad code", &fakeStore{}, http.MethodPost, operatingUnits, `{"type":"department","code":"-","name":"x"}`, unitDuties, true, http.StatusBadRequest, `"code"`},
		{"create blank name", &fakeStore{}, http.MethodPost, operatingUnits, `{"type":"department","code":"D1","name":"  "}`, unitDuties, true, http.StatusBadRequest, `"name"`},
		{"create manager on department", &fakeStore{}, http.MethodPost, operatingUnits, `{"type":"department","code":"D1","name":"x","managerPartyId":"` + unitID1 + `"}`, unitDuties, true, http.StatusBadRequest, `"managerPartyId"`},
		{"create manager on cost center", &fakeStore{}, http.MethodPost, operatingUnits, `{"type":"cost_center","code":"C1","name":"x","managerPartyId":"` + unitID1 + `"}`, unitDuties, true, http.StatusCreated, `"managerPartyId"`},
		{"create malformed legal entity", &fakeStore{}, http.MethodPost, operatingUnits, `{"type":"department","code":"D1","name":"x","defaultLegalEntityId":"x"}`, unitDuties, true, http.StatusBadRequest, `"defaultLegalEntityId"`},
		{"create unknown field", &fakeStore{}, http.MethodPost, operatingUnits, `{"type":"department","code":"D1","name":"x","x":1}`, unitDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create without duty", &fakeStore{}, http.MethodPost, operatingUnits, `{}`, "org.read", true, http.StatusForbidden, "AUTH-0003"},
		{"create without tenant", &fakeStore{}, http.MethodPost, operatingUnits, `{}`, "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"get ok", seededUnits(), http.MethodGet, operatingUnits + "/" + unitID1, "", unitDuties, true, http.StatusOK, `"code":"CC1"`},
		{"get unknown", seededUnits(), http.MethodGet, operatingUnits + "/00000000-0000-0000-0000-0000000000ff", "", unitDuties, true, http.StatusNotFound, "ORG-OU-NOT-FOUND"},
		{"get malformed", seededUnits(), http.MethodGet, operatingUnits + "/nope", "", unitDuties, true, http.StatusNotFound, "ORG-OU-NOT-FOUND"},
		{"get without tenant", seededUnits(), http.MethodGet, operatingUnits + "/" + unitID1, "", "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"list bad type", seededUnits(), http.MethodGet, operatingUnits + "?type=region", "", unitDuties, true, http.StatusBadRequest, `"type"`},
		{"list bad status", seededUnits(), http.MethodGet, operatingUnits + "?status=deleted", "", unitDuties, true, http.StatusBadRequest, `"status"`},
		{"list bad legal entity", seededUnits(), http.MethodGet, operatingUnits + "?defaultLegalEntityId=x", "", unitDuties, true, http.StatusBadRequest, `"defaultLegalEntityId"`},
		{"list cursor without slash", seededUnits(), http.MethodGet, operatingUnits + "?cursor=Zm9v", "", unitDuties, true, http.StatusBadRequest, `"cursor"`},
		{"list without duty", seededUnits(), http.MethodGet, operatingUnits, "", "", true, http.StatusForbidden, "AUTH-0003"},
		{"patch name", seededUnits(), http.MethodPatch, operatingUnits + "/" + unitID1, `{"version":1,"name":"Renamed"}`, unitDuties, true, http.StatusOK, `"version":2`},
		{"patch code rejected", seededUnits(), http.MethodPatch, operatingUnits + "/" + unitID1, `{"version":1,"code":"X1"}`, unitDuties, true, http.StatusBadRequest, "COM-0001"},
		{"patch type rejected", seededUnits(), http.MethodPatch, operatingUnits + "/" + unitID1, `{"version":1,"type":"department"}`, unitDuties, true, http.StatusBadRequest, "COM-0001"},
		{"patch missing version", seededUnits(), http.MethodPatch, operatingUnits + "/" + unitID1, `{"name":"x"}`, unitDuties, true, http.StatusBadRequest, `"version"`},
		{"patch stale version", seededUnits(), http.MethodPatch, operatingUnits + "/" + unitID1, `{"version":7,"name":"x"}`, unitDuties, true, http.StatusConflict, "ORG-VERSION-CONFLICT"},
		{"patch bad status and id", seededUnits(), http.MethodPatch, operatingUnits + "/" + unitID1, `{"version":1,"status":"deleted","defaultLegalEntityId":"x"}`, unitDuties, true, http.StatusBadRequest, `"defaultLegalEntityId"`},
		{"patch clear id", seededUnits(), http.MethodPatch, operatingUnits + "/" + unitID1, `{"version":1,"defaultLegalEntityId":""}`, unitDuties, true, http.StatusOK, `"version":1`},
		{"patch malformed id", seededUnits(), http.MethodPatch, operatingUnits + "/nope", `{"version":1}`, unitDuties, true, http.StatusNotFound, "ORG-OU-NOT-FOUND"},
		{"patch without duty", seededUnits(), http.MethodPatch, operatingUnits + "/" + unitID1, `{"version":1}`, "org.read", true, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("%s = %d %s, want %d containing %q", tc.name, rec.Code, rec.Body, tc.wantStatus, tc.wantBody)
			}
			if strings.HasSuffix(tc.path, "/nope") && tc.store.ouCalls != 0 {
				t.Fatalf("malformed id reached the store")
			}
		})
	}
}

func TestOperatingUnits_HierarchyFilter(t *testing.T) {
	const typeID = "00000000-0000-0000-0000-0000000000aa"
	base := operatingUnits + "?hierarchyTypeId=" + typeID
	rejected := []struct{ name, path, field string }{
		{"bad type id", operatingUnits + "?hierarchyTypeId=bogus", `"hierarchyTypeId"`},
		{"impossible month", base + "&asOf=2026-13-01", `"asOf"`},
		{"datetime", base + "&asOf=2026-03-01T00:00:00Z", `"asOf"`},
		{"as of alone", operatingUnits + "?asOf=2026-03-01", `"asOf"`},
		{"bad as of alone", operatingUnits + "?asOf=nope", `"asOf"`},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			f := seededUnits()
			rec := do(routerAt(f), http.MethodGet, tc.path, "", unitDuties, true)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") || !strings.Contains(rec.Body.String(), tc.field) || f.ouFilter != (store.OperatingUnitFilter{}) {
				t.Fatalf("got %d %s; want 400 on %s with no store call", rec.Code, rec.Body.String(), tc.field)
			}
		})
	}
	t.Run("as of defaults to today", func(t *testing.T) {
		f := seededUnits()
		if rec := do(routerAt(f), http.MethodGet, base, "", unitDuties, true); rec.Code != http.StatusOK {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		if want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC); f.ouFilter.HierarchyTypeID != typeID || !f.ouFilter.AsOf.Equal(want) {
			t.Fatalf("filter = %+v; want the type and %s", f.ouFilter, want)
		}
	})
	t.Run("explicit as of reaches the store", func(t *testing.T) {
		f := seededUnits()
		do(routerAt(f), http.MethodGet, base+"&asOf=2026-08-01&type=department", "", unitDuties, true)
		if want := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC); !f.ouFilter.AsOf.Equal(want) || f.ouFilter.Type != "department" {
			t.Fatalf("filter = %+v", f.ouFilter)
		}
	})
	t.Run("no hierarchy filter leaves the filter zero", func(t *testing.T) {
		f := seededUnits()
		do(routerAt(f), http.MethodGet, operatingUnits, "", unitDuties, true)
		if f.ouFilter.HierarchyTypeID != "" {
			t.Fatalf("filter = %+v", f.ouFilter)
		}
	})
	t.Run("unknown type", func(t *testing.T) {
		f := &fakeStore{err: httpx.NewProblem(http.StatusNotFound, store.CodeHierarchyTypeNotFound, "hierarchy type not found")}
		if rec := do(routerAt(f), http.MethodGet, base, "", unitDuties, true); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "ORG-HIER-TYPE-NOT-FOUND") {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestOperatingUnits_StoreErrorHidesInternals(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, operatingUnits, `{"type":"department","code":"D1","name":"x"}`},
		{http.MethodGet, operatingUnits, ""},
		{http.MethodGet, operatingUnits + "/" + unitID1, ""},
		{http.MethodPatch, operatingUnits + "/" + unitID1, `{"version":1,"name":"x"}`},
	} {
		f := seededUnits()
		f.err = errors.New("pg secret detail")
		r, _ := newRouter(f)
		rec := do(r, tc.method, tc.path, tc.body, unitDuties, true)
		if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "COM-0500") || strings.Contains(rec.Body.String(), "secret") {
			t.Fatalf("%s %s = %d %s", tc.method, tc.path, rec.Code, rec.Body)
		}
	}
}

func TestOperatingUnits_ListWalksPages(t *testing.T) {
	r, _ := newRouter(seededUnits())
	var codes []string
	path := operatingUnits + "?limit=1"
	for range 3 {
		rec := do(r, http.MethodGet, path, "", unitDuties, true)
		var page struct {
			Items      []store.OperatingUnit `json:"items"`
			NextCursor string                `json:"nextCursor"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("page = %d %s, %v", rec.Code, rec.Body, err)
		}
		for _, u := range page.Items {
			codes = append(codes, u.Code)
		}
		if page.NextCursor == "" {
			break
		}
		path = operatingUnits + "?limit=1&cursor=" + page.NextCursor
	}
	if strings.Join(codes, ",") != "CC1,D1" {
		t.Fatalf("walk = %v, want CC1,D1 once each", codes)
	}
}

func TestOperatingUnits_MetricsCountCreatesAndVersionBumps(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	rec := do(r, http.MethodPost, operatingUnits, `{"type":"department","code":"D1","name":"x"}`, unitDuties, true)
	var created store.OperatingUnit
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	do(r, http.MethodPatch, operatingUnits+"/"+created.ID, `{"version":1,"name":"y"}`, unitDuties, true)
	do(r, http.MethodPatch, operatingUnits+"/"+created.ID, `{"version":2,"name":"y"}`, unitDuties, true)
	out := httptest.NewRecorder()
	a.Metrics().ServeHTTP(out, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"org_operating_units_created_total 1", "org_operating_units_updated_total 1"} {
		if !strings.Contains(out.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, out.Body)
		}
	}
}
