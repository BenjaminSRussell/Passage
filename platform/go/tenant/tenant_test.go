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
