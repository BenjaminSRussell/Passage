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
	"github.com/BenjaminSRussell/passage/services/party/internal/api"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
)

const validatePath = "/v1/party/tax-registrations:validate"

type stubChecker struct {
	status domain.Status
	err    error
	calls  int
}

func (s *stubChecker) Check(context.Context, string, string) (domain.Status, error) {
	s.calls++
	return s.status, s.err
}

type stubPinger struct{ err error }

func (p stubPinger) Ping(context.Context) error { return p.err }

func newRouter(c domain.Checker) (*chi.Mux, *api.API) {
	return newRouterWithPinger(c, stubPinger{})
}

func newRouterWithPinger(c domain.Checker, p api.Pinger) (*chi.Mux, *api.API) {
	a := api.New(c, p, nil, nil)
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func do(r http.Handler, method, path, body, duties string, withTenant bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if withTenant {
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-Duties", duties)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name       string
		checker    *stubChecker
		body       string
		wantStatus int
		wantBody   string
		wantCalls  int
	}{
		{"vat valid", &stubChecker{status: domain.StatusValid}, `{"type":"VAT","country":"DE","number":"DE 123456789"}`, http.StatusOK, `"validationStatus":"valid"`, 1},
		{"vat invalid", &stubChecker{status: domain.StatusInvalid}, `{"type":"VAT","country":"DE","number":"DE123456789"}`, http.StatusOK, `"validationStatus":"invalid"`, 1},
		{"normalized number echoed", &stubChecker{status: domain.StatusValid}, `{"type":"VAT","country":"gr","number":"el 123-456-789"}`, http.StatusOK, `"number":"EL123456789"`, 1},
		{"checker timeout is unverified", &stubChecker{err: context.DeadlineExceeded}, `{"type":"VAT","country":"DE","number":"DE123456789"}`, http.StatusOK, `"validationStatus":"unverified"`, 1},
		{"checker outage is unverified", &stubChecker{err: errors.New("vies 503")}, `{"type":"VAT","country":"FR","number":"FR12345678901"}`, http.StatusOK, `"validationStatus":"unverified"`, 1},
		{"ein is format only", &stubChecker{}, `{"type":"EIN","number":"12-3456789"}`, http.StatusOK, `"validationStatus":"format_only"`, 0},
		{"gstin is format only", &stubChecker{}, `{"type":"GSTIN","number":"27AAPFU0939F1ZV"}`, http.StatusOK, `"validationStatus":"format_only"`, 0},
		{"gstin bad check digit", &stubChecker{}, `{"type":"GSTIN","number":"27AAPFU0939F1ZW"}`, http.StatusUnprocessableEntity, "PTY-0001", 0},
		{"vat wrong shape never calls adapter", &stubChecker{}, `{"type":"VAT","country":"DE","number":"DE12"}`, http.StatusUnprocessableEntity, "PTY-0001", 0},
		{"empty number", &stubChecker{}, `{"type":"VAT","country":"DE","number":"  "}`, http.StatusUnprocessableEntity, `"number":"is required"`, 0},
		{"missing type", &stubChecker{}, `{"number":"123456789"}`, http.StatusUnprocessableEntity, `"type":`, 0},
		{"unsupported type", &stubChecker{}, `{"type":"ABN","number":"51824753556"}`, http.StatusUnprocessableEntity, "PTY-0001", 0},
		{"vat without country", &stubChecker{}, `{"type":"VAT","number":"DE123456789"}`, http.StatusUnprocessableEntity, `"country":`, 0},
		{"malformed body", &stubChecker{}, `{`, http.StatusBadRequest, "COM-0001", 0},
		{"empty body", &stubChecker{}, ``, http.StatusBadRequest, "COM-0001", 0},
		{"unknown field", &stubChecker{}, `{"type":"VAT","number":"1","extra":1}`, http.StatusBadRequest, "COM-0001", 0},
		{"number given as integer", &stubChecker{}, `{"type":"EIN","number":123456789}`, http.StatusBadRequest, "COM-0001", 0},
		{"type given as object", &stubChecker{}, `{"type":{},"number":"1"}`, http.StatusBadRequest, "COM-0001", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.checker)
			rec := do(r, http.MethodPost, validatePath, tc.body, "party.write", true)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) || tc.checker.calls != tc.wantCalls {
				t.Fatalf("got %d %s calls=%d; want %d containing %s calls=%d", rec.Code, rec.Body.String(), tc.checker.calls, tc.wantStatus, tc.wantBody, tc.wantCalls)
			}
		})
	}
}

