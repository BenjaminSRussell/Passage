package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

// UpdateRouteDraft mimics the store: draft only, version must match, identity fields are kept.
func (f *fakeStore) UpdateRouteDraft(_ context.Context, v domain.RouteVersion, expected int, _ store.Idem) (domain.RouteVersion, error) {
	if f.err != nil {
		return domain.RouteVersion{}, f.err
	}
	for i, stored := range f.routes {
		if stored.ID != v.ID {
			continue
		}
		if err := stored.EnsureDraft(); err != nil {
			return stored, err
		}
		if stored.Version != expected {
			return stored, &domain.Error{Code: domain.CodeRouteConflict, Detail: fmt.Sprintf("the draft is at version %d", stored.Version)}
		}
		if err := domain.ValidateRouteVersion(v); err != nil {
			return domain.RouteVersion{}, err
		}
		v.Status, v.Version = stored.Status, stored.Version+1
		f.routes[i] = v
		return v, nil
	}
	return domain.RouteVersion{}, httpx.NewProblem(http.StatusNotFound, store.CodeRouteNotFound, "route version not found")
}

func routeEditBody(version string, extra string) string {
	return `{"effectiveFrom":"2026-03-01"` + extra + `,"version":` + version + `,"operations":` + routeOps + `}`
}

func routeEditFixtures() []domain.RouteVersion {
	fixtures := routeFixtures()
	for i := range fixtures {
		fixtures[i].Version = 3
		fixtures[i].VersionNo = i + 1
	}
	return fixtures
}

func TestUpdateRouteVersion(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		body   string
		duties string
		status int
		want   string
	}{
		{"saves a draft and bumps the version", rid(4), routeEditBody("3", ""), routeWriter, http.StatusOK, `"version":4`},
		{"replaces the operations", rid(4), routeEditBody("3", ""), routeWriter, http.StatusOK, `"operationCount":2`},
		{"identity fields may be sent unchanged", rid(4), routeEditBody("3", `,"productId":"BIKE","siteId":"S1","routeCode":"R1"`), routeWriter, http.StatusOK, `"version":4`},
		{"stale version is a conflict", rid(4), routeEditBody("2", ""), routeWriter, http.StatusConflict, domain.CodeRouteConflict},
		{"version is required", rid(4), strings.Replace(routeEditBody("3", ""), `"version":3,`, "", 1), routeWriter, http.StatusBadRequest, "version"},
		{"an active version cannot be edited", rid(3), routeEditBody("3", ""), routeWriter, http.StatusConflict, domain.CodeRouteState},
		{"the product cannot change", rid(4), routeEditBody("3", `,"productId":"TRIKE"`), routeWriter, http.StatusBadRequest, "productId"},
		{"the site cannot change", rid(4), routeEditBody("3", `,"siteId":"S9"`), routeWriter, http.StatusBadRequest, "siteId"},
		{"the route code cannot change", rid(4), routeEditBody("3", `,"routeCode":"R2"`), routeWriter, http.StatusBadRequest, "routeCode"},
		{"the version number cannot change", rid(4), routeEditBody("3", `,"versionNo":9`), routeWriter, http.StatusBadRequest, "versionNo"},
		{"no operations is invalid", rid(4), `{"effectiveFrom":"2026-03-01","version":3,"operations":[]}`, routeWriter, http.StatusBadRequest, "operations"},
		{"a bad date is invalid", rid(4), routeEditBody("3", `,"effectiveTo":"2026-01-01"`), routeWriter, http.StatusBadRequest, "effectiveTo"},
		{"a dangling next is refused", rid(4), `{"effectiveFrom":"2026-03-01","version":3,"operations":[{"no":10,"nextNo":99}]}`, routeWriter, http.StatusUnprocessableEntity, domain.CodeRouteNext},
		{"unknown id", rid(99), routeEditBody("3", ""), routeWriter, http.StatusNotFound, store.CodeRouteNotFound},
		{"not a uuid", "nope", routeEditBody("3", ""), routeWriter, http.StatusBadRequest, "COM-0001"},
		{"read duty alone is refused", rid(4), routeEditBody("3", ""), "engineering.read", http.StatusForbidden, "AUTH-0003"},
		{"an unknown field is refused", rid(4), routeEditBody("3", `,"bogus":1`), routeWriter, http.StatusBadRequest, "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{routes: routeEditFixtures()}
			for i := range f.routes {
				f.routes[i].RouteCode = "R1"
			}
			r, _ := newRouter(f, approves)
			rec := do(r, http.MethodPut, routesPath+"/"+tc.id, tc.body, tc.duties, true)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("update = %d %s; want %d containing %q", rec.Code, rec.Body, tc.status, tc.want)
			}
		})
	}
}

func TestUpdateRouteVersion_ReadBackShowsTheEditAndMetrics(t *testing.T) {
	f := &fakeStore{routes: routeEditFixtures()}
	r, a := newRouter(f, approves)
	if rec := do(r, http.MethodPut, routesPath+"/"+rid(4), routeEditBody("3", ""), routeWriter, true); rec.Code != http.StatusOK {
		t.Fatalf("update = %d %s", rec.Code, rec.Body)
	}
	rec := do(r, http.MethodGet, routesPath+"/"+rid(4), "", "engineering.read", true)
	var got struct {
		EffectiveFrom string `json:"effectiveFrom"`
		Version       int    `json:"version"`
		Operations    []struct{ No int }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.EffectiveFrom != "2026-03-01" || got.Version != 4 || len(got.Operations) != 2 {
		t.Fatalf("read back = %s (%v); want 2026-03-01, version 4, two operations", rec.Body, err)
	}
	if rec := do(r, http.MethodPut, routesPath+"/"+rid(4), routeEditBody("3", ""), routeWriter, true); rec.Code != http.StatusConflict {
		t.Fatalf("second save of version 3 = %d; want 409", rec.Code)
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"engineering_route_versions_updated_total 1", "engineering_route_version_conflicts_total 1"} {
		if !strings.Contains(metrics.Body.String(), want) {
			t.Fatalf("metrics lack %q: %s", want, metrics.Body)
		}
	}
}

func TestUpdateRouteVersion_UnexpectedFailureIsHidden(t *testing.T) {
	r, _ := newRouter(&fakeStore{routes: routeEditFixtures(), err: context.DeadlineExceeded}, approves)
	if rec := do(r, http.MethodPut, routesPath+"/"+rid(4), routeEditBody("3", ""), routeWriter, true); rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "deadline") {
		t.Fatalf("update = %d %s; want a hidden 500", rec.Code, rec.Body)
	}
}
