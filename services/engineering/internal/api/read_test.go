package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

const (
	routesPath = "/v1/engineering/route-versions"
	activePath = routesPath + ":active"
)

func rid(n int) string { return fmt.Sprintf("00000000-0000-0000-0000-%012d", n) }

func route(n int, product, site, status, from, to string) domain.RouteVersion {
	v := domain.RouteVersion{ID: rid(n), ProductID: product, SiteID: site, RouteCode: "R", VersionNo: n, EffectiveFrom: day(from), Status: status}
	if to != "" {
		v.EffectiveTo = day(to)
	}
	v.Operations = []domain.VersionOperation{{OperationID: "OP", LinkType: domain.LinkHard, Requirements: []domain.OperationRequirement{{Kind: domain.ReqCapability, RefID: "weld", Level: 1, Quantity: 1}}}}
	v.Operations[0].No, v.Operations[0].ProcessQty = 10, dec("1")
	return v
}

func routeFixtures() []domain.RouteVersion {
	return []domain.RouteVersion{
		route(3, "BIKE", "S1", domain.StatusActive, "2026-06-01", ""),
		route(1, "BIKE", "S1", domain.StatusExpired, "2025-01-01", "2025-12-31"),
		route(2, "BIKE", "S1", domain.StatusActive, "2026-01-01", "2026-05-31"),
		route(4, "BIKE", "S1", domain.StatusDraft, "2026-01-01", ""),
		route(5, "BIKE", "S2", domain.StatusActive, "2026-01-01", ""),
		route(6, "TRIKE", "S1", domain.StatusActive, "2026-01-01", ""),
	}
}

func (f *fakeStore) ListRouteVersions(_ context.Context, flt store.RouteVersionFilter, after string, limit int) (store.Page[store.RouteVersionHeader], error) {
	if f.err != nil {
		return store.Page[store.RouteVersionHeader]{}, f.err
	}
	var matched []store.RouteVersionHeader
	for _, v := range f.routes {
		covers := flt.AsOf.IsZero() || (!v.EffectiveFrom.After(flt.AsOf) && (v.EffectiveTo.IsZero() || !v.EffectiveTo.Before(flt.AsOf)))
		if v.ID > after && covers && (flt.ProductID == "" || v.ProductID == flt.ProductID) && (flt.SiteID == "" || v.SiteID == flt.SiteID) && (flt.Status == "" || v.Status == flt.Status) {
			matched = append(matched, store.RouteVersionHeader{Version: v, Operations: len(v.Operations)})
		}
	}
	slices.SortFunc(matched, func(a, b store.RouteVersionHeader) int { return strings.Compare(a.Version.ID, b.Version.ID) })
	page := store.Page[store.RouteVersionHeader]{Items: matched}
	if len(matched) > limit {
		page.Items = matched[:limit]
		page.NextID = page.Items[limit-1].Version.ID
	}
	return page, nil
}

func (f *fakeStore) ActiveRouteVersion(_ context.Context, product, site string, asOf time.Time) (domain.RouteVersion, error) {
	if f.err != nil {
		return domain.RouteVersion{}, f.err
	}
	for _, v := range f.routes {
		if v.ProductID == product && v.SiteID == site && v.Status == domain.StatusActive && !v.EffectiveFrom.After(asOf) && (v.EffectiveTo.IsZero() || !v.EffectiveTo.Before(asOf)) {
			return v, nil
		}
	}
	return domain.RouteVersion{}, httpx.NewProblem(http.StatusNotFound, store.CodeRouteNotFound, "no active route version covers the date")
}

func (f *fakeStore) GetRouteVersion(_ context.Context, id string) (domain.RouteVersion, error) {
	if f.err != nil {
		return domain.RouteVersion{}, f.err
	}
	for _, v := range f.routes {
		if v.ID == id {
			return v, nil
		}
	}
	return domain.RouteVersion{}, httpx.NewProblem(http.StatusNotFound, store.CodeRouteNotFound, "route version not found")
}

