package tenant_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	validTenant = "0190a5f2-7b1c-7d3e-8a4f-1234567890ab"
	validLE     = "0190a5f2-7b1c-7d3e-8a4f-1234567890ac"
	nilUUID     = "00000000-0000-0000-0000-000000000000"
)

func serve(tenantID, legalEntity string) (*httptest.ResponseRecorder, *tenant.Principal) {
	var got *tenant.Principal
	h := tenant.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		got = &p
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	if tenantID != "" {
		req.Header.Set(tenant.HeaderTenant, tenantID)
	}
	if legalEntity != "" {
		req.Header.Set(tenant.HeaderLegalEntity, legalEntity)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, got
}

func TestMiddlewareTenantValidation(t *testing.T) {
	tests := []struct {
		name, tenant string
		wantStatus   int
		wantTenant   string
		wantDetail   string
	}{
		{"valid", validTenant, http.StatusOK, validTenant, ""},
		{"uppercase canonicalized", strings.ToUpper(validTenant), http.StatusOK, validTenant, ""},
		{"missing", "", http.StatusUnauthorized, "", "missing tenant"},
		{"nil uuid", nilUUID, http.StatusUnauthorized, "", "invalid tenant"},
		{"not a uuid", "not-a-uuid", http.StatusUnauthorized, "", "invalid tenant"},
		{"short word", "tenant-a", http.StatusUnauthorized, "", "invalid tenant"},
		{"no hyphens", strings.ReplaceAll(validTenant, "-", ""), http.StatusUnauthorized, "", "invalid tenant"},
		{"braced", "{" + validTenant + "}", http.StatusUnauthorized, "", "invalid tenant"},
		{"urn prefixed", "urn:uuid:" + validTenant, http.StatusUnauthorized, "", "invalid tenant"},
		{"trailing space", validTenant + " ", http.StatusUnauthorized, "", "invalid tenant"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, p := serve(tc.tenant, "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if tc.wantStatus == http.StatusOK {
				if p == nil || p.TenantID != tc.wantTenant {
					t.Fatalf("principal %+v", p)
				}
				return
			}
			if p != nil || !strings.Contains(rec.Body.String(), `"code":"AUTH-0001"`) || !strings.Contains(rec.Body.String(), tc.wantDetail) {
				t.Fatalf("handler ran or body wrong: %s", rec.Body)
			}
		})
	}
}

func TestMiddlewareLegalEntityValidation(t *testing.T) {
	other := "0190a5f2-7b1c-7d3e-8a4f-1234567890ad"
	tests := []struct {
		name, legalEntity string
		wantStatus        int
		wantLE            string
	}{
		{"absent", "", http.StatusOK, ""},
		{"valid", validLE, http.StatusOK, validLE},
		{"uppercase canonicalized", strings.ToUpper(validLE), http.StatusOK, validLE},
		{"list with spaces", validLE + " , " + strings.ToUpper(other), http.StatusOK, validLE + "," + other},
		{"not a uuid", "le-1", http.StatusBadRequest, ""},
		{"valid then invalid entry", validLE + "," + "x", http.StatusBadRequest, ""},
		{"empty list entry", validLE + ",," + other, http.StatusBadRequest, ""},
		{"trailing comma", validLE + ",", http.StatusBadRequest, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec, p := serve(validTenant, tc.legalEntity)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status %d: %s", rec.Code, rec.Body)
			}
			if tc.wantStatus == http.StatusOK {
				if p == nil || p.LegalEntityID != tc.wantLE {
					t.Fatalf("principal %+v", p)
				}
				return
			}
			if p != nil || !strings.Contains(rec.Body.String(), `"code":"COM-0001"`) {
				t.Fatalf("handler ran or body wrong: %s", rec.Body)
			}
		})
	}
}

func TestMiddlewareHealthBypassesValidation(t *testing.T) {
	h := tenant.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, path := range []string{"/healthz", "/readyz"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNoContent {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
}

func hasAllScope(t *testing.T, method string, headers map[string]string) bool {
	t.Helper()
	var got bool
	h := tenant.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		got = p.HasDuty(tenant.DutyAllScope)
	}))
	r := httptest.NewRequest(method, "/x", nil)
	r.Header.Set(tenant.HeaderTenant, validTenant)
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
	r.Header.Set(tenant.HeaderTenant, validTenant)
	r.Header.Set(tenant.HeaderWebLegalEntity, validLE)
	h.ServeHTTP(httptest.NewRecorder(), r)
	if got != validLE {
		t.Fatalf("got %q", got)
	}
}

func serveWith(method string, headers map[string]string) (*httptest.ResponseRecorder, *tenant.Principal) {
	var got *tenant.Principal
	h := tenant.Middleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		got = &p
	}))
	r := httptest.NewRequest(method, "/x", nil)
	r.Header.Set(tenant.HeaderTenant, validTenant)
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec, got
}

func TestMiddlewareRejectsWildcardInBothSpellings(t *testing.T) {
	for _, header := range []string{tenant.HeaderLegalEntity, tenant.HeaderWebLegalEntity} {
		for _, value := range []string{"*", "all", "ALL", validLE + ",*"} {
			for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPost} {
				rec, p := serveWith(method, map[string]string{header: value})
				if rec.Code != http.StatusBadRequest || p != nil || !strings.Contains(rec.Body.String(), `"code":"COM-0001"`) {
					t.Fatalf("%s %q %s: %d %s", header, value, method, rec.Code, rec.Body)
				}
			}
		}
	}
}

func TestMiddlewareBothLegalEntityHeaders(t *testing.T) {
	other := "0190a5f2-7b1c-7d3e-8a4f-1234567890ad"
	rec, _ := serveWith(http.MethodGet, map[string]string{tenant.HeaderLegalEntity: validLE, tenant.HeaderWebLegalEntity: other})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("conflicting headers: %d", rec.Code)
	}
	rec, p := serveWith(http.MethodGet, map[string]string{tenant.HeaderLegalEntity: validLE, tenant.HeaderWebLegalEntity: strings.ToUpper(validLE)})
	if rec.Code != http.StatusOK || p.LegalEntityID != validLE {
		t.Fatalf("identical headers: %d %+v", rec.Code, p)
	}
}
