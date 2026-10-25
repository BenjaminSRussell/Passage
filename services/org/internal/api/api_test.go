package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	pevents "github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/org/internal/api"
	orgevents "github.com/BenjaminSRussell/passage/services/org/internal/events"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

type fakeStore struct {
	err      error
	pingErr  error
	entities []store.LegalEntity
	byKey    map[string]store.LegalEntity
}

func (f *fakeStore) CreateLegalEntity(_ context.Context, in store.LegalEntity, key string) (store.LegalEntity, error) {
	if f.err != nil {
		return store.LegalEntity{}, f.err
	}
	if replay, ok := f.byKey[key]; ok && key != "" {
		return replay, nil
	}
	in.ID, in.Status, in.Version, in.CreatedAt = "id-1", "active", 1, time.Unix(0, 0).UTC()
	f.entities = append(f.entities, in)
	if f.byKey == nil {
		f.byKey = map[string]store.LegalEntity{}
	}
	f.byKey[key] = in
	return in, nil
}

func (f *fakeStore) ListLegalEntities(_ context.Context, after string, limit int) (store.LegalEntityPage, error) {
	if f.err != nil {
		return store.LegalEntityPage{}, f.err
	}
	page := store.LegalEntityPage{Items: []store.LegalEntity{}}
	for _, e := range f.entities {
		if e.Code > after {
			page.Items = append(page.Items, e)
		}
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextCode = page.Items[limit-1].Code
	}
	return page, nil
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func newRouter(f *fakeStore) (*chi.Mux, *api.API) {
	a := api.New(f)
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func do(r http.Handler, method, path, body, duties string, withTenant bool, headers ...string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if withTenant {
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-Duties", duties)
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const (
	legalEntities = "/v1/org/legal-entities"
	allDuties     = "org.read,org.legal-entity.write"
	validBody     = `{"code":"US01","name":"Passage US","country":"US","currency":"USD","timeZone":"UTC"}`
)

func TestLegalEntities(t *testing.T) {
	tests := []struct {
		name       string
		store      *fakeStore
		method     string
		body       string
		duties     string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"create ok", &fakeStore{}, http.MethodPost, validBody, allDuties, true, http.StatusCreated, `"code":"US01"`},
		{"create without tenant", &fakeStore{}, http.MethodPost, validBody, "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"create malformed body", &fakeStore{}, http.MethodPost, `{`, allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create unknown field", &fakeStore{}, http.MethodPost, `{"code":"US01","x":1}`, allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create reserved code", &fakeStore{}, http.MethodPost, `{"code":"SYS","name":"n","country":"US","currency":"USD","timeZone":"UTC"}`, allDuties, true, http.StatusBadRequest, `"code"`},
		{"create bad country and currency", &fakeStore{}, http.MethodPost, `{"code":"US01","name":"n","country":"USA","currency":"US"}`, allDuties, true, http.StatusBadRequest, `"currency"`},
		{"create missing time zone", &fakeStore{}, http.MethodPost, `{"code":"US01","name":"n","country":"US","currency":"USD"}`, allDuties, true, http.StatusBadRequest, `"timeZone"`},
		{"create unknown time zone", &fakeStore{}, http.MethodPost, `{"code":"US01","name":"n","country":"US","currency":"USD","timeZone":"Mars/Base"}`, allDuties, true, http.StatusBadRequest, `"timeZone"`},
		{"create local time zone", &fakeStore{}, http.MethodPost, `{"code":"US01","name":"n","country":"US","currency":"USD","timeZone":"Local"}`, allDuties, true, http.StatusBadRequest, `"timeZone"`},
		{"create numeric time zone", &fakeStore{}, http.MethodPost, `{"code":"US01","name":"n","country":"US","currency":"USD","timeZone":5}`, allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"create blank name", &fakeStore{}, http.MethodPost, `{"code":"US01","name":"  ","country":"US","currency":"USD","timeZone":"UTC"}`, allDuties, true, http.StatusBadRequest, `"name"`},
		{"create store error hides detail", &fakeStore{err: errors.New("pg secret detail")}, http.MethodPost, validBody, allDuties, true, http.StatusInternalServerError, "COM-0500"},
		{"create duplicate code", &fakeStore{err: httpx.NewProblem(http.StatusConflict, store.CodeLegalEntityCode, "exists")}, http.MethodPost, validBody, allDuties, true, http.StatusConflict, store.CodeLegalEntityCode},
		{"create without duty", &fakeStore{}, http.MethodPost, validBody, "org.read", true, http.StatusForbidden, "AUTH-0003"},
		{"list empty is array", &fakeStore{}, http.MethodGet, "", allDuties, true, http.StatusOK, `"items":[]`},
		{"list store error", &fakeStore{err: errors.New("boom")}, http.MethodGet, "", allDuties, true, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, legalEntities, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("internal detail leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestCreateLegalEntity_ReplayReturnsOriginal(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	post := func(body string) *httptest.ResponseRecorder {
		return do(r, http.MethodPost, legalEntities, body, allDuties, true, "Idempotency-Key", "key-1")
	}
	first, replay := post(validBody), post(validBody)
	if first.Code != http.StatusCreated || replay.Code != http.StatusCreated || first.Body.String() != replay.Body.String() || len(f.entities) != 1 {
		t.Fatalf("replay must return the original entity: %d %s / %d %s, rows %d", first.Code, first.Body, replay.Code, replay.Body, len(f.entities))
	}
}

func TestLegalEntities_InvalidLegalEntityHeader(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		rec := do(r, method, legalEntities, validBody, allDuties, true, "X-Passage-Legal-Entity", "foo")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
			t.Fatalf("%s: got %d %s; want 400 COM-0001", method, rec.Code, rec.Body.String())
		}
	}
}

func TestConvert(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"kilograms to grams", "from=kg&to=g&qty=2.5", true, http.StatusOK, `"quantity":"2500"`},
		{"offset conversion", "from=C&to=F&qty=100", true, http.StatusOK, `"quantity":"212"`},
		{"without tenant", "from=kg&to=g&qty=1", false, http.StatusUnauthorized, "AUTH-0001"},
		{"missing qty", "from=kg&to=g", true, http.StatusBadRequest, "COM-0001"},
		{"non numeric qty", "from=kg&to=g&qty=abc", true, http.StatusBadRequest, "COM-0001"},
		{"huge exponent qty", "from=kg&to=g&qty=1e999", true, http.StatusBadRequest, "COM-0001"},
		{"missing from", "to=g&qty=1", true, http.StatusBadRequest, "COM-0001"},
		{"unknown unit", "from=kg&to=furlong&qty=1", true, http.StatusUnprocessableEntity, "ORG-UOM-NO-PATH"},
		{"cross class", "from=kg&to=m&qty=1", true, http.StatusUnprocessableEntity, "ORG-UOM-NO-PATH"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{})
			rec := do(r, http.MethodGet, "/v1/org/uom/convert?"+tc.query, "", "", tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestProbes(t *testing.T) {
	down := &fakeStore{pingErr: errors.New("db down")}
	tests := []struct {
		name       string
		store      *fakeStore
		path       string
		wantStatus int
	}{
		{"readyz ok", &fakeStore{}, "/readyz", http.StatusOK},
		{"readyz fails closed when database is down", down, "/readyz", http.StatusServiceUnavailable},
		{"healthz stays up when database is down", down, "/healthz", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, http.MethodGet, tc.path, "", "", false)
			if rec.Code != tc.wantStatus {
				t.Fatalf("got %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusServiceUnavailable && !strings.Contains(rec.Body.String(), `"status":"unavailable"`) {
				t.Fatalf("unexpected body %s", rec.Body)
			}
		})
	}
}

func TestMetricsCountBusinessOperations(t *testing.T) {
	f := &fakeStore{}
	r, a := newRouter(f)
	do(r, http.MethodPost, legalEntities, validBody, allDuties, true)
	f.err = errors.New("boom")
	do(r, http.MethodPost, legalEntities, validBody, allDuties, true)

	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		"# TYPE org_legal_entities_created_total counter",
		"org_legal_entities_created_total 1",
		"org_legal_entities_failed_total 1",
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body.String())
		}
	}
}

func TestOpenAPIDocumentsEveryRoute(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newRouter(&fakeStore{})
	_ = chi.Walk(r, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.Contains(string(spec), "\n  "+route+":") {
			t.Errorf("openapi.yaml does not document %s", route)
		}
		return nil
	})
}

