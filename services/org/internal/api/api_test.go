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
	refs     map[string]store.ReferenceItem
	getCalls int
}

func (f *fakeStore) reference(kind store.ReferenceKind, code string) (store.ReferenceItem, bool) {
	if f.refs == nil {
		minor0, minor2, yes := 0, 2, true
		f.refs = map[string]store.ReferenceItem{
			"currency/USD": {Code: "USD", Name: "US Dollar", NumericCode: "840", MinorUnits: &minor2, Status: "active"},
			"currency/JPY": {Code: "JPY", Name: "Yen", NumericCode: "392", MinorUnits: &minor0, Status: "active"},
			"country/US":   {Code: "US", Name: "United States", RegionRequired: &yes, Status: "active"},
			"region/US-CA": {Code: "US-CA", Name: "California", CountryCode: "US", Status: "active"},
			"language/en":  {Code: "en", Name: "English", Status: "active"},
		}
	}
	item, ok := f.refs[string(kind)+"/"+code]
	return item, ok
}

func (f *fakeStore) ListReference(_ context.Context, kind store.ReferenceKind, filter store.ReferenceFilter) ([]store.ReferenceItem, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.reference(kind, "")
	items := []store.ReferenceItem{}
	for key, item := range f.refs {
		if strings.HasPrefix(key, string(kind)+"/") && (filter.Status == "" || item.Status == filter.Status) && (filter.Country == "" || item.CountryCode == filter.Country) {
			items = append(items, item)
		}
	}
	return items, nil
}

func (f *fakeStore) GetReference(_ context.Context, kind store.ReferenceKind, code string) (store.ReferenceItem, error) {
	if f.err != nil {
		return store.ReferenceItem{}, f.err
	}
	item, ok := f.reference(kind, code)
	if !ok {
		return item, httpx.NewProblem(http.StatusNotFound, store.CodeReferenceNotFound, "unknown code")
	}
	return item, nil
}

func (f *fakeStore) SetReferenceStatus(ctx context.Context, kind store.ReferenceKind, code, status string) (store.ReferenceItem, error) {
	item, err := f.GetReference(ctx, kind, code)
	if err != nil {
		return item, err
	}
	item.Status = status
	f.refs[string(kind)+"/"+code] = item
	return item, nil
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

func (f *fakeStore) find(id string) (int, bool) {
	for i, e := range f.entities {
		if e.ID == id {
			return i, true
		}
	}
	return 0, false
}

func (f *fakeStore) GetLegalEntity(_ context.Context, id string) (store.LegalEntity, error) {
	f.getCalls++
	if f.err != nil {
		return store.LegalEntity{}, f.err
	}
	i, ok := f.find(id)
	if !ok {
		return store.LegalEntity{}, httpx.NewProblem(http.StatusNotFound, store.CodeLegalEntityNotFound, "legal entity not found")
	}
	return f.entities[i], nil
}

func (f *fakeStore) UpdateLegalEntity(_ context.Context, id string, version int, in store.LegalEntityPatch) (store.LegalEntity, error) {
	e, err := f.GetLegalEntity(context.Background(), id)
	if err != nil {
		return e, err
	}
	if e.Version != version {
		return store.LegalEntity{}, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, "current version is 1")
	}
	if in.Name != nil && *in.Name != e.Name {
		i, _ := f.find(id)
		f.entities[i].Name = *in.Name
		f.entities[i].Version++
		return f.entities[i], nil
	}
	return e, nil
}

func (f *fakeStore) ListLegalEntityNames(_ context.Context, id string) ([]store.LegalEntityName, error) {
	if _, err := f.GetLegalEntity(context.Background(), id); err != nil {
		return nil, err
	}
	return []store.LegalEntityName{{Name: "Passage US", ValidFrom: time.Unix(0, 0).UTC()}}, nil
}

