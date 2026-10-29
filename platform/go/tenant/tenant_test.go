package tenant_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

func hasAllScope(t *testing.T, method string, headers map[string]string) bool {
	t.Helper()
	var got bool
	h := tenant.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		got = p.HasDuty(tenant.DutyAllScope)
	}))
	r := httptest.NewRequest(method, "/x", nil)
	r.Header.Set(tenant.HeaderTenant, "t")
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	h.ServeHTTP(httptest.NewRecorder(), r)
	return got
}

func TestMiddlewareAllScopeOnlyOnExplicitRead(t *testing.T) {
	duty := map[string]string{tenant.HeaderDuties: tenant.DutyAllScope}
	withALL := map[string]string{tenant.HeaderDuties: tenant.DutyAllScope, tenant.HeaderLegalEntityScopeAll: tenant.LegalEntityScopeAll}
	if !hasAllScope(t, http.MethodGet, withALL) {
		t.Fatal("GET with ALL and duty must keep the duty")
	}
	if hasAllScope(t, http.MethodGet, duty) {
		t.Fatal("GET without the ALL header must drop the duty")
	}
	if hasAllScope(t, http.MethodPost, withALL) {
		t.Fatal("writes must drop the duty")
	}
	if hasAllScope(t, http.MethodGet, map[string]string{tenant.HeaderLegalEntityScopeAll: tenant.LegalEntityScopeAll}) {
		t.Fatal("ALL without the duty must not grant it")
	}
}

func TestMiddlewareAcceptsWebLegalEntityHeader(t *testing.T) {
	var got string
	h := tenant.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		got = p.LegalEntityID
	}))
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set(tenant.HeaderTenant, "t")
	r.Header.Set(tenant.HeaderWebLegalEntity, "le-1")
	h.ServeHTTP(httptest.NewRecorder(), r)
	if got != "le-1" {
		t.Fatalf("got %q", got)
	}
}
