package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/platform-health/internal/api"
	"github.com/BenjaminSRussell/passage/services/platform-health/internal/store"
)

type fakeStore struct {
	err     error
	pingErr error
	entries []store.Entry
}

func (f *fakeStore) Create(_ context.Context, message, _ string) (store.Entry, error) {
	if f.err != nil {
		return store.Entry{}, f.err
	}
	e := store.Entry{ID: "id-1", Message: message, CreatedAt: time.Unix(0, 0).UTC()}
	f.entries = append(f.entries, e)
	return e, nil
}

func (f *fakeStore) List(context.Context) ([]store.Entry, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]store.Entry{}, f.entries...), nil
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }

func newRouter(f *fakeStore) (*chi.Mux, *api.API) {
	a := api.New(f)
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func do(r http.Handler, method, path, body string, withTenant bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if withTenant {
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-Duties", "hello.write,hello.read")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestEntries(t *testing.T) {
	const create = "/v1/hello-ledger/entries"
	tests := []struct {
		name       string
		store      *fakeStore
		method     string
		body       string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"create ok", &fakeStore{}, http.MethodPost, `{"message":"hi"}`, true, http.StatusCreated, `"message":"hi"`},
		{"create without tenant", &fakeStore{}, http.MethodPost, `{"message":"hi"}`, false, http.StatusUnauthorized, "AUTH-0001"},
		{"list without tenant", &fakeStore{}, http.MethodGet, "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"create malformed body", &fakeStore{}, http.MethodPost, `{`, true, http.StatusBadRequest, "COM-0001"},
		{"create unknown field", &fakeStore{}, http.MethodPost, `{"message":"hi","x":1}`, true, http.StatusBadRequest, "COM-0001"},
		{"create number message", &fakeStore{}, http.MethodPost, `{"message":5}`, true, http.StatusBadRequest, "COM-0001"},
		{"create object message", &fakeStore{}, http.MethodPost, `{"message":{}}`, true, http.StatusBadRequest, "COM-0001"},
		{"create empty message", &fakeStore{}, http.MethodPost, `{"message":"  "}`, true, http.StatusBadRequest, "COM-0001"},
		{"create null message", &fakeStore{}, http.MethodPost, `{"message":null}`, true, http.StatusBadRequest, "COM-0001"},
		{"create null body", &fakeStore{}, http.MethodPost, `null`, true, http.StatusBadRequest, "COM-0001"},
		{"create too long", &fakeStore{}, http.MethodPost, `{"message":"` + strings.Repeat("a", 281) + `"}`, true, http.StatusBadRequest, "COM-0001"},
		{"create store error hides detail", &fakeStore{err: errors.New("pg secret detail")}, http.MethodPost, `{"message":"hi"}`, true, http.StatusInternalServerError, "COM-0500"},
		{"list empty is array", &fakeStore{}, http.MethodGet, "", true, http.StatusOK, "[]"},
		{"list store error", &fakeStore{err: errors.New("boom")}, http.MethodGet, "", true, http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, create, tc.body, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus == http.StatusInternalServerError && strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("internal detail leaked: %s", rec.Body.String())
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
			if rec := do(r, http.MethodGet, tc.path, "", false); rec.Code != tc.wantStatus {
				t.Fatalf("got %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestEntriesRequireDuty(t *testing.T) {
	tests := []struct {
		name, method, duties string
		wantStatus           int
	}{
		{"create without duty", http.MethodPost, "", http.StatusForbidden},
		{"create with read duty only", http.MethodPost, "hello.read", http.StatusForbidden},
		{"list without duty", http.MethodGet, "", http.StatusForbidden},
		{"list with write duty only", http.MethodGet, "hello.write", http.StatusForbidden},
		{"create with write duty", http.MethodPost, "hello.write", http.StatusCreated},
		{"list with read duty", http.MethodGet, "hello.read", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&fakeStore{})
			req := httptest.NewRequest(tc.method, "/v1/hello-ledger/entries", strings.NewReader(`{"message":"hi"}`))
			req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
			req.Header.Set("X-Passage-Duties", tc.duties)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("got %d %s; want %d", rec.Code, rec.Body.String(), tc.wantStatus)
			}
			if tc.wantStatus == http.StatusForbidden && !strings.Contains(rec.Body.String(), "AUTH-0003") {
				t.Fatalf("want AUTH-0003, got %s", rec.Body.String())
			}
		})
	}
}

func TestMetricsCountBusinessOperationsAndHTTP(t *testing.T) {
	f := &fakeStore{}
	r, a := newRouter(f)
	h := a.Instrument(r)
	do(h, http.MethodPost, "/v1/hello-ledger/entries", `{"message":"hi"}`, true)
	f.err = errors.New("boom")
	do(h, http.MethodPost, "/v1/hello-ledger/entries", `{"message":"hi"}`, true)
	do(h, http.MethodPost, "/v1/hello-ledger/entries", `{}`, false)
	do(h, http.MethodGet, "/healthz", "", false)
	do(h, http.MethodGet, "/nope", "", false)

	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		"# TYPE hello_ledger_entries_created_total counter",
		"hello_ledger_entries_created_total 1",
		"hello_ledger_entries_failed_total 1",
		`hello_ledger_http_requests_total{route="/v1/hello-ledger/entries",status="201"} 1`,
		`hello_ledger_http_requests_total{route="/v1/hello-ledger/entries",status="500"} 1`,
		`hello_ledger_http_requests_total{route="/healthz",status="200"} 1`,
		`hello_ledger_http_requests_total{route="unmatched",status="401"} 2`,
		`hello_ledger_http_request_duration_seconds_count{route="/v1/hello-ledger/entries"} 2`,
		`hello_ledger_http_request_duration_seconds_bucket{route="/healthz",le="+Inf"} 1`,
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
	err = chi.Walk(r, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.Contains(string(spec), "\n  "+route+":") {
			t.Errorf("openapi.yaml does not document %s", route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(spec), "\n  /metrics:") {
		t.Error("openapi.yaml does not document /metrics")
	}
}

func TestEntries_InvalidLegalEntityHeader(t *testing.T) {
	for _, header := range []string{"foo", testkit.TenantID(1) + "," + testkit.TenantID(2)} {
		for _, method := range []string{http.MethodPost, http.MethodGet} {
			r, _ := newRouter(&fakeStore{})
			req := httptest.NewRequest(method, "/v1/hello-ledger/entries", strings.NewReader(`{"message":"hi"}`))
			req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
			req.Header.Set("X-Passage-Duties", "hello.write,hello.read")
			req.Header.Set("X-Passage-Legal-Entity", header)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
				t.Fatalf("%s legal entity %q: got %d %s; want 400 COM-0001", method, header, rec.Code, rec.Body.String())
			}
		}
	}
}
