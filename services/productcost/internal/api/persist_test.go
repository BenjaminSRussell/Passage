package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

// fakeStore records what the handlers pass and answers from its fields. The
// store's own rules are tested against Postgres in internal/store.
type fakeStore struct {
	key, after      string
	limit           int
	filter          store.VersionFilter
	spec            domain.VersionSpec
	component       domain.CostComponent
	group           domain.CostGroup
	moveID, action  string
	expected, calls int
	replayed        bool
	err             error
}

func (f *fakeStore) ListComponents(context.Context) ([]store.Component, error) {
	f.calls++
	return []store.Component{{ID: ids.Nil, CostComponent: domain.CostComponent{Code: "DM"}}}, f.err
}

func (f *fakeStore) CreateComponent(_ context.Context, in domain.CostComponent, key string) (store.Component, bool, error) {
	f.calls++
	f.component, f.key = in, key
	return store.Component{ID: ids.Nil, CostComponent: in}, f.replayed, f.err
}

func (f *fakeStore) ListGroups(context.Context) ([]store.Group, error) {
	f.calls++
	return []store.Group{}, f.err
}

func (f *fakeStore) CreateGroup(_ context.Context, in domain.CostGroup, key string) (store.Group, bool, error) {
	f.calls++
	f.group, f.key = in, key
	return store.Group{ID: ids.Nil, CostGroup: in, ComponentCode: "DL"}, f.replayed, f.err
}

func (f *fakeStore) ListVersions(_ context.Context, filter store.VersionFilter, after string, limit int) (store.VersionPage, error) {
	f.calls++
	f.filter, f.after, f.limit = filter, after, limit
	return store.VersionPage{Items: []store.Version{}}, f.err
}

func (f *fakeStore) GetVersion(_ context.Context, id string) (store.Version, error) {
	f.calls++
	return store.Version{ID: id, Status: domain.VersionOpen, Version: 1, SiteIDs: []string{}}, f.err
}

func (f *fakeStore) CreateVersion(_ context.Context, spec domain.VersionSpec, key string) (store.Version, bool, error) {
	f.calls++
	f.spec, f.key = spec, key
	return store.Version{ID: ids.Nil, Code: spec.Code, Status: domain.VersionOpen, Version: 1, SiteIDs: spec.SiteIDs}, f.replayed, f.err
}

func (f *fakeStore) TransitionVersion(_ context.Context, id string, expected int, action string) (store.Version, error) {
	f.calls++
	f.moveID, f.expected, f.action = id, expected, action
	return store.Version{ID: id, Status: domain.VersionBlocked, Version: expected + 1, SiteIDs: []string{}}, f.err
}

func storedRouter(f *fakeStore) http.Handler {
	r := httpx.NewRouter()
	a := New(nil, Config{})
	if f != nil {
		a.WithStore(f)
	}
	a.Register(r)
	return r
}

type call struct {
	method, path, body, duties, key, scope string
}