func (f *fakeStore) ListLegalEntities(_ context.Context, _ store.LegalEntityFilter, after string, limit int) (store.LegalEntityPage, error) {
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
	legalEntities   = "/v1/org/legal-entities"
	allDuties       = "org.read,org.legal-entity.write"
	referenceDuties = "org.read,org.reference.write"
	validBody       = `{"code":"US01","name":"Passage US","country":"US","currency":"USD","timeZone":"UTC"}`
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

const (
	knownID   = "0190b6c0-0000-7000-8000-000000000001"
	unknownID = "0190b6c0-0000-7000-8000-000000000002"
)

func seededStore() *fakeStore {
	return &fakeStore{entities: []store.LegalEntity{{ID: knownID, Code: "US01", Name: "Passage US", Version: 1}}}
}

func TestLegalEntityLifecycleRoutes(t *testing.T) {
	byID := legalEntities + "/" + knownID
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
		{"get ok", seededStore(), http.MethodGet, byID, "", allDuties, true, http.StatusOK, `"code":"US01"`},
		{"get unknown id", seededStore(), http.MethodGet, legalEntities + "/" + unknownID, "", allDuties, true, http.StatusNotFound, "ORG-LE-NOT-FOUND"},
		{"get without tenant", seededStore(), http.MethodGet, byID, "", "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"get without duty", seededStore(), http.MethodGet, byID, "", "", true, http.StatusForbidden, "AUTH-0003"},
		{"patch name", seededStore(), http.MethodPatch, byID, `{"version":1,"name":"Passage USA"}`, allDuties, true, http.StatusOK, `"version":2`},
		{"patch no change keeps version", seededStore(), http.MethodPatch, byID, `{"version":1,"name":"Passage US"}`, allDuties, true, http.StatusOK, `"version":1`},
		{"patch unknown field code", seededStore(), http.MethodPatch, byID, `{"version":1,"code":"X1"}`, allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"patch missing version", seededStore(), http.MethodPatch, byID, `{"name":"x"}`, allDuties, true, http.StatusBadRequest, `"version"`},
		{"patch bad status", seededStore(), http.MethodPatch, byID, `{"version":1,"status":"deleted"}`, allDuties, true, http.StatusBadRequest, `"status"`},
		{"patch blank name", seededStore(), http.MethodPatch, byID, `{"version":1,"name":"  "}`, allDuties, true, http.StatusBadRequest, `"name"`},
		{"patch bad time zone", seededStore(), http.MethodPatch, byID, `{"version":1,"timeZone":"Local"}`, allDuties, true, http.StatusBadRequest, `"timeZone"`},
		{"patch long registration number", seededStore(), http.MethodPatch, byID, `{"version":1,"registrationNumber":"` + strings.Repeat("9", 41) + `"}`, allDuties, true, http.StatusBadRequest, `"registrationNumber"`},
		{"patch stale version", seededStore(), http.MethodPatch, byID, `{"version":7,"name":"x"}`, allDuties, true, http.StatusConflict, "ORG-VERSION-CONFLICT"},
		{"patch unknown id", seededStore(), http.MethodPatch, legalEntities + "/" + unknownID, `{"version":1}`, allDuties, true, http.StatusNotFound, "ORG-LE-NOT-FOUND"},
		{"patch without write duty", seededStore(), http.MethodPatch, byID, `{"version":1}`, "org.read", true, http.StatusForbidden, "AUTH-0003"},
		{"patch store error hides detail", &fakeStore{err: errors.New("pg secret detail")}, http.MethodPatch, byID, `{"version":1}`, allDuties, true, http.StatusInternalServerError, "COM-0500"},
		{"names", seededStore(), http.MethodGet, byID + "/names", "", allDuties, true, http.StatusOK, `"items":[{"name":"Passage US"`},
		{"names unknown id", seededStore(), http.MethodGet, legalEntities + "/" + unknownID + "/names", "", allDuties, true, http.StatusNotFound, "ORG-LE-NOT-FOUND"},
		{"names without duty", seededStore(), http.MethodGet, byID + "/names", "", "", true, http.StatusForbidden, "AUTH-0003"},
		{"list bad status", &fakeStore{}, http.MethodGet, legalEntities + "?status=bogus", "", allDuties, true, http.StatusBadRequest, "COM-0001"},
		{"list valid filters", &fakeStore{}, http.MethodGet, legalEntities + "?status=active&country=US&currency=USD", "", allDuties, true, http.StatusOK, `"items":[]`},
		{"create reserved code", &fakeStore{}, http.MethodPost, legalEntities, `{"code":"ALL","name":"n","country":"US","currency":"USD","timeZone":"UTC"}`, allDuties, true, http.StatusBadRequest, "ORG-LE-CODE-RESERVED"},
		{"create reserved code in lower case", &fakeStore{}, http.MethodPost, legalEntities, `{"code":"sys","name":"n","country":"US","currency":"USD","timeZone":"UTC"}`, allDuties, true, http.StatusBadRequest, "ORG-LE-CODE-RESERVED"},
		{"create bad default language", &fakeStore{}, http.MethodPost, legalEntities, `{"code":"US01","name":"n","country":"US","currency":"USD","timeZone":"UTC","defaultLanguage":"english"}`, allDuties, true, http.StatusBadRequest, `"defaultLanguage"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("internal detail leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestLegalEntityMalformedIDNeverReachesStore(t *testing.T) {
	f := seededStore()
	r, _ := newRouter(f)
	do(r, http.MethodGet, legalEntities+"/nope", "", allDuties, true)
	do(r, http.MethodPatch, legalEntities+"/nope", `{"version":1}`, allDuties, true)
	do(r, http.MethodGet, legalEntities+"/nope/names", "", allDuties, true)
	if f.getCalls != 0 {
		t.Fatalf("store was called %d times for a malformed id", f.getCalls)
	}
}

func TestCreateLegalEntity_NormalisesCodeAndDefaultsLanguage(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	rec := do(r, http.MethodPost, legalEntities, `{"code":"us01","name":"Passage US","country":"US","currency":"USD","timeZone":"UTC","registrationNumber":"R-1"}`, allDuties, true)
	if rec.Code != http.StatusCreated || len(f.entities) != 1 {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if got := f.entities[0]; got.Code != "US01" || got.DefaultLanguage != "en-US" || got.RegistrationNumber == nil || *got.RegistrationNumber != "R-1" {
		t.Fatalf("stored entity = %+v; want code US01, language en-US, registration R-1", got)
	}
}

func TestLegalEntityUpdatedCounterCountsOnlyVersionBumps(t *testing.T) {
	r, a := newRouter(seededStore())
	byID := legalEntities + "/" + knownID
	do(r, http.MethodPatch, byID, `{"version":1,"name":"Passage US"}`, allDuties, true)
	do(r, http.MethodPatch, byID, `{"version":9,"name":"Other"}`, allDuties, true)
	do(r, http.MethodPatch, byID, `{"version":1,"name":"Passage USA"}`, allDuties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"# TYPE org_legal_entities_updated_total counter", "org_legal_entities_updated_total 1"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body.String())
		}
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

func TestReference(t *testing.T) {
	tests := []struct {
		name       string
		store      *fakeStore
		method     string
		path       string
		body       string
		duties     string
		withTenant bool
		headers    []string
		wantStatus int
		wantBody   string
	}{
		{"list countries", &fakeStore{}, http.MethodGet, "/v1/org/countries", "", referenceDuties, true, nil, http.StatusOK, `"items":[{"code":"US"`},
		{"list currencies", &fakeStore{}, http.MethodGet, "/v1/org/currencies?status=active", "", referenceDuties, true, nil, http.StatusOK, `"numericCode":"840"`},
		{"list regions by country", &fakeStore{}, http.MethodGet, "/v1/org/regions?country=US", "", referenceDuties, true, nil, http.StatusOK, `"countryCode":"US"`},
		{"list regions with no match is empty array", &fakeStore{}, http.MethodGet, "/v1/org/regions?country=ZZ", "", referenceDuties, true, nil, http.StatusOK, `"items":[]`},
		{"list languages", &fakeStore{}, http.MethodGet, "/v1/org/languages", "", referenceDuties, true, nil, http.StatusOK, `"code":"en"`},
		{"list bad status", &fakeStore{}, http.MethodGet, "/v1/org/currencies?status=bogus", "", referenceDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"list bad country shape", &fakeStore{}, http.MethodGet, "/v1/org/regions?country=usa", "", referenceDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"list country on non-region collection", &fakeStore{}, http.MethodGet, "/v1/org/currencies?country=US", "", referenceDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"list without tenant", &fakeStore{}, http.MethodGet, "/v1/org/countries", "", "", false, nil, http.StatusUnauthorized, "AUTH-0001"},
		{"list without duty", &fakeStore{}, http.MethodGet, "/v1/org/countries", "", "", true, nil, http.StatusForbidden, "AUTH-0003"},
		{"list store error hides detail", &fakeStore{err: errors.New("pg secret detail")}, http.MethodGet, "/v1/org/countries", "", referenceDuties, true, nil, http.StatusInternalServerError, "COM-0500"},
		{"get JPY has zero minor units", &fakeStore{}, http.MethodGet, "/v1/org/currencies/JPY", "", referenceDuties, true, nil, http.StatusOK, `"minorUnits":0`},
		{"get unknown code", &fakeStore{}, http.MethodGet, "/v1/org/currencies/ZZZ", "", referenceDuties, true, nil, http.StatusNotFound, store.CodeReferenceNotFound},
		{"get without tenant", &fakeStore{}, http.MethodGet, "/v1/org/currencies/USD", "", "", false, nil, http.StatusUnauthorized, "AUTH-0001"},
		{"get store error hides detail", &fakeStore{err: errors.New("pg secret detail")}, http.MethodGet, "/v1/org/currencies/USD", "", referenceDuties, true, nil, http.StatusInternalServerError, "COM-0500"},
		{"patch deactivates", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, referenceDuties, true, nil, http.StatusOK, `"status":"inactive"`},
		{"patch repeat is idempotent", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"active"}`, referenceDuties, true, nil, http.StatusOK, `"status":"active"`},
		{"patch invalid status", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"deleted"}`, referenceDuties, true, nil, http.StatusBadRequest, `"status"`},
		{"patch empty object", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{}`, referenceDuties, true, nil, http.StatusBadRequest, `"status"`},
		{"patch wrong type", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":5}`, referenceDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"patch unknown field", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"active","x":1}`, referenceDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"patch malformed", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{`, referenceDuties, true, nil, http.StatusBadRequest, "COM-0001"},
		{"patch without write duty", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, "org.read", true, nil, http.StatusForbidden, "AUTH-0003"},
		{"patch legal-entity scoped", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, referenceDuties, true, []string{"X-Passage-Legal-Entity", testkit.TenantID(2)}, http.StatusForbidden, "AUTH-0003"},
		{"patch without tenant", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, "", false, nil, http.StatusUnauthorized, "AUTH-0001"},
		{"patch unknown code", &fakeStore{}, http.MethodPatch, "/v1/org/currencies/ZZZ", `{"status":"inactive"}`, referenceDuties, true, nil, http.StatusNotFound, store.CodeReferenceNotFound},
		{"patch store error hides detail", &fakeStore{err: errors.New("pg secret detail")}, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, referenceDuties, true, nil, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant, tc.headers...)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("internal detail leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestReference_PatchCountsOnlySuccessfulChanges(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	do(r, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, referenceDuties, true)
	do(r, http.MethodPatch, "/v1/org/currencies/ZZZ", `{"status":"inactive"}`, referenceDuties, true)
	do(r, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"deleted"}`, referenceDuties, true)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"# TYPE org_reference_status_changed_total counter", "org_reference_status_changed_total 1"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body.String())
		}
	}
}

func TestReference_DeactivatedRowIsFilteredByStatus(t *testing.T) {
	r, _ := newRouter(&fakeStore{})
	do(r, http.MethodPatch, "/v1/org/currencies/USD", `{"status":"inactive"}`, referenceDuties, true)
	rec := do(r, http.MethodGet, "/v1/org/currencies?status=inactive", "", referenceDuties, true)
	if !strings.Contains(rec.Body.String(), `"code":"USD"`) || strings.Contains(rec.Body.String(), `"code":"JPY"`) {
		t.Fatalf("inactive list = %s", rec.Body.String())
	}
}
