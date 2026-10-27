package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/api"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	legalEntity = "00000000-0000-0000-0001-000000000001"
	allDuties   = "pricing.view,pricing.maintain"
)

// fakeStore mirrors the Postgres store's rules (overlap via domain.Conflict, state, version).
type fakeStore struct {
	mu       sync.Mutex
	records  map[string]store.Entry
	seq      int
	keys     []string // Idempotency-Key values received by lifecycle commands
	lastList store.Filter
	pingErr  error
}

func newFake() *fakeStore { return &fakeStore{records: map[string]store.Entry{}} }

func (f *fakeStore) CreateRecord(_ context.Context, in store.Entry, _ string) (store.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seq++
	in.ID = fmt.Sprintf("00000000-0000-0000-0002-%012d", f.seq)
	in.Status, in.Version = domain.StatusDraft, 1
	f.records[in.ID] = in
	return in, nil
}

func (f *fakeStore) GetRecord(_ context.Context, id string) (store.Entry, error) {
	e, ok := f.records[id]
	if !ok {
		return store.Entry{}, httpx.NewProblem(http.StatusNotFound, store.CodeRecordNotFound, "no condition record "+id)
	}
	return e, nil
}

func (f *fakeStore) ListRecords(_ context.Context, filter store.Filter) ([]store.Entry, error) {
	f.lastList = filter
	var out []store.Entry
	for _, e := range f.records {
		if filter.Status == "" || e.Status == filter.Status {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeStore) ActiveRecords(_ context.Context, le, currency string, asOf time.Time) ([]domain.Record, error) {
	var out []domain.Record
	for _, e := range f.records {
		if e.LegalEntityID == le && e.Currency == currency && e.Status == domain.StatusActive &&
			!asOf.Before(e.ValidFrom) && !asOf.After(e.ValidTo) {
			out = append(out, e.Record)
		}
	}
	return out, nil
}

func (f *fakeStore) ActivateRecord(_ context.Context, id string, version int, key string) (store.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	cur, err := f.GetRecord(context.Background(), id)
	if err != nil {
		return store.Entry{}, err
	}
	if cur.Version != version {
		return store.Entry{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "record version does not match")
	}
	if cur.Status != domain.StatusDraft {
		return store.Entry{}, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "only DRAFT records can be activated")
	}
	var active []domain.Record
	for _, e := range f.records {
		if e.LegalEntityID == cur.LegalEntityID && e.Status == domain.StatusActive {
			active = append(active, e.Record)
		}
	}
	if c, clash := domain.Conflict(cur.Record, active); clash {
		return store.Entry{}, httpx.NewProblem(http.StatusConflict, domain.CodeOverlap, "overlapping active record "+c.ID+" for the same key, currency and unit")
	}
	cur.Status, cur.Version = domain.StatusActive, cur.Version+1
	f.records[id] = cur
	return cur, nil
}

func (f *fakeStore) WithdrawRecord(_ context.Context, id string, version int, _, key string) (store.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	cur, err := f.GetRecord(context.Background(), id)
	if err != nil {
		return store.Entry{}, err
	}
	if cur.Version != version {
		return store.Entry{}, httpx.NewProblem(http.StatusPreconditionFailed, store.CodeVersionConflict, "record version does not match")
	}
	if cur.Status != domain.StatusActive {
		return store.Entry{}, httpx.NewProblem(http.StatusConflict, store.CodeStateInvalid, "only ACTIVE records can be withdrawn")
	}
	cur.Status, cur.Version = domain.StatusWithdrawn, version+1
	f.records[id] = cur
	return cur, nil
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func router(s store.Store) http.Handler {
	r := httpx.NewRouter()
	api.New(s, api.Config{}).Register(r)
	return r
}

func call(t *testing.T, h http.Handler, method, path, duties, body string, headers ...string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	if duties != "" {
		req.Header.Set("X-Passage-Duties", duties)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

const recordBody = `{"legalEntityId":"%s","type":"PRICE","customerKind":"customer","customerKey":"C1","productKind":"product","productKey":"P1",
	"currency":"USD","fromQuantity":"0","amount":"%s","validFrom":"2026-01-01","validTo":"2026-12-31"}`

const resolveBody = `{"legalEntityId":"%s","customerId":"C1","currency":"USD","pricingDate":"2026-06-01",
	"lines":[{"productId":"P1","unit":"EA","quantity":"%s"}]}`

func TestSimulate_ExplainsTheAccessSequence(t *testing.T) {
	// AC-26.2 over HTTP: search_next on access 2 keeps the lower access 4 price, both traced.
	body := `{"customerId":"C1","customerGroupId":"PG-A","currency":"USD","pricingDate":"2026-06-01",
		"lines":[{"productId":"P1","unit":"EA","quantity":"1"}],
		"records":[
			{"id":"r2","type":"PRICE","customerKind":"customer","customerKey":"C1","productKind":"product","productKey":"P1","currency":"USD","amount":"95.00","validFrom":"2026-01-01","validTo":"2026-12-31","searchNext":true},
			{"id":"r4","type":"PRICE","customerKind":"customer_group","customerKey":"PG-A","productKind":"product","productKey":"P1","currency":"USD","amount":"90.00","validFrom":"2026-01-01","validTo":"2026-12-31"}]}`
	code, out := call(t, router(newFake()), http.MethodPost, "/v1/pricing/simulate", allDuties, body)
	if code != http.StatusOK {
		t.Fatalf("status %d: %v", code, out)
	}
	line := out["lines"].([]any)[0].(map[string]any)
	if line["unitPrice"] != "90" || line["lineNet"] != "90" {
		t.Fatalf("line = %v; want 90", line)
	}
	if ref, _ := out["explainRef"].(string); !strings.HasPrefix(ref, "sha256:") || line["trace"] == nil {
		t.Fatalf("explain must carry a sha256 reference and the trace, got ref %q", ref)
	}
}

func TestResolve_ActivatedRecordPricesTheLine(t *testing.T) {
	fs := newFake()
	h := router(fs)
	code, created := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, fmt.Sprintf(recordBody, legalEntity, "95.00"))
	if code != http.StatusCreated {
		t.Fatalf("create status %d: %v", code, created)
	}
	id := created["id"].(string)
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+id+":activate", allDuties, `{"version":1}`); code != http.StatusOK {
		t.Fatalf("activate status %d", code)
	}
	code, out := call(t, h, http.MethodPost, "/v1/pricing/resolve?explain=true", allDuties, fmt.Sprintf(resolveBody, legalEntity, "2"))
	if code != http.StatusOK {
		t.Fatalf("resolve status %d: %v", code, out)
	}
	if line := out["lines"].([]any)[0].(map[string]any); line["lineNet"] != "190" {
		t.Fatalf("line net = %v; want 190", line["lineNet"])
	}
}

func TestResolve_ValidationAndDuties(t *testing.T) {
	h := router(newFake())
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/resolve", allDuties, fmt.Sprintf(resolveBody, legalEntity, "0")); code != http.StatusUnprocessableEntity {
		t.Fatalf("zero quantity must be 422, got %d %v", code, out)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/resolve", "", fmt.Sprintf(resolveBody, legalEntity, "1")); code != http.StatusForbidden || out["code"] != "AUTH-0003" {
		t.Fatalf("missing pricing.view must be 403, got %d %v", code, out)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/condition-records", "pricing.view", fmt.Sprintf(recordBody, legalEntity, "1")); code != http.StatusForbidden {
		t.Fatalf("a viewer must not maintain records, got %d", code)
	}
}

func TestCreateRecord_NegativeAmountIsPRC0003(t *testing.T) {
	code, out := call(t, router(newFake()), http.MethodPost, "/v1/pricing/condition-records", allDuties, fmt.Sprintf(recordBody, legalEntity, "-1"))
	if code != http.StatusUnprocessableEntity || out["code"] != domain.CodeNegative {
		t.Fatalf("negative amount = %d %v; want 422 %s", code, out, domain.CodeNegative)
	}
}

func TestActivate_OverlapIs409NamingTheConflict(t *testing.T) {
	fs := newFake()
	h := router(fs)
	var ids []string
	for i := 0; i < 2; i++ {
		_, created := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, fmt.Sprintf(recordBody, legalEntity, "5"))
		ids = append(ids, created["id"].(string))
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+ids[0]+":activate", allDuties, `{"version":1}`); code != http.StatusOK {
		t.Fatalf("first activation must succeed, got %d", code)
	}
	code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+ids[1]+":activate", allDuties, `{"version":1}`)
	if code != http.StatusConflict || out["code"] != domain.CodeOverlap || !strings.Contains(out["detail"].(string), ids[0]) {
		t.Fatalf("overlap must be 409 %s naming %s, got %d %v", domain.CodeOverlap, ids[0], code, out)
	}
}

