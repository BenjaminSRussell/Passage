package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fx/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

func (f *fakeStore) PostRates(_ context.Context, in []store.RateInput) ([]store.Posted, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.mu.Lock()
	f.posts = append(f.posts, in)
	f.mu.Unlock()
	out := make([]store.Posted, len(in))
	for i := range in {
		out[i] = store.Posted{ID: "rate-" + in[i].To, Version: 1}
	}
	return out, nil
}

func (f *fakeStore) CurrentBook(context.Context, time.Time) (*domain.Book, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.book, nil
}

func (f *fakeStore) History(context.Context, string, string, string) ([]store.HistoryEntry, error) {
	return f.history, f.err
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func idemKey(ctx context.Context, key string) string {
	p, _ := tenant.FromContext(ctx)
	return p.TenantID + "/" + key
}

func (f *fakeStore) PublishIdempotent(ctx context.Context, key, hash string, in []store.RateInput, render func([]store.Posted) (json.RawMessage, error)) (store.IdempotencyRecord, bool, error) {
	f.mu.Lock()
	if f.locks == nil {
		f.locks = map[string]*sync.Mutex{}
	}
	l, ok := f.locks[idemKey(ctx, key)]
	if !ok {
		l = &sync.Mutex{}
		f.locks[idemKey(ctx, key)] = l
	}
	f.mu.Unlock()
	l.Lock()
	defer l.Unlock()
	f.mu.Lock()
	existing, found := f.idem[idemKey(ctx, key)]
	f.mu.Unlock()
	if found {
		if existing.RequestHash != hash {
			return store.IdempotencyRecord{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0409", "idempotency key reused with a different request")
		}
		return existing, true, nil
	}
	posted, err := f.PostRates(ctx, in)
	if err != nil {
		return store.IdempotencyRecord{}, false, err
	}
	body, err := render(posted)
	if err != nil {
		return store.IdempotencyRecord{}, false, err
	}
	rec := store.IdempotencyRecord{Key: key, RequestHash: hash, ResponseStatus: http.StatusCreated, ResponseBody: body}
	f.mu.Lock()
	if f.idem == nil {
		f.idem = map[string]store.IdempotencyRecord{}
	}
	f.idem[idemKey(ctx, key)] = rec
	f.mu.Unlock()
	return rec, false, nil
}

func bookWith(t *testing.T, rates ...domain.Rate) *domain.Book {
	t.Helper()
	b := domain.NewBook(time.Now(), domain.RateType{Code: "CLOSING", AllowInverse: true})
	for i, r := range rates {
		r.ID = "r" + string(rune('a'+i))
		r.Type = "CLOSING"
		r.ValidFrom = time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
		if err := b.Add(r); err != nil {
			t.Fatal(err)
		}
	}
	return b
}

func rate(from, to, value string) domain.Rate {
	return domain.Rate{From: from, To: to, Value: decimal.RequireFromString(value)}
}

func doKeyed(r http.Handler, method, path, body, key string) *httptest.ResponseRecorder {
	return doKeyedAs(r, 1, method, path, body, key)
}

func doKeyedAs(r http.Handler, tenantN int, method, path, body, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(tenantN))
	req.Header.Set("X-Passage-Duties", allDuties)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const publishBody = `{"rates":[{"rateType":"CLOSING","from":"EUR","to":"USD","validFrom":"2026-01-02","rate":"1.0850000000"}]}`

func TestPublishRates(t *testing.T) {
	tests := []struct {
		name, body, key string
		wantStatus      int
		wantBody        string
		wantPosts       int
	}{
		{"created", publishBody, "k1", 201, `"version":1`, 1},
		{"missing key", publishBody, "", 400, "Idempotency-Key", 0},
		{"empty list", `{"rates":[]}`, "k1", 400, `"rates"`, 0},
		{"bad date and rate", `{"rates":[{"rateType":"CLOSING","from":"EUR","to":"USD","validFrom":"yesterday","rate":"abc"}]}`, "k1", 400, "rates[0].rate", 0},
		{"missing date", `{"rates":[{"rateType":"CLOSING","from":"EUR","to":"USD","rate":"1"}]}`, "k1", 400, "rates[0].validFrom", 0},
		{"numeric rate rejected", `{"rates":[{"rateType":"CLOSING","from":"EUR","to":"USD","validFrom":"2026-01-02","rate":1.08}]}`, "k1", 400, "COM-0001", 0},
		{"unknown field", `{"rates":[],"x":1}`, "k1", 400, "COM-0001", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			rec := doKeyed(newRouter(f), http.MethodPost, "/v1/fx/rates", tc.body, tc.key)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if len(f.posts) != tc.wantPosts {
				t.Fatalf("posts = %d, want %d", len(f.posts), tc.wantPosts)
			}
		})
	}
}

func TestPublishRatesRecordsManualSource(t *testing.T) {
	f := &fakeStore{}
	doKeyed(newRouter(f), http.MethodPost, "/v1/fx/rates", publishBody, "k1")
	if got := f.posts[0][0]; got.Source != "manual" || got.Rate.String() != "1.085" || got.ValidFrom.Format(time.DateOnly) != "2026-01-02" {
		t.Fatalf("posted %+v", got)
	}
}

func TestPublishRatesIdempotency(t *testing.T) {
	f := &fakeStore{}
	r := newRouter(f)
	first := doKeyed(r, http.MethodPost, "/v1/fx/rates", publishBody, "k1")
	replay := doKeyed(r, http.MethodPost, "/v1/fx/rates", publishBody, "k1")
	if replay.Code != 201 || replay.Header().Get("Idempotent-Replayed") != "true" || replay.Body.String() != first.Body.String() || len(f.posts) != 1 {
		t.Fatalf("replay %d %s posts=%d", replay.Code, replay.Body.String(), len(f.posts))
	}
	other := doKeyed(r, http.MethodPost, "/v1/fx/rates", strings.Replace(publishBody, "1.0850000000", "1.2", 1), "k1")
	if other.Code != 422 || !strings.Contains(other.Body.String(), "COM-0409") || len(f.posts) != 1 {
		t.Fatalf("mismatch %d %s", other.Code, other.Body.String())
	}
}

func TestPublishRatesFailureIsNotRecorded(t *testing.T) {
	f := &fakeStore{err: domain.ErrInvalidRate}
	rec := doKeyed(newRouter(f), http.MethodPost, "/v1/fx/rates", publishBody, "k1")
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "FX_RATE_INVALID") || len(f.idem) != 0 {
		t.Fatalf("got %d %s idem=%d", rec.Code, rec.Body.String(), len(f.idem))
	}
}

