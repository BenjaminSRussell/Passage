package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/audit/internal/api"
	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
	"github.com/BenjaminSRussell/passage/services/audit/internal/store"
)

type fakeStore struct {
	err     error
	ping    error
	applied []domain.Change
	records []store.Record
	alerts  []store.Alert
}

func (f *fakeStore) Apply(_ context.Context, change domain.Change) error {
	if f.err != nil {
		return f.err
	}
	f.applied = append(f.applied, change)
	return nil
}
func (f *fakeStore) Query(context.Context, string, string) ([]store.Record, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.records == nil {
		return []store.Record{}, nil
	}
	return f.records, nil
}
func (f *fakeStore) CreateRule(_ context.Context, rule store.AlertRule) (store.AlertRule, error) {
	if f.err != nil {
		return store.AlertRule{}, f.err
	}
	rule.ID = "rule-1"
	return rule, nil
}
func (f *fakeStore) ListAlerts(context.Context) ([]store.Alert, error) {
	if f.err != nil {
		return nil, f.err
	}
	if f.alerts == nil {
		return []store.Alert{}, nil
	}
	return f.alerts, nil
}
func (f *fakeStore) Ping(context.Context) error { return f.ping }

func newRouter(f *fakeStore) (*chi.Mux, *api.API) {
	a := api.New(f)
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func do(r http.Handler, method, path, body, duties string, withTenant bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if withTenant {
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-User", "user-1")
		req.Header.Set("X-Passage-Duties", duties)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const (
	changes = "/v1/audit/changes"
	record  = "/v1/audit/records/party.vendor/v-1"
	duties  = "audit.read,audit.write"
	valid   = `{"entity":"party.vendor","recordId":"v-1","field":"name","oldValue":"Acme","newValue":"Acme Ltd","userId":"user-1","traceId":"trace-abc"}`
)

func TestAuditHTTP(t *testing.T) {
	recAt := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	viewer := &fakeStore{records: []store.Record{{
		ID: "e1", Entity: "party.vendor", RecordID: "v-1", Field: "name",
		OldValue: "Acme", NewValue: "Acme Ltd", UserID: "user-1", TraceID: "trace-abc", OccurredAt: recAt,
	}}}
	tests := []struct {
		name, method, path, body, duty string
		store                          *fakeStore
		withTenant                     bool
		wantStatus                     int
		wantBody                       string
	}{
		{"ingest ok", http.MethodPost, changes, valid, duties, &fakeStore{}, true, http.StatusAccepted, `"recorded"`},
		{"ingest without tenant", http.MethodPost, changes, valid, "", &fakeStore{}, false, http.StatusUnauthorized, "AUTH-0001"},
		{"ingest malformed", http.MethodPost, changes, `{`, duties, &fakeStore{}, true, http.StatusBadRequest, "COM-0001"},
		{"ingest missing field", http.MethodPost, changes, `{"entity":"party.vendor","recordId":"v-1","oldValue":"a","newValue":"b","userId":"u","traceId":"t"}`, duties, &fakeStore{}, true, http.StatusBadRequest, `"field"`},
		{"ingest without duty", http.MethodPost, changes, valid, "audit.read", &fakeStore{}, true, http.StatusForbidden, "AUTH-0003"},
		{"ingest store error", http.MethodPost, changes, valid, duties, &fakeStore{err: errors.New("secret")}, true, http.StatusInternalServerError, "COM-0500"},
		{"query viewer", http.MethodGet, record, "", duties, viewer, true, http.StatusOK, `"traceId":"trace-abc"`},
		{"query empty", http.MethodGet, record, "", duties, &fakeStore{}, true, http.StatusOK, `"items":[]`},
		{"expand", http.MethodPost, "/v1/audit/texts/expand", `{"body":"Hi {name}","vars":{"name":"Ada"}}`, "", &fakeStore{}, true, http.StatusOK, `"Hi Ada"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duty, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestIngestThenQueryIsTheTwoSecondViewerPath(t *testing.T) {
	f := &fakeStore{}
	r, _ := newRouter(f)
	start := time.Now()
	rec := do(r, http.MethodPost, changes, valid, duties, true)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("ingest %d %s", rec.Code, rec.Body.String())
	}
	f.records = []store.Record{{
		Entity: "party.vendor", RecordID: "v-1", Field: "name",
		OldValue: "Acme", NewValue: "Acme Ltd", UserID: "user-1", TraceID: "trace-abc", OccurredAt: time.Now().UTC(),
	}}
	rec = do(r, http.MethodGet, record, "", duties, true)
	if time.Since(start) > 2*time.Second {
		t.Fatal("viewer slower than 2s")
	}
	for _, need := range []string{`"userId":"user-1"`, `"oldValue":"Acme"`, `"newValue":"Acme Ltd"`, `"traceId":"trace-abc"`} {
		if !strings.Contains(rec.Body.String(), need) {
			t.Fatalf("viewer missing %s in %s", need, rec.Body.String())
		}
	}
}

func TestReadyAndMetrics(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	if rec := do(r, http.MethodGet, "/readyz", "", "", false); rec.Code != http.StatusOK {
		t.Fatalf("ready %d", rec.Code)
	}
	down, _ := newRouter(&fakeStore{ping: errors.New("down")})
	if rec := do(down, http.MethodGet, "/readyz", "", "", false); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("down %d", rec.Code)
	}
	do(r, http.MethodPost, changes, valid, duties, true)
	w := httptest.NewRecorder()
	a.Metrics().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(), "audit_changes_applied_total 1") {
		t.Fatalf("metrics %s", w.Body.String())
	}
}