func (f *fakeStore) ActivateRouteVersion(_ context.Context, id string) (domain.RouteVersion, error) {
	if f.err != nil {
		return domain.RouteVersion{}, f.err
	}
	for i, v := range f.routes {
		if v.ID != id {
			continue
		}
		if v.Status != domain.StatusActive && f.activateErr != nil {
			return domain.RouteVersion{}, f.activateErr
		}
		f.routes[i].Status = domain.StatusActive
		return f.routes[i], nil
	}
	return domain.RouteVersion{}, httpx.NewProblem(http.StatusNotFound, store.CodeRouteNotFound, "route version not found")
}

func (f *fakeStore) GetBomVersion(_ context.Context, id string) (domain.BomVersion, error) {
	if f.err != nil {
		return domain.BomVersion{}, f.err
	}
	for _, v := range append(slices.Clone(f.created), f.listed...) {
		if v.ID == id {
			return v, nil
		}
	}
	return domain.BomVersion{}, httpx.NewProblem(http.StatusNotFound, store.CodeBomNotFound, "BOM version not found")
}

type routeList struct {
	Items []struct {
		ID string `json:"id"`
	} `json:"items"`
	NextCursor string `json:"nextCursor"`
}

func listIDs(t *testing.T, r http.Handler, query string) []string {
	t.Helper()
	var ids []string
	cursor := ""
	for range 20 {
		rec := do(r, http.MethodGet, routesPath+"?"+query+"&cursor="+cursor, "", allDuties, true)
		var body routeList
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
			t.Fatalf("list %q = %d %s", query, rec.Code, rec.Body)
		}
		for _, item := range body.Items {
			ids = append(ids, item.ID)
		}
		if body.NextCursor == "" {
			return ids
		}
		cursor = body.NextCursor
	}
	t.Fatal("paging did not terminate")
	return nil
}

func TestListRouteVersions(t *testing.T) {
	r, _ := newRouter(&fakeStore{routes: routeFixtures()}, approves)
	tests := []struct {
		name, query string
		want        []int
	}{
		{"every row across pages with no gap or repeat", "limit=2", []int{1, 2, 3, 4, 5, 6}},
		{"product and site", "productId=BIKE&siteId=S2&limit=1", []int{5}},
		{"asOf covers range", "productId=BIKE&siteId=S1&asOf=2026-03-01&limit=1", []int{2, 4}},
		{"empty filter values are no filter", "productId=&siteId=&status=&asOf=", []int{1, 2, 3, 4, 5, 6}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var want []string
			for _, n := range tc.want {
				want = append(want, rid(n))
			}
			if got := listIDs(t, r, tc.query); !slices.Equal(got, want) {
				t.Fatalf("ids = %v; want %v", got, want)
			}
		})
	}
	rec := do(r, http.MethodGet, routesPath+"?productId=TRIKE", "", allDuties, true)
	if !strings.Contains(rec.Body.String(), `"operationCount":1`) || strings.Contains(rec.Body.String(), `"operations"`) {
		t.Fatalf("header must carry operationCount and no operations: %s", rec.Body)
	}
	if rec := do(r, http.MethodGet, routesPath+"?productId=NOPE", "", allDuties, true); !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty page = %s; want items []", rec.Body)
	}
}

func TestListRouteVersions_RejectsBadInput(t *testing.T) {
	r, _ := newRouter(&fakeStore{routes: routeFixtures()}, approves)
	for _, bad := range []string{
		"limit=0", "limit=101", "limit=x", "cursor=!!", "cursor=YWJj", "status=bogus", "asOf=2026-13-40", "asOf=abc", "asOf=20260101", "productId=" + strings.Repeat("p", 65), "siteId=" + strings.Repeat("s", 65),
	} {
		rec := do(r, http.MethodGet, routesPath+"?"+bad, "", allDuties, true)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
			t.Fatalf("%s = %d %s; want 400 COM-0001", bad, rec.Code, rec.Body)
		}
	}
}

