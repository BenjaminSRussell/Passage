package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/api"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/config"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

type emptyStore struct{ api.Store }

func (emptyStore) Lag(context.Context) ([]store.LagRow, error) { return nil, nil }

// The handler must build without panicking (chi refuses middleware added after
// routes) and serve health plus a tenant-scoped reporting route.
func TestNewHandler_ServesHealthAndTenantRoutes(t *testing.T) {
	h := newHandler(emptyStore{}, config.Reporting{})
	for _, tc := range []struct {
		path    string
		headers map[string]string
		want    int
	}{
		{"/healthz", nil, http.StatusOK},
		{"/v1/reporting/lag", map[string]string{
			"X-Passage-Tenant": "00000000-0000-0000-0000-000000000001", "X-Passage-User": "u1", "X-Passage-Duties": "reporting.statement.view",
		}, http.StatusOK},
		{"/v1/reporting/lag", nil, http.StatusUnauthorized},
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

func (emptyStore) RedeemShareLink(context.Context, string) (store.SharedFile, error) {
	return store.SharedFile{}, nil
}

// Slice 19 added a route without a Passage identity. Every other route must
// still need the tenant headers: walk the authenticated route table and call
// each route with none, and check the download is the only route that answers
// without them.
func TestNewHandler_EveryAPIRouteStillNeedsATenant(t *testing.T) {
	h := newHandler(emptyStore{}, config.Reporting{})
	routes := chi.NewRouter()
	api.New(emptyStore{}).Register(routes)
	param := regexp.MustCompile(`\{[^}]+\}`)
	n := 0
	err := chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		path := param.ReplaceAllString(route, "0190f0a0-0000-7000-8000-000000000001")
		path = strings.ReplaceAll(path, "/*", "/0190f0a0-0000-7000-8000-000000000001:revoke")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without a tenant = %d, want 401", method, path, w.Code)
		}
		n++
		return nil
	})
	if err != nil || n < 40 {
		t.Fatalf("walked %d routes, err %v", n, err)
	}
	for _, path := range []string{"/v1/reporting/shared", "/v1/reporting/sharedx/" + strings.Repeat("a", 32), "/v1/reporting/unknown"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("GET %s without a tenant = %d, want 401", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/reporting/shared/0190f0a000007000800000000000000a."+strings.Repeat("A", 43), nil))
	if w.Code != http.StatusOK {
		t.Fatalf("the share link download needs no tenant: %d %s", w.Code, w.Body.String())
	}
}

func TestRun_RequiresDatabaseURL(t *testing.T) {
	if err := run(context.Background(), func(string) string { return "" }); err == nil {
		t.Fatal("run without DATABASE_URL must fail")
	}
}

// An out-of-range tunable stops startup before any database connection, so the
// error names the variable even when DATABASE_URL is also missing.
func TestRun_RejectsInvalidReportingConfigBeforeConnecting(t *testing.T) {
	env := map[string]string{"REPORTING_MAX_LAG_S": "9999"}
	err := run(context.Background(), func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "REPORTING_MAX_LAG_S must be between 0 and 600") {
		t.Fatalf("run error = %v, want the REPORTING_MAX_LAG_S range", err)
	}
}
