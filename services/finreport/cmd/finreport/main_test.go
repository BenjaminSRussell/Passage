package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/api"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

type emptyStore struct{ api.Store }

func (emptyStore) Lag(context.Context) ([]store.LagRow, error) { return nil, nil }

// The handler must build without panicking (chi refuses middleware added after
// routes) and serve health plus a tenant-scoped reporting route.
func TestNewHandler_ServesHealthAndTenantRoutes(t *testing.T) {
	h := newHandler(emptyStore{})
	for _, tc := range []struct {
		path    string
		headers map[string]string
		want    int
	}{
		{"/healthz", nil, http.StatusOK},
		{"/api/v1/reporting/lag", map[string]string{
			"X-Passage-Tenant": "00000000-0000-0000-0000-000000000001", "X-Passage-User": "u1", "X-Passage-Duties": "reporting.statement.view",
		}, http.StatusOK},
		{"/api/v1/reporting/lag", nil, http.StatusUnauthorized},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		for k, v := range tc.headers {
			req.Header.Set(k, v)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.want {
			t.Fatalf("GET %s = %d, want %d: %s", tc.path, w.Code, tc.want, w.Body.String())
		}
	}
}

func TestRun_RequiresDatabaseURL(t *testing.T) {
	if err := run(context.Background(), func(string) string { return "" }); err == nil {
		t.Fatal("run without DATABASE_URL must fail")
	}
}