func TestActiveRouteVersion(t *testing.T) {
	r, _ := newRouter(&fakeStore{routes: routeFixtures()}, approves)
	rec := do(r, http.MethodGet, activePath+"?productId=BIKE&siteId=S1&asOf=2026-06-01", "", allDuties, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"`+rid(3)+`"`) || !strings.Contains(rec.Body.String(), `"requirements":[{"kind":"capability"`) {
		t.Fatalf("active = %d %s; want route 3 with operations and requirements", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, activePath+"?productId=BIKE&siteId=S1", "", allDuties, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"id":"`+rid(3)+`"`) {
		t.Fatalf("asOf must default to today: %d %s", rec.Code, rec.Body)
	}
	if rec := do(r, http.MethodGet, activePath+"?productId=BIKE&siteId=S1&asOf=2025-06-01", "", allDuties, true); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeRouteNotFound) {
		t.Fatalf("uncovered date = %d %s; want 404 %s", rec.Code, rec.Body, store.CodeRouteNotFound)
	}
	for _, bad := range []string{"", "?productId=BIKE", "?siteId=S1", "?productId=&siteId=S1", "?productId=BIKE&siteId=S1&asOf=nope", "?productId=" + strings.Repeat("p", 65) + "&siteId=S1"} {
		rec := do(r, http.MethodGet, activePath+bad, "", allDuties, true)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
			t.Fatalf("%q = %d %s; want 400 COM-0001", bad, rec.Code, rec.Body)
		}
	}
}

func TestGetRouteVersion(t *testing.T) {
	r, _ := newRouter(&fakeStore{routes: routeFixtures()}, approves)
	if rec := do(r, http.MethodGet, routesPath+"/"+rid(2), "", allDuties, true); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"effectiveTo":"2026-05-31"`) {
		t.Fatalf("detail = %d %s", rec.Code, rec.Body)
	}
	for path, want := range map[string]int{routesPath + "/" + rid(99): http.StatusNotFound, routesPath + "/not-a-uuid": http.StatusBadRequest} {
		if rec := do(r, http.MethodGet, path, "", allDuties, true); rec.Code != want {
			t.Fatalf("%s = %d %s; want %d", path, rec.Code, rec.Body, want)
		}
	}
}

const routeWriter = "engineering.route.write"

func activate(r http.Handler, id, duties string) *httptest.ResponseRecorder {
	return do(r, http.MethodPost, routesPath+"/"+id+":activate", "", duties, true)
}

func TestActivateRouteVersion(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		err    error
		duties string
		status int
		want   string
	}{
		{"draft that satisfies the checks", rid(4), nil, routeWriter, http.StatusOK, `"status":"active"`},
		{"already active is unchanged", rid(3), nil, routeWriter, http.StatusOK, `"status":"active"`},
		{"unknown id", rid(99), nil, routeWriter, http.StatusNotFound, store.CodeRouteNotFound},
		{"not a draft", rid(1), &domain.Error{Code: domain.CodeRouteState, Detail: "only a draft route version can be activated"}, routeWriter, http.StatusConflict, domain.CodeRouteState},
		{"overlapping active version", rid(4), &domain.Error{Code: domain.CodeRouteOverlap, Detail: "overlaps"}, routeWriter, http.StatusConflict, domain.CodeRouteOverlap},
		{"requirement cannot be met", rid(4), &domain.Error{Code: domain.CodeRouteUnsatisfied, Detail: "unsatisfied"}, routeWriter, http.StatusUnprocessableEntity, domain.CodeRouteUnsatisfied},
		{"read duty alone is refused", rid(4), nil, "engineering.read", http.StatusForbidden, "AUTH-0003"},
		{"not a uuid", "not-a-uuid", nil, routeWriter, http.StatusBadRequest, "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{routes: routeFixtures(), activateErr: tc.err}, approves)
			rec := activate(r, tc.id, tc.duties)
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("activate = %d %s; want %d containing %s", rec.Code, rec.Body, tc.status, tc.want)
			}
		})
	}
}

func TestActivateRouteVersion_RepeatReturnsSameBodyAndNoTenantIs401(t *testing.T) {
	f := &fakeStore{routes: routeFixtures()}
	r, a := newRouter(f, approves)
	first, second := activate(r, rid(4), routeWriter), activate(r, rid(4), routeWriter)
	if first.Code != http.StatusOK || second.Code != http.StatusOK || first.Body.String() != second.Body.String() || f.routes[3].Status != domain.StatusActive {
		t.Fatalf("repeat = %d %s then %d %s; want identical 200 bodies", first.Code, first.Body, second.Code, second.Body)
	}
	if rec := do(r, http.MethodPost, routesPath+"/"+rid(4)+":activate", "", "", false); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "AUTH-0001") {
		t.Fatalf("no tenant = %d %s; want 401", rec.Code, rec.Body)
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"engineering_route_versions_activated_total 2", "engineering_route_requests_failed_total 0", `route="/v1/engineering/route-versions/{id}:activate"`} {
		if !strings.Contains(metrics.Body.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, metrics.Body)
		}
	}
}

func TestActivateRouteVersion_UnexpectedFailureIsHiddenAndCounted(t *testing.T) {
	r, a := newRouter(&fakeStore{err: errors.New("pg secret detail")}, approves)
	rec := activate(r, rid(4), routeWriter)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("store error = %d %s; want 500 with hidden detail", rec.Code, rec.Body)
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "engineering_route_requests_failed_total 1") || !strings.Contains(metrics.Body.String(), "engineering_route_versions_activated_total 0") {
		t.Fatalf("metrics = %s; want one failure and no activation", metrics.Body)
	}
}

func TestGetBomVersion(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f, approves)
	body := `{"productId":"BIKE","effectiveFrom":"2026-01-01","lines":[{"lineNo":2,"componentProductId":"B","quantity":"2"},{"lineNo":1,"componentProductId":"A","quantity":"1"}]}`
	created := do(r, http.MethodPost, versions, body, allDuties, true)
	var made struct {
		ID string `json:"id"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &made) != nil {
		t.Fatalf("create = %d %s", created.Code, created.Body)
	}
	read := do(r, http.MethodGet, versions+"/"+made.ID, "", allDuties, true)
	if read.Code != http.StatusOK || read.Body.String() != created.Body.String() {
		t.Fatalf("read = %d %s; want the body create returned %s", read.Code, read.Body, created.Body)
	}
	if rec := do(r, http.MethodGet, versions+"/"+rid(99), "", allDuties, true); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), store.CodeBomNotFound) {
		t.Fatalf("unknown id = %d %s; want 404 %s", rec.Code, rec.Body, store.CodeBomNotFound)
	}
}