func TestRecordRoutes_RejectMalformedIDsAndTenants(t *testing.T) {
	h := router(newFake())
	if code, _ := call(t, h, http.MethodGet, "/v1/pricing/condition-records/not-a-uuid", allDuties, ""); code != http.StatusNotFound {
		t.Fatalf("malformed id must be 404, got %d", code)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/pricing/resolve", strings.NewReader("{}"))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing tenant must be 401, got %d", rec.Code)
	}
}

func TestMetricsExposesCounters(t *testing.T) {
	a := api.New(newFake(), api.Config{})
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(rec.Body.String(), "pricing_resolves_total 0") {
		t.Fatalf("metrics missing counters: %s", rec.Body.String())
	}
}

func TestRecordLifecycle_OverHTTP(t *testing.T) {
	fs := newFake()
	h := router(fs)
	_, created := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, fmt.Sprintf(recordBody, legalEntity, "5"))
	id := created["id"].(string)
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+id+":withdraw", allDuties, `{"version":1}`); code != http.StatusConflict || out["code"] != store.CodeStateInvalid {
		t.Fatalf("withdrawing a draft = %d %v; want 409 %s", code, out, store.CodeStateInvalid)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+id+":activate", allDuties, `{}`); code != http.StatusUnprocessableEntity || out["code"] != "COM-0001" {
		t.Fatalf("activate without a version = %d %v; want 422", code, out)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+id+":activate", allDuties, `{"version":1}`, "Idempotency-Key", "act-1"); code != http.StatusOK {
		t.Fatalf("activate = %d", code)
	}
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+id+":activate", allDuties, `{"version":1}`); code != http.StatusPreconditionFailed || out["code"] != store.CodeVersionConflict {
		t.Fatalf("stale version = %d %v; want 412 %s", code, out, store.CodeVersionConflict)
	}
	code, out := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+id+":withdraw", allDuties, `{"version":2,"reason":"superseded"}`, "Idempotency-Key", "wd-1")
	if code != http.StatusOK || out["status"] != domain.StatusWithdrawn || out["version"] != float64(3) {
		t.Fatalf("withdraw = %d %v; want WITHDRAWN version 3", code, out)
	}
	if fmt.Sprint(fs.keys) != fmt.Sprint([]string{"", "act-1", "", "wd-1"}) {
		t.Fatalf("Idempotency-Key must reach the store, got %v", fs.keys)
	}
	code, got := call(t, h, http.MethodGet, "/v1/pricing/condition-records/"+id, "pricing.view", "")
	if code != http.StatusOK || got["status"] != domain.StatusWithdrawn || got["amount"] != "5" || got["validFrom"] != "2026-01-01" {
		t.Fatalf("get = %d %v", code, got)
	}
	if code, _ := call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+id+":withdraw", "pricing.view", `{"version":3}`); code != http.StatusForbidden {
		t.Fatalf("a viewer must not withdraw, got %d", code)
	}
}

