package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/numbering/internal/api"
	"github.com/BenjaminSRussell/passage/services/numbering/internal/store"
)

type fakeStore struct {
	err    error
	ping   error
	seq    store.Sequence
	issued store.Issued
}

func (f *fakeStore) Create(_ context.Context, in store.Sequence, _ string) (store.Sequence, error) {
	if f.err != nil {
		return store.Sequence{}, f.err
	}
	in.ID = "id-1"
	f.seq = in
	return in, nil
}
func (f *fakeStore) Get(context.Context, string) (store.Sequence, error) {
	if f.err != nil {
		return store.Sequence{}, f.err
	}
	return f.seq, nil
}
func (f *fakeStore) Allocate(context.Context, string, string) (store.Issued, error) {
	if f.err != nil {
		return store.Issued{}, f.err
	}
	return f.issued, nil
}
func (f *fakeStore) Ping(context.Context) error { return f.ping }

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
		req.Header.Set("X-Passage-Legal-Entity", testkit.TenantID(9))
		req.Header.Set("X-Passage-Duties", duties)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const (
	createPath = "/v1/numbering/sequences"
	nextPath   = "/v1/numbering/sequences/INV/next"
	allDuties  = "numbering.read,numbering.write"
	validBody  = `{"code":"INV","name":"Invoices","format":"INV-{######}","legalEntityId":"00000000-0000-0000-0000-000000000009","mode":"gap_free","startFrom":1}`
)

func TestSequences(t *testing.T) {
	ok := &fakeStore{issued: store.Issued{Value: 1, Formatted: "INV-000001", Sequence: "INV", LegalEntityID: testkit.TenantID(9)}}
	tests := []struct {
		name, method, path, body, duties string
		store                            *fakeStore
		withTenant                       bool
		wantStatus                       int
		wantBody                         string
	}{
		{"create ok", http.MethodPost, createPath, validBody, allDuties, ok, true, http.StatusCreated, `"code":"INV"`},
		{"create without tenant", http.MethodPost, createPath, validBody, "", ok, false, http.StatusUnauthorized, "AUTH-0001"},
		{"create malformed", http.MethodPost, createPath, `{`, allDuties, ok, true, http.StatusBadRequest, "COM-0001"},
		{"create unknown field", http.MethodPost, createPath, `{"code":"INV","x":1}`, allDuties, ok, true, http.StatusBadRequest, "COM-0001"},
		{"create bad mode", http.MethodPost, createPath, `{"code":"INV","name":"n","format":"INV-{######}","legalEntityId":"00000000-0000-0000-0000-000000000009","mode":"other","startFrom":1}`, allDuties, ok, true, http.StatusBadRequest, `"mode"`},
		{"create without duty", http.MethodPost, createPath, validBody, "numbering.read", ok, true, http.StatusForbidden, "AUTH-0003"},
		{"create store error", http.MethodPost, createPath, validBody, allDuties, &fakeStore{err: errors.New("pg secret")}, true, http.StatusInternalServerError, "COM-0500"},
		{"allocate ok", http.MethodPost, nextPath, "", allDuties, ok, true, http.StatusOK, `"formatted":"INV-000001"`},
		{"allocate without duty", http.MethodPost, nextPath, "", "numbering.read", ok, true, http.StatusForbidden, "AUTH-0003"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, tc.method, tc.path, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestReadyAndMetrics(t *testing.T) {
	r, a := newRouter(&fakeStore{})
	rec := do(r, http.MethodGet, "/readyz", "", "", false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ready") {
		t.Fatalf("ready: %d %s", rec.Code, rec.Body.String())
	}
	down, _ := newRouter(&fakeStore{ping: errors.New("down")})
	rec = do(down, http.MethodGet, "/readyz", "", "", false)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("down ready: %d", rec.Code)
	}
	do(r, http.MethodPost, nextPath, "", allDuties, true)
	w := httptest.NewRecorder()
	a.Metrics().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(w.Body.String(), "numbering_allocated_total 1") {
		t.Fatalf("metrics: %s", w.Body.String())
	}
}
