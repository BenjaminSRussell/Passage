package tenant_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/go-chi/chi/v5/middleware"
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

func distinctLEs(n int) string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("0190a5f2-7b1c-7d3e-8a4f-%012x", i+1)
	}
	return strings.Join(out, ",")
}

func TestRejectionIsProblemJSONWithTraceID(t *testing.T) {
	h := middleware.RequestID(tenant.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
	req.Header.Set("X-Request-Id", "req-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Content-Type") != "application/problem+json" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("status %d headers %v", rec.Code, rec.Header())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["traceId"] != "req-123" || body["code"] != "AUTH-0001" {
		t.Fatalf("body %v", body)
	}
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
		{"nil entry", validLE + "," + nilUUID, http.StatusBadRequest, ""},
		{"only nil", nilUUID, http.StatusBadRequest, ""},
		{"duplicates collapsed", validLE + "," + strings.ToUpper(validLE) + "," + other, http.StatusOK, validLE + "," + other},
		{"at cap", distinctLEs(100), http.StatusOK, distinctLEs(100)},
		{"over cap", distinctLEs(101), http.StatusBadRequest, ""},
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

func TestReadOnlyPOSTKeepsAllScopeOnlyWhereDeclared(t *testing.T) {
	duty := map[string]string{tenant.HeaderDuties: tenant.DutyAllScope}
	withALL := map[string]string{tenant.HeaderDuties: tenant.DutyAllScope, tenant.HeaderLegalEntityScopeAll: tenant.LegalEntityScopeAll}
	noDuty := map[string]string{tenant.HeaderLegalEntityScopeAll: tenant.LegalEntityScopeAll}
	probe := func(method string, headers map[string]string, readOnly bool) bool {
		var got bool
		var inner http.Handler = http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			p, _ := tenant.FromContext(r.Context())
			got = p.HasDuty(tenant.DutyAllScope)
		})
		if readOnly {
			inner = tenant.ReadOnlyPOST(inner)
		}
		r := httptest.NewRequest(method, "/x", nil)
		r.Header.Set(tenant.HeaderTenant, validTenant)
		for k, v := range headers {
			r.Header.Set(k, v)
		}
		tenant.Middleware(inner).ServeHTTP(httptest.NewRecorder(), r)
		return got
	}
	if !probe(http.MethodPost, withALL, true) {
		t.Fatal("allowlisted POST with ALL and duty must keep the duty, like a GET")
	}
	if probe(http.MethodPost, withALL, false) {
		t.Fatal("a non-allowlisted POST must still drop the duty")
	}
	if probe(http.MethodPost, duty, true) {
		t.Fatal("allowlisted POST without the ALL header must drop the duty")
	}
	if probe(http.MethodPost, noDuty, true) {
		t.Fatal("ALL without the duty must grant nothing")
	}
	for _, m := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete} {
		if probe(m, withALL, true) {
			t.Fatalf("%s must never carry the duty, even under ReadOnlyPOST", m)
		}
	}
}

func TestReadOnlyPOSTDoesNotLetASwappedPrincipalInheritTheGrant(t *testing.T) {
	var got bool
	inner := tenant.ReadOnlyPOST(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, _ := tenant.FromContext(r.Context())
		got = p.HasDuty(tenant.DutyAllScope)
	}))
	swap := func(p tenant.Principal) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			inner.ServeHTTP(w, r.WithContext(tenant.With(r.Context(), p)))
		})
	}
	for name, p := range map[string]tenant.Principal{
		"other user":    {TenantID: validTenant, UserID: "someone-else"},
		"other tenant":  {TenantID: "00000000-0000-7000-8000-000000000002", UserID: "u1"},
		"service token": {TenantID: validTenant, UserID: "u1", Service: "billing"},
	} {
		got = false
		r := httptest.NewRequest(http.MethodPost, "/x", nil)
		r.Header.Set(tenant.HeaderTenant, validTenant)
		r.Header.Set(tenant.HeaderUser, "u1")
		r.Header.Set(tenant.HeaderDuties, tenant.DutyAllScope)
		r.Header.Set(tenant.HeaderLegalEntityScopeAll, tenant.LegalEntityScopeAll)
		tenant.Middleware(swap(p)).ServeHTTP(httptest.NewRecorder(), r)
		if got {
			t.Fatalf("%s: swapped principal inherited the all-scope grant", name)
		}
	}
}

func TestMiddlewareGatewaySecret(t *testing.T) {
	t.Setenv(tenant.GatewaySecretEnv, "s3cret-per-boot")
	called := false
	h := tenant.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	do := func(secret string) int {
		called = false
		req := httptest.NewRequest(http.MethodGet, "/v1/x", nil)
		req.Header.Set(tenant.HeaderTenant, validTenant)
		if secret != "" {
			req.Header.Set(tenant.HeaderGatewaySecret, secret)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := do(""); code != http.StatusUnauthorized || called {
		t.Fatalf("no secret: %d called=%v", code, called)
	}
	if code := do("wrong"); code != http.StatusUnauthorized || called {
		t.Fatalf("wrong secret: %d called=%v", code, called)
	}
	if code := do("s3cret-per-boot"); code != http.StatusOK || !called {
		t.Fatalf("right secret: %d called=%v", code, called)
	}
	// Health probes never carry the secret.
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz: %d", rec.Code)
	}
}
