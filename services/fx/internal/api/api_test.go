package api_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fx/internal/api"
	"github.com/BenjaminSRussell/passage/services/fx/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

type fakeStore struct {
	store.Store
	err   error
	types map[string]store.RateType
	puts  int

	posts   [][]store.RateInput
	book    *domain.Book
	history []store.HistoryEntry
	idem    map[string]store.IdempotencyRecord
	pingErr error
	mu      sync.Mutex
	locks   map[string]*sync.Mutex
}

func (f *fakeStore) PutRateType(_ context.Context, in store.RateType) (store.RateType, error) {
	f.puts++
	if f.err != nil {
		return store.RateType{}, f.err
	}
	if in.Code == "" || in.Name == "" {
		return store.RateType{}, fmt.Errorf("%w: rate type code and name are required", domain.ErrInvalidRate)
	}
	if f.types == nil {
		f.types = map[string]store.RateType{}
	}
	in.ID = "id-" + in.Code
	f.types[in.Code] = in
	return in, nil
}

func (f *fakeStore) GetRateType(_ context.Context, code string) (store.RateType, error) {
	t, ok := f.types[code]
	if !ok {
		return store.RateType{}, httpx.NewProblem(http.StatusNotFound, "FX_RATE_TYPE_UNKNOWN", "rate type not found")
	}
	return t, nil
}

func (f *fakeStore) ListRateTypes(context.Context) ([]store.RateType, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := []store.RateType{}
	for _, t := range f.types {
		out = append(out, t)
	}
	return out, nil
}

func newRouter(f *fakeStore) *chi.Mux {
	r := httpx.NewRouter()
	api.New(f).Register(r)
	return r
}

func do(r http.Handler, method, path, body, duties string, tenantID string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if tenantID != "" {
		req.Header.Set("X-Passage-Tenant", tenantID)
		req.Header.Set("X-Passage-Legal-Entity", testkit.TenantID(9))
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
	typesPath   = "/v1/fx/rate-types"
	closingPath = "/v1/fx/rate-types/CLOSING"
	closingBody = `{"name":"Closing","allowInverse":true,"provider":"ecb"}`
	allDuties   = "View exchange rates,Maintain exchange rates"
)

func TestRateTypes(t *testing.T) {
	tenantID := testkit.TenantID(1)
	tests := []struct {
		name, method, path, body, duties, tenant string
		store                                    *fakeStore
		wantStatus                               int
		wantBody                                 string
		wantPuts                                 int
	}{
		{"put ok", http.MethodPut, closingPath, closingBody, allDuties, tenantID, &fakeStore{}, 200, `"code":"CLOSING"`, 1},
		{"put without Maintain exchange rates", http.MethodPut, closingPath, closingBody, "View exchange rates", tenantID, &fakeStore{}, 403, "AUTH-0003", 0},
		{"put unknown field", http.MethodPut, closingPath, `{"name":"x","extra":1}`, allDuties, tenantID, &fakeStore{}, 400, "COM-0001", 0},
		{"put wrong type", http.MethodPut, closingPath, `{"name":"x","allowInverse":"yes"}`, allDuties, tenantID, &fakeStore{}, 400, "COM-0001", 0},
		{"put empty name", http.MethodPut, closingPath, `{}`, allDuties, tenantID, &fakeStore{}, 400, "FX_RATE_INVALID", 1},
		{"put store failure", http.MethodPut, closingPath, closingBody, allDuties, tenantID, &fakeStore{err: errors.New("pg secret")}, 500, "COM-0500", 1},
		{"get unknown", http.MethodGet, "/v1/fx/rate-types/NOPE", "", allDuties, tenantID, &fakeStore{}, 404, "FX_RATE_TYPE_UNKNOWN", 0},
		{"get without View exchange rates", http.MethodGet, closingPath, "", "Maintain exchange rates", tenantID, &fakeStore{}, 403, "AUTH-0003", 0},
		{"list without tenant", http.MethodGet, typesPath, "", "", "", &fakeStore{}, 401, "AUTH-0001", 0},
		{"list malformed tenant", http.MethodGet, typesPath, "", allDuties, "not-a-uuid", &fakeStore{}, 401, "AUTH-0001", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(newRouter(tc.store), tc.method, tc.path, tc.body, tc.duties, tc.tenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if strings.Contains(rec.Body.String(), "pg secret") {
				t.Fatalf("detail leaked: %s", rec.Body.String())
			}
			if tc.store.puts != tc.wantPuts {
				t.Fatalf("store puts = %d, want %d", tc.store.puts, tc.wantPuts)
			}
		})
	}
}

func TestPutRateTypeIdempotentAndReadable(t *testing.T) {
	r := newRouter(&fakeStore{})
	tenantID := testkit.TenantID(1)
	first := do(r, http.MethodPut, closingPath, closingBody, allDuties, tenantID)
	second := do(r, http.MethodPut, closingPath, closingBody, allDuties, tenantID)
	if first.Code != 200 || second.Code != 200 || first.Body.String() != second.Body.String() {
		t.Fatalf("put twice: %d %s / %d %s", first.Code, first.Body.String(), second.Code, second.Body.String())
	}
	got := do(r, http.MethodGet, closingPath, "", allDuties, tenantID)
	list := do(r, http.MethodGet, typesPath, "", allDuties, tenantID)
	if got.Body.String() != first.Body.String() || !strings.Contains(list.Body.String(), `"code":"CLOSING"`) {
		t.Fatalf("get %s list %s", got.Body.String(), list.Body.String())
	}
}