func (c call) do(h http.Handler) *httptest.ResponseRecorder {
	req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, testkit.TenantID(1))
	req.Header.Set(tenant.HeaderDuties, c.duties)
	if c.key != "" {
		req.Header.Set("Idempotency-Key", c.key)
	}
	if c.scope != "" {
		req.Header.Set(tenant.HeaderLegalEntity, c.scope)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func problemCode(t *testing.T, rec *httptest.ResponseRecorder) (string, map[string]string) {
	t.Helper()
	var p struct {
		Code   string            `json:"code"`
		Errors map[string]string `json:"errors"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("not a problem: %s", rec.Body.String())
	}
	return p.Code, p.Errors
}

const (
	maintain = "CostingMaintain"
	view     = "CostingView"
	activate = "CostingActivate"
	someID   = "0190f0c2-7a5e-7d3c-9a1b-2c3d4e5f6a7b"
)

var componentBody = `{"code":" IND-MAT ","name":"Material overhead","costGroupType":"Indirect"}`

func TestStoredRoutes_WithoutStoreAnswer503(t *testing.T) {
	h := storedRouter(nil)
	for _, c := range []call{
		{http.MethodGet, "/v1/product-costing/cost-components", "", view, "", ""},
		{http.MethodPost, "/v1/product-costing/cost-components", componentBody, maintain, "k", ""},
		{http.MethodGet, "/v1/product-costing/cost-groups", "", view, "", ""},
		{http.MethodPost, "/v1/product-costing/cost-groups", `{}`, maintain, "k", ""},
		{http.MethodGet, "/v1/product-costing/versions", "", view, "", ""},
		{http.MethodPost, "/v1/product-costing/versions", `{}`, maintain, "k", ""},
		{http.MethodGet, "/v1/product-costing/versions/" + someID, "", view, "", ""},
		{http.MethodPost, "/v1/product-costing/versions/" + someID + "/transition", `{"action":"block","version":1}`, maintain, "", ""},
		{http.MethodPost, "/v1/product-costing/versions/" + someID + "/activate", `{"version":1}`, activate, "", ""},
	} {
		rec := c.do(h)
		if code, _ := problemCode(t, rec); rec.Code != http.StatusServiceUnavailable || code != codeStoreUnavailable {
			t.Errorf("%s %s: %d %s, want 503 %s", c.method, c.path, rec.Code, code, codeStoreUnavailable)
		}
	}
}

func TestStoredRoutes_EnforceSegregatedDuties(t *testing.T) {
	f := &fakeStore{}
	h := storedRouter(f)
	for _, c := range []call{
		{http.MethodGet, "/v1/product-costing/versions", "", maintain, "", ""},
		{http.MethodPost, "/v1/product-costing/cost-components", componentBody, view, "k", ""},
		{http.MethodPost, "/v1/product-costing/cost-groups", `{}`, view + "," + activate, "k", ""},
		{http.MethodPost, "/v1/product-costing/versions", `{}`, view + "," + activate, "k", ""},
		// Activation is CostingActivate only; the lifecycle moves are CostingMaintain only (PRD 7.39.17).
		{http.MethodPost, "/v1/product-costing/versions/" + someID + "/activate", `{"version":1}`, maintain, "", ""},
		{http.MethodPost, "/v1/product-costing/versions/" + someID + "/transition", `{"action":"block","version":1}`, activate, "", ""},
	} {
		if rec := c.do(h); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with %s: %d, want 403", c.method, c.path, c.duties, rec.Code)
		}
	}
	if f.calls != 0 {
		t.Fatalf("the store was called %d times for refused requests", f.calls)
	}
}

func TestCreateComponent_NormalisesAndNeedsAnIdempotencyKey(t *testing.T) {
	f := &fakeStore{}
	h := storedRouter(f)
	rec := call{http.MethodPost, "/v1/product-costing/cost-components", componentBody, maintain, "", ""}.do(h)
	if code, fields := problemCode(t, rec); rec.Code != http.StatusBadRequest || code != "COM-0001" || fields["Idempotency-Key"] == "" {
		t.Fatalf("no key: %d %s %v", rec.Code, code, fields)
	}
	rec = call{http.MethodPost, "/v1/product-costing/cost-components", `{"code":"dm","name":"","costGroupType":"Labour","behaviour":"Mixed"}`, maintain, "k", ""}.do(h)
	if _, fields := problemCode(t, rec); rec.Code != http.StatusBadRequest || len(fields) != 4 {
		t.Fatalf("bad component: %d %v, want 4 field errors", rec.Code, fields)
	}
	if f.calls != 0 {
		t.Fatalf("invalid requests reached the store")
	}
	rec = call{http.MethodPost, "/v1/product-costing/cost-components", componentBody, maintain, " key-1 ", ""}.do(h)
	if rec.Code != http.StatusCreated || rec.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if f.key != "key-1" || f.component.Code != "IND-MAT" || f.component.Behaviour != domain.BehaviourVariable {
		t.Fatalf("store got key %q and %+v", f.key, f.component)
	}
	f.replayed = true
	if rec = (call{http.MethodPost, "/v1/product-costing/cost-components", componentBody, maintain, "key-1", ""}).do(h); rec.Header().Get("Idempotent-Replayed") != "true" || rec.Code != http.StatusCreated {
		t.Fatalf("replay: %d, header %q", rec.Code, rec.Header().Get("Idempotent-Replayed"))
	}
}

func TestStoredRoutes_MapStoreErrors(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"coded rule", &domain.CodedError{Code: domain.CodeComponentLimit, Detail: "limit"}, http.StatusUnprocessableEntity, domain.CodeComponentLimit},
		{"duplicate", httpx.NewProblem(http.StatusConflict, domain.CodeComponentDuplicate, "dup"), http.StatusConflict, domain.CodeComponentDuplicate},
		{"key reuse", httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeIdempotencyReuse, "reuse"), http.StatusUnprocessableEntity, store.CodeIdempotencyReuse},
	}
	for _, tc := range tests {
		h := storedRouter(&fakeStore{err: tc.err})
		rec := call{http.MethodPost, "/v1/product-costing/cost-components", componentBody, maintain, "k", ""}.do(h)
		if code, _ := problemCode(t, rec); rec.Code != tc.status || code != tc.code {
			t.Errorf("%s: %d %s, want %d %s", tc.name, rec.Code, code, tc.status, tc.code)
		}
	}
}

func TestCreateGroup_Validates(t *testing.T) {
	f := &fakeStore{}
	h := storedRouter(f)
	rec := call{http.MethodPost, "/v1/product-costing/cost-groups", `{"code":"MACH","name":"Machining","type":"DirectManufacturing","componentId":"DL"}`, maintain, "k", ""}.do(h)
	if _, fields := problemCode(t, rec); rec.Code != http.StatusBadRequest || fields["componentId"] == "" {
		t.Fatalf("component code instead of id: %d %v", rec.Code, fields)
	}
	body := `{"code":"MACH","name":"Machining","type":"DirectManufacturing","behaviour":"Fixed","componentId":"` + someID + `"}`
	rec = call{http.MethodPost, "/v1/product-costing/cost-groups", body, maintain, "k", ""}.do(h)
	if rec.Code != http.StatusCreated || f.group.ComponentID != someID || f.group.Behaviour != domain.BehaviourFixed {
		t.Fatalf("create: %d %+v", rec.Code, f.group)
	}
	if !strings.Contains(rec.Body.String(), `"componentCode":"DL"`) {
		t.Fatalf("response %s lacks the component code", rec.Body.String())
	}
}

func TestCreateVersion_ValidatesScopesAndNormalises(t *testing.T) {
	le := ids.New()
	f := &fakeStore{}
	h := storedRouter(f)
	post := func(body, scope string) *httptest.ResponseRecorder {
		return call{http.MethodPost, "/v1/product-costing/versions", body, maintain, "key-v", scope}.do(h)
	}
	rec := post(`{"legalEntityId":"LE-1","code":"","type":"Budget","siteIds":["S1"]}`, "")
	if _, fields := problemCode(t, rec); rec.Code != http.StatusBadRequest || fields["legalEntityId"] == "" || fields["code"] == "" || fields["type"] == "" || fields["siteIds[0]"] == "" {
		t.Fatalf("bad spec: %d %v", rec.Code, fields)
	}
	rec = post(`{"legalEntityId":"`+le+`","code":"SIM","fallback":"SpecifiedVersion"}`, "")
	if code, _ := problemCode(t, rec); rec.Code != http.StatusUnprocessableEntity || code != domain.CodeFallbackVersion {
		t.Fatalf("specified fallback without a version: %d %s", rec.Code, code)
	}
	rec = post(`{"legalEntityId":"`+le+`","code":"STD"}`, "")
	if code, _ := problemCode(t, rec); rec.Code != http.StatusForbidden || code != domain.CodeLegalEntityScope {
		t.Fatalf("no legal-entity scope: %d %s", rec.Code, code)
	}
	rec = post(`{"legalEntityId":"`+le+`","code":"STD"}`, ids.New()+","+ids.New())
	if code, _ := problemCode(t, rec); rec.Code != http.StatusForbidden || code != domain.CodeLegalEntityScope {
		t.Fatalf("outside the caller's scope: %d %s", rec.Code, code)
	}
	if f.calls != 0 {
		t.Fatal("refused requests reached the store")
	}
	rec = post(`{"legalEntityId":"`+le+`","code":" STD-2027 ","effectiveFrom":"2027-01-01T00:00:00-05:00"}`, ids.New()+", "+le)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	want := time.Date(2027, 1, 1, 5, 0, 0, 0, time.UTC)
	if f.spec.Code != "STD-2027" || f.spec.Type != domain.VersionStandard || f.spec.Fallback != domain.FallbackActive || !f.spec.EffectiveFrom.Equal(want) || f.key != "key-v" {
		t.Fatalf("store got %+v key %q", f.spec, f.key)
	}
}

func TestListVersions_PassesFiltersAndChecksThem(t *testing.T) {
	f := &fakeStore{}
	h := storedRouter(f)
	le := ids.New()
	rec := call{http.MethodGet, "/v1/product-costing/versions?legalEntityId=x&status=Draft&type=Budget&after=1&limit=501", "", view, "", ""}.do(h)
	if _, fields := problemCode(t, rec); rec.Code != http.StatusBadRequest || len(fields) != 5 {
		t.Fatalf("bad query: %d %v", rec.Code, fields)
	}
	rec = call{http.MethodGet, "/v1/product-costing/versions?legalEntityId=" + le + "&status=Active&type=Planned&after=" + someID + "&limit=20", "", view, "", ""}.do(h)
	if rec.Code != http.StatusOK || f.filter != (store.VersionFilter{LegalEntityID: le, Status: "Active", Type: "Planned"}) || f.after != someID || f.limit != 20 {
		t.Fatalf("list: %d filter %+v after %q limit %d", rec.Code, f.filter, f.after, f.limit)
	}
	if rec = (call{http.MethodGet, "/v1/product-costing/versions", "", view, "", ""}).do(h); f.limit != defaultPageSize || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("default page: limit %d body %s", f.limit, rec.Body.String())
	}
}

func TestStoredVersionMoves(t *testing.T) {
	f := &fakeStore{}
	h := storedRouter(f)
	path := "/v1/product-costing/versions/" + someID
	for _, c := range []struct {
		body, field string
	}{
		{`{"action":"block"}`, "version"},
		{`{"action":"block","version":0}`, "version"},
		{`{"action":"activate","version":1}`, "action"},
		{`{"action":"delete","version":1}`, "action"},
	} {
		rec := call{http.MethodPost, path + "/transition", c.body, maintain, "", ""}.do(h)
		if _, fields := problemCode(t, rec); rec.Code != http.StatusBadRequest || fields[c.field] == "" {
			t.Errorf("%s: %d %v, want a %s error", c.body, rec.Code, fields, c.field)
		}
	}
	if rec := (call{http.MethodPost, "/v1/product-costing/versions/not-a-uuid/transition", `{"action":"block","version":1}`, maintain, "", ""}).do(h); rec.Code != http.StatusNotFound {
		t.Fatalf("malformed id: %d", rec.Code)
	}
	if f.calls != 0 {
		t.Fatal("refused moves reached the store")
	}
	rec := call{http.MethodPost, path + "/transition", `{"action":"block","version":3}`, maintain, "", ""}.do(h)
	if rec.Code != http.StatusOK || f.moveID != someID || f.expected != 3 || f.action != domain.ActionBlock || !strings.Contains(rec.Body.String(), `"version":4`) {
		t.Fatalf("block: %d %s (store got %s %d %s)", rec.Code, rec.Body.String(), f.moveID, f.expected, f.action)
	}
	if rec = (call{http.MethodPost, path + "/activate", `{"version":4}`, activate, "", ""}).do(h); rec.Code != http.StatusOK || f.action != domain.ActionActivate || f.expected != 4 {
		t.Fatalf("activate: %d action %s", rec.Code, f.action)
	}
	f.err = httpx.NewProblem(http.StatusConflict, domain.CodeVersionConflict, "current version is 5")
	if rec = (call{http.MethodPost, path + "/activate", `{"version":4}`, activate, "", ""}).do(h); rec.Code != http.StatusConflict {
		t.Fatalf("stale version: %d", rec.Code)
	}
	f.err = &domain.CodedError{Code: domain.CodeSimulationActivation, Detail: "simulation"}
	if rec = (call{http.MethodPost, path + "/activate", `{"version":1}`, activate, "", ""}).do(h); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("simulation: %d", rec.Code)
	}
}

// The stored routes share the /v1/product-costing/versions prefix with the stateless
// slice-3 routes; both must still resolve.
func TestStoredAndStatelessVersionRoutesCoexist(t *testing.T) {
	f := &fakeStore{}
	h := storedRouter(f)
	if rec := (call{http.MethodGet, "/v1/product-costing/versions/" + someID, "", view, "", ""}).do(h); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), someID) {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	stateless := `{"version":{"id":"v1","type":"Standard","status":"Open","fallback":"Active"},"action":"block"}`
	if rec := (call{http.MethodPost, "/v1/product-costing/versions:transition", stateless, maintain, "", ""}).do(h); rec.Code != http.StatusOK {
		t.Fatalf("stateless transition: %d %s", rec.Code, rec.Body.String())
	}
	if f.calls != 1 {
		t.Fatalf("store calls %d, want 1 (the stateless route stores nothing)", f.calls)
	}
}