func TestReadRoutes_Authz(t *testing.T) {
	r, _ := newRouter(&fakeStore{routes: routeFixtures()}, approves)
	for _, path := range []string{routesPath, activePath + "?productId=BIKE&siteId=S1", routesPath + "/" + rid(1), versions + "/" + rid(1)} {
		if rec := do(r, http.MethodGet, path, "", "", false); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "AUTH-0001") {
			t.Fatalf("%s without tenant = %d %s; want 401", path, rec.Code, rec.Body)
		}
		if rec := do(r, http.MethodGet, path, "", "engineering.bom.write", true); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
			t.Fatalf("%s without read duty = %d %s; want 403", path, rec.Code, rec.Body)
		}
	}
}

func TestReadMetrics(t *testing.T) {
	r, a := newRouter(&fakeStore{err: errors.New("pg secret detail")}, approves)
	for _, path := range []string{routesPath, activePath + "?productId=BIKE&siteId=S1", routesPath + "/" + rid(1), versions + "/" + rid(1)} {
		rec := do(r, http.MethodGet, path, "", allDuties, true)
		if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "secret") {
			t.Fatalf("%s store error = %d %s; want 500 with hidden detail", path, rec.Code, rec.Body)
		}
	}
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		"engineering_route_requests_failed_total 3",
		"engineering_bom_versions_failed_total 1",
		`route="/v1/engineering/route-versions:active"`,
		`route="/v1/engineering/route-versions/{id}"`,
		`route="/v1/engineering/bom-versions/{id}"`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q:\n%s", want, rec.Body)
		}
	}
}