func TestAsyncAPIDeclaresEveryEvent(t *testing.T) {
	spec, err := os.ReadFile("../../events/asyncapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	for _, m := range regexp.MustCompile(`x-passage-type: (\S+)`).FindAllStringSubmatch(string(spec), -1) {
		declared[m[1]] = true
	}
	for _, typ := range orgevents.All() {
		if !pevents.ValidType(typ) || !declared[typ] {
			t.Errorf("%s must be valid and declared in asyncapi.yaml", typ)
		}
		delete(declared, typ)
	}
	for typ := range declared {
		t.Errorf("asyncapi.yaml declares %s which events.All() does not list", typ)
	}
}

func TestScopedCallerCannotCreateLegalEntity(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	rec := do(r, http.MethodPost, legalEntities, validBody, allDuties, true, "X-Passage-Legal-Entity", testkit.TenantID(2))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
		t.Fatalf("got %d %s; want 403 AUTH-0003", rec.Code, rec.Body.String())
	}
}

func TestListLegalEntitiesPagesByCursor(t *testing.T) {
	f := &fakeStore{entities: []store.LegalEntity{{Code: "A1"}, {Code: "B2"}, {Code: "C3"}}}
	r, _ := newRouter(f)
	first := do(r, http.MethodGet, legalEntities+"?limit=2", "", allDuties, true)
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"B2"`) || strings.Contains(first.Body.String(), `"C3"`) || !strings.Contains(first.Body.String(), `"nextCursor":"QjI"`) {
		t.Fatalf("first page = %d %s", first.Code, first.Body.String())
	}
	second := do(r, http.MethodGet, legalEntities+"?limit=2&cursor=QjI", "", allDuties, true)
	if !strings.Contains(second.Body.String(), `"C3"`) || strings.Contains(second.Body.String(), "nextCursor") {
		t.Fatalf("second page = %d %s", second.Code, second.Body.String())
	}
	for _, bad := range []string{"?limit=0", "?limit=101", "?limit=x", "?cursor=!!"} {
		if rec := do(r, http.MethodGet, legalEntities+bad, "", allDuties, true); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d; want 400", bad, rec.Code)
		}
	}
}