func TestValidateProblemEnvelope(t *testing.T) {
	r, _ := newRouter(&stubChecker{})
	rec := do(r, http.MethodPost, validatePath, `{"type":"EIN","number":"1"}`, "party.write", true)
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content type %q", ct)
	}
	for _, key := range []string{`"type":"https://passage.dev/problems/PTY-0001"`, `"title"`, `"status":422`, `"detail"`, `"instance":"` + validatePath, `"code":"PTY-0001"`, `"traceId"`, `"errors"`} {
		if !strings.Contains(rec.Body.String(), key) {
			t.Errorf("problem body missing %s: %s", key, rec.Body.String())
		}
	}
}

func TestValidateRequiresTenantAndDuty(t *testing.T) {
	body := `{"type":"EIN","number":"12-3456789"}`
	tests := []struct {
		name, duties string
		withTenant   bool
		wantStatus   int
		wantCode     string
	}{
		{"no tenant", "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"no duties", "", true, http.StatusForbidden, "AUTH-0003"},
		{"read duty only", "party.read", true, http.StatusForbidden, "AUTH-0003"},
		{"write duty", "party.write", true, http.StatusOK, "format_only"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&stubChecker{})
			rec := do(r, http.MethodPost, validatePath, body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantCode) {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestValidateRejectsMalformedTenant(t *testing.T) {
	tests := []struct {
		name, tenantID, legalEntity string
		wantStatus                  int
		wantCode                    string
	}{
		{"tenant not a uuid", "not-a-uuid", "", http.StatusUnauthorized, "AUTH-0001"},
		{"legal entity list", testkit.TenantID(1), testkit.TenantID(2) + "," + testkit.TenantID(3), http.StatusBadRequest, "COM-0001"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(&stubChecker{})
			req := httptest.NewRequest(http.MethodPost, validatePath, strings.NewReader(`{"type":"EIN","number":"12-3456789"}`))
			req.Header.Set("X-Passage-Tenant", tc.tenantID)
			req.Header.Set("X-Passage-Legal-Entity", tc.legalEntity)
			req.Header.Set("X-Passage-Duties", "party.write")
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantCode) {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestMetricsCountBusinessOperations(t *testing.T) {
	checker := &stubChecker{status: domain.StatusValid}
	r, a := newRouter(checker)
	do(r, http.MethodPost, validatePath, `{"type":"VAT","country":"DE","number":"DE123456789"}`, "party.write", true)
	checker.status, checker.err = "", errors.New("vies down")
	do(r, http.MethodPost, validatePath, `{"type":"VAT","country":"DE","number":"DE123456789"}`, "party.write", true)
	do(r, http.MethodPost, validatePath, `{"type":"VAT","country":"DE","number":"bad"}`, "party.write", true)
	do(r, http.MethodPost, validatePath, `{`, "party.write", true)
	req := httptest.NewRequest(http.MethodPost, validatePath, strings.NewReader(`{"type":"EIN","number":"12-3456789"}`))
	req.Header = http.Header{"X-Passage-Tenant": {testkit.TenantID(1)}, "X-Passage-Legal-Entity": {testkit.TenantID(2) + "," + testkit.TenantID(3)}, "X-Passage-Duties": {"party.write"}}
	r.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		`party_tax_validations_total{status="valid"} 1`,
		`party_tax_validations_total{status="unverified"} 1`,
		`party_tax_validations_rejected_total 2`,
		`party_tax_checker_failures_total 1`,
	} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics missing %q:\n%s", want, rec.Body.String())
		}
	}
}

func TestUnknownCheckerStatusCountsAsUnverified(t *testing.T) {
	r, a := newRouter(&stubChecker{status: domain.Status("unexpected")})
	rec := do(r, http.MethodPost, validatePath, `{"type":"VAT","country":"DE","number":"DE123456789"}`, "party.write", true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"validationStatus":"unverified"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	metrics := httptest.NewRecorder()
	a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), `party_tax_validations_total{status="unverified"} 1`) {
		t.Fatalf("unexpected status was not counted as unverified: %s", metrics.Body.String())
	}
}

func TestReadyz_ReturnsUnavailableWhenPingFails(t *testing.T) {
	r, _ := newRouterWithPinger(&stubChecker{}, stubPinger{err: errors.New("database down")})
	rec := do(r, http.MethodGet, "/readyz", "", "", false)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"status":"unavailable"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if healthz := do(r, http.MethodGet, "/healthz", "", "", false); healthz.Code != http.StatusOK {
		t.Fatalf("liveness must not depend on the database, got %d", healthz.Code)
	}
}

func TestProbesAreServedByKernel(t *testing.T) {
	r, _ := newRouter(&stubChecker{})
	for _, path := range []string{"/healthz", "/readyz"} {
		if rec := do(r, http.MethodGet, path, "", "", false); rec.Code != http.StatusOK {
			t.Errorf("%s got %d", path, rec.Code)
		}
	}
}

func newRequest(method, path, body, duties string) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", duties)
	return req
}

func serve(r http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func httpRecorderFor(h http.Handler) string {
	return serve(h, httptest.NewRequest(http.MethodGet, "/metrics", nil)).Body.String()
}