func TestListRecords_FiltersAndValidation(t *testing.T) {
	fs := newFake()
	h := router(fs)
	call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, fmt.Sprintf(recordBody, legalEntity, "5"))
	code, out := call(t, h, http.MethodGet, "/v1/pricing/condition-records?legalEntityId="+legalEntity+"&status=DRAFT&type=PRICE&customer=C1&product=P1&asOf=2026-03-01&limit=10", "pricing.view", "")
	if code != http.StatusOK || len(out["items"].([]any)) != 1 {
		t.Fatalf("list = %d %v; want one item", code, out)
	}
	f := fs.lastList
	if f.LegalEntityID != legalEntity || f.Status != "DRAFT" || f.Type != "PRICE" || f.Customer != "C1" || f.Product != "P1" || f.Limit != 10 || f.AsOf == nil {
		t.Fatalf("filter = %+v; want every query parameter passed to the store", f)
	}
	code, out = call(t, h, http.MethodGet, "/v1/pricing/condition-records?status=OPEN&type=FREIGHT&asOf=03/01/2026&limit=0&legalEntityId=x", "pricing.view", "")
	errs, _ := out["errors"].(map[string]any)
	if code != http.StatusUnprocessableEntity || len(errs) != 5 {
		t.Fatalf("bad filters = %d %v; want 422 with five field errors", code, out)
	}
}