func TestPublishRatesNeedsMaintainDuty(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/v1/fx/rates", strings.NewReader(publishBody))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", "View exchange rates")
	req.Header.Set("Idempotency-Key", "k1")
	rec := httptest.NewRecorder()
	f := &fakeStore{}
	newRouter(f).ServeHTTP(rec, req)
	if rec.Code != 403 || len(f.posts) != 0 {
		t.Fatalf("got %d posts=%d", rec.Code, len(f.posts))
	}
}

func TestLookupRate(t *testing.T) {
	book := bookWith(t, rate("EUR", "USD", "1.25"), rate("EUR", "GBP", "0.8"))
	tests := []struct {
		name, query string
		wantStatus  int
		wantBody    string
	}{
		{"direct", "type=CLOSING&from=EUR&to=USD&date=2026-01-02", 200, `"path":"direct","rate":"1.25"`},
		{"inverse", "type=CLOSING&from=USD&to=EUR&date=2026-01-02", 200, `"path":"inverse","rate":"0.8"`},
		{"triangulated", "type=CLOSING&from=USD&to=GBP&date=2026-01-02&via=EUR", 200, `"path":"triangulated"`},
		{"missing rate", "type=CLOSING&from=USD&to=JPY&date=2026-01-02", 422, "LEDGER_RATE_MISSING"},
		{"future date", "type=CLOSING&from=EUR&to=USD&date=2999-01-01", 422, "FX_RATE_DATE_FUTURE"},
		{"missing params", "from=EUR", 400, `"type":"is required"`},
		{"bad date", "type=CLOSING&from=EUR&to=USD&date=01/02/2026", 400, "YYYY-MM-DD"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(newRouter(&fakeStore{book: book}), http.MethodGet, "/v1/fx/rates?"+tc.query, "", allDuties, testkit.TenantID(1))
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestLookupRateStoreFailureHidesDetail(t *testing.T) {
	rec := do(newRouter(&fakeStore{err: context.DeadlineExceeded}), http.MethodGet, "/v1/fx/rates?type=CLOSING&from=EUR&to=USD", "", allDuties, testkit.TenantID(1))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "deadline") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRateHistory(t *testing.T) {
	created := time.Date(2026, 1, 2, 9, 0, 0, 0, time.UTC)
	f := &fakeStore{history: []store.HistoryEntry{
		{ID: "a", Version: 1, ValidFrom: created, Rate: decimal.RequireFromString("1.1"), Source: "ecb", SupersededBy: "b", CreatedAt: created},
		{ID: "b", Version: 2, ValidFrom: created, Rate: decimal.RequireFromString("1.2"), Source: "manual", CreatedAt: created, CreatedBy: "u1"},
	}}
	rec := do(newRouter(f), http.MethodGet, "/v1/fx/rates/history?type=CLOSING&from=EUR&to=USD", "", allDuties, testkit.TenantID(1))
	var body struct{ Entries []map[string]any }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != 200 || len(body.Entries) != 2 {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if body.Entries[0]["supersededBy"] != "b" || body.Entries[1]["createdBy"] != "u1" || body.Entries[1]["validFrom"] != "2026-01-02" {
		t.Fatalf("entries %v", body.Entries)
	}
	missing := do(newRouter(f), http.MethodGet, "/v1/fx/rates/history?type=CLOSING", "", allDuties, testkit.TenantID(1))
	if missing.Code != 400 {
		t.Fatalf("missing params got %d", missing.Code)
	}
}

func TestConvert(t *testing.T) {
	book := bookWith(t, rate("EUR", "USD", "1.0855"), rate("EUR", "JPY", "160.5"))
	tests := []struct {
		name, body string
		wantStatus int
		wantBody   string
	}{
		{"rounds to target minor units", `{"rateType":"CLOSING","to":"USD","date":"2026-01-02","amounts":[{"value":"100.00","currency":"EUR"}]}`, 200, `"converted":{"value":"108.55","currency":"USD"}`},
		{"zero decimal target", `{"rateType":"CLOSING","to":"JPY","date":"2026-01-02","amounts":[{"value":"10.01","currency":"EUR"}]}`, 200, `"value":"1607"`},
		{"same currency", `{"rateType":"CLOSING","to":"EUR","date":"2026-01-02","amounts":[{"value":"5","currency":"EUR"}]}`, 200, `"converted":{"value":"5.00","currency":"EUR"}`},
		{"inverse", `{"rateType":"CLOSING","to":"EUR","date":"2026-01-02","amounts":[{"value":"108.55","currency":"USD"}]}`, 200, `"path":"inverse"`},
		{"missing rate", `{"rateType":"CLOSING","to":"CHF","date":"2026-01-02","amounts":[{"value":"1","currency":"EUR"}]}`, 422, "LEDGER_RATE_MISSING"},
		{"field errors", `{"rateType":"","to":"X","amounts":[{"value":"abc","currency":"E"}]}`, 400, "amounts[0].value"},
		{"no amounts", `{"rateType":"CLOSING","to":"USD","amounts":[]}`, 400, `"amounts"`},
		{"future date", `{"rateType":"CLOSING","to":"USD","date":"2999-01-01","amounts":[{"value":"1","currency":"EUR"}]}`, 422, "FX_RATE_DATE_FUTURE"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(newRouter(&fakeStore{book: book}), http.MethodPost, "/v1/fx/convert", tc.body, allDuties, testkit.TenantID(1))
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestReadiness(t *testing.T) {
	ok := do(newRouter(&fakeStore{}), http.MethodGet, "/readyz", "", "", "")
	down := do(newRouter(&fakeStore{pingErr: context.DeadlineExceeded}), http.MethodGet, "/readyz", "", "", "")
	if ok.Code != 200 || down.Code != 503 {
		t.Fatalf("ready %d down %d", ok.Code, down.Code)
	}
}

func TestIdempotencyKeyLengthBoundary(t *testing.T) {
	for _, tc := range []struct {
		name string
		n    int
		want int
	}{{"255 accepted", 255, 201}, {"256 rejected", 256, 400}} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{}
			rec := doKeyed(newRouter(f), http.MethodPost, "/v1/fx/rates", publishBody, strings.Repeat("k", tc.n))
			if rec.Code != tc.want {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestIdempotencyIsScopedPerTenant(t *testing.T) {
	f := &fakeStore{}
	r := newRouter(f)
	doKeyedAs(r, 1, http.MethodPost, "/v1/fx/rates", publishBody, "shared")
	other := doKeyedAs(r, 2, http.MethodPost, "/v1/fx/rates", strings.Replace(publishBody, "1.0850000000", "9", 1), "shared")
	if other.Code != 201 || other.Header().Get("Idempotent-Replayed") != "" || len(f.posts) != 2 {
		t.Fatalf("tenant 2 got %d replayed=%q posts=%d", other.Code, other.Header().Get("Idempotent-Replayed"), len(f.posts))
	}
}

func TestConcurrentSameKeyPublishesOnce(t *testing.T) {
	f := &fakeStore{}
	r := newRouter(f)
	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = doKeyed(r, http.MethodPost, "/v1/fx/rates", publishBody, "race").Code
		}()
	}
	wg.Wait()
	for _, c := range codes {
		if c != 201 {
			t.Fatalf("codes = %v", codes)
		}
	}
	if len(f.posts) != 1 {
		t.Fatalf("posts = %d, want 1", len(f.posts))
	}
}

func TestCurrencyCodesAreValidatedAsFieldErrors(t *testing.T) {
	book := bookWith(t, rate("EUR", "USD", "1.25"))
	f := &fakeStore{book: book}
	for _, tc := range []struct{ name, method, path, body, field string }{
		{"lookup from", "GET", "/v1/fx/rates?type=CLOSING&from=eur&to=USD", "", `"from"`},
		{"lookup to", "GET", "/v1/fx/rates?type=CLOSING&from=EUR&to=USDX", "", `"to"`},
		{"lookup via", "GET", "/v1/fx/rates?type=CLOSING&from=EUR&to=USD&via=e", "", `"via"`},
		{"history from", "GET", "/v1/fx/rates/history?type=CLOSING&from=E&to=USD", "", `"from"`},
		{"convert to", "POST", "/v1/fx/convert", `{"rateType":"CLOSING","to":"usd","amounts":[{"value":"1","currency":"EUR"}]}`, `"to"`},
		{"convert via", "POST", "/v1/fx/convert", `{"rateType":"CLOSING","to":"USD","via":"x","amounts":[{"value":"1","currency":"EUR"}]}`, `"via"`},
		{"convert currency", "POST", "/v1/fx/convert", `{"rateType":"CLOSING","to":"USD","amounts":[{"value":"1","currency":"eur"}]}`, "amounts[0].currency"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(newRouter(f), tc.method, tc.path, tc.body, allDuties, testkit.TenantID(1))
			if rec.Code != 400 || !strings.Contains(rec.Body.String(), tc.field) || !strings.Contains(rec.Body.String(), "COM-0001") {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