func TestCreateRecord_ReportsEveryFieldError(t *testing.T) {
	body := `{"legalEntityId":"nope","type":"PRICE","customerKind":"customer","productKind":"product","productKey":"P1","currency":"usd","validFrom":"2026-02-01","validTo":"2026-01-01"}`
	code, out := call(t, router(newFake()), http.MethodPost, "/v1/pricing/condition-records", allDuties, body)
	errs, _ := out["errors"].(map[string]any)
	for _, field := range []string{"legalEntityId", "customerKey", "currency", "validity"} {
		if errs[field] == nil {
			t.Fatalf("missing %s error in %d %v", field, code, out)
		}
	}
}

func TestResolve_RejectsTooManyLinesAndBadLegalEntity(t *testing.T) {
	h := httpx.NewRouter()
	api.New(newFake(), api.Config{MaxLines: 1}).Register(h)
	two := `{"legalEntityId":"` + legalEntity + `","customerId":"C1","currency":"USD","pricingDate":"2026-06-01","lines":[{"productId":"P1","unit":"EA","quantity":"1"},{"productId":"P1","unit":"EA","quantity":"1"}]}`
	if code, out := call(t, h, http.MethodPost, "/v1/pricing/resolve", allDuties, two); code != http.StatusUnprocessableEntity || out["code"] != "PRC-0009" {
		t.Fatalf("two lines over a limit of one = %d %v; want 422 PRC-0009", code, out)
	}
	code, out := call(t, h, http.MethodPost, "/v1/pricing/resolve", allDuties, `{"customerId":"C1","currency":"USD","pricingDate":"2026-06-01","lines":[{"productId":"P1","unit":"EA","quantity":"1"}]}`)
	if errs, _ := out["errors"].(map[string]any); code != http.StatusUnprocessableEntity || errs["legalEntityId"] == nil {
		t.Fatalf("missing legal entity = %d %v; want a legalEntityId field error", code, out)
	}
}

func TestResolve_ExplainIsOptIn(t *testing.T) {
	fs := newFake()
	h := router(fs)
	_, created := call(t, h, http.MethodPost, "/v1/pricing/condition-records", allDuties, fmt.Sprintf(recordBody, legalEntity, "95.00"))
	call(t, h, http.MethodPost, "/v1/pricing/condition-records/"+created["id"].(string)+":activate", allDuties, `{"version":1}`)
	_, plain := call(t, h, http.MethodPost, "/v1/pricing/resolve", allDuties, fmt.Sprintf(resolveBody, legalEntity, "1"))
	_, explained := call(t, h, http.MethodPost, "/v1/pricing/resolve?explain=true", allDuties, fmt.Sprintf(resolveBody, legalEntity, "1"))
	if plain["lines"].([]any)[0].(map[string]any)["trace"] != nil {
		t.Fatal("trace must be omitted unless explain=true")
	}
	if explained["lines"].([]any)[0].(map[string]any)["trace"] == nil || plain["explainRef"] != explained["explainRef"] {
		t.Fatalf("explain=true must add the trace and keep the same reference: %v vs %v", plain["explainRef"], explained["explainRef"])
	}
}

func TestReadyz_ReflectsTheDatabase(t *testing.T) {
	fs := newFake()
	h := router(fs)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ready = %d", rec.Code)
	}
	fs.pingErr = errors.New("connection refused")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready with the database down = %d; want 503", rec.Code)
	}
}
