package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/tax/internal/api"
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

const (
	calculatePath = "/v1/tax/calculate"
	duty          = "Calculate tax"
	workedBody    = `{"currency":"EUR","direction":"output","document_date":"2026-03-15","party":{"tax_group":"G"},` +
		`"lines":[{"line_ref":"L1","amount":"119.00","item_tax_group":"I","price_includes_tax":true}],` +
		`"config":{"default_rounding":"per_document","groups":{"G":["VAT"]},"item_groups":{"I":["VAT"]},` +
		`"codes":[{"code":"VAT","jurisdiction":"DE","rates":[{"valid_from":"2020-01-01","rate_pct":"19"}]}]}}`
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

type downProvider struct{}

func (downProvider) Name() string { return "avalara" }
func (downProvider) Calculate(context.Context, domain.Document) (domain.Result, error) {
	return domain.Result{}, &domain.ProviderError{Status: 503, Err: errors.New("unavailable")}
}
func (downProvider) Commit(context.Context, string) error                  { return nil }
func (downProvider) Void(context.Context, string) error                    { return nil }
func (downProvider) ValidateAddress(context.Context, domain.Address) error { return nil }

type rejectingProvider struct{ downProvider }

func (rejectingProvider) Calculate(context.Context, domain.Document) (domain.Result, error) {
	return domain.Result{}, &domain.ProviderError{Status: 400, Err: errors.New("bad address")}
}

func newRouter(db fakeDB, providers map[string]domain.TaxProvider) (*chi.Mux, *api.API) {
	a := api.New(db, providers, 100*time.Millisecond)
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func do(r http.Handler, method, path, body, tenantID, duties string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if tenantID != "" {
		req.Header.Set("X-Passage-Tenant", tenantID)
		req.Header.Set("X-Passage-Duties", duties)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func mutated(t *testing.T, from, to string) string {
	t.Helper()
	if !strings.Contains(workedBody, from) {
		t.Fatalf("worked body lacks %q", from)
	}
	return strings.Replace(workedBody, from, to, 1)
}

func TestCalculate(t *testing.T) {
	tenantID := testkit.TenantID(1)
	tests := []struct {
		name       string
		body       string
		tenant     string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"worked example", workedBody, tenantID, duty, http.StatusOK, `"engine":"internal"`},
		{"no tenant header", workedBody, "", "", http.StatusUnauthorized, "AUTH-0001"},
		{"malformed tenant header", workedBody, "not-a-uuid", duty, http.StatusUnauthorized, "AUTH-0001"},
		{"missing duty", workedBody, tenantID, "", http.StatusForbidden, "AUTH-0003"},
		{"malformed JSON", `{`, tenantID, duty, http.StatusBadRequest, "COM-0001"},
		{"empty body", ``, tenantID, duty, http.StatusBadRequest, "COM-0001"},
		{"unknown field", mutated(t, `"currency"`, `"x":1,"currency"`), tenantID, duty, http.StatusBadRequest, "COM-0001"},
		{"amount as JSON number", mutated(t, `"119.00"`, `119`), tenantID, duty, http.StatusBadRequest, "COM-0001"},
		{"amount with seven decimals", mutated(t, `"119.00"`, `"1.0000001"`), tenantID, duty, http.StatusBadRequest, "COM-0001"},
		{"amount as text", mutated(t, `"119.00"`, `"abc"`), tenantID, duty, http.StatusBadRequest, "COM-0001"},
		{"price_includes_tax as string", mutated(t, `true`, `"yes"`), tenantID, duty, http.StatusBadRequest, "COM-0001"},
		{"malformed date", mutated(t, `2026-03-15`, `15-03-2026`), tenantID, duty, http.StatusBadRequest, "COM-0001"},
		{"no lines", mutated(t, `"lines":[{"line_ref":"L1","amount":"119.00","item_tax_group":"I","price_includes_tax":true}]`, `"lines":[]`), tenantID, duty, http.StatusBadRequest, "COM-0001"},
		{"lower-case currency", mutated(t, `"EUR"`, `"eur"`), tenantID, duty, http.StatusBadRequest, "COM-0001"},
		{"no rate on tax date", mutated(t, `"valid_from":"2020-01-01"`, `"valid_from":"2030-01-01"`), tenantID, duty, http.StatusUnprocessableEntity, "TAX-0001"},
		{"negative rate", mutated(t, `"rate_pct":"19"`, `"rate_pct":"-1"`), tenantID, duty, http.StatusUnprocessableEntity, "TAX-0002"},
		{"unresolved group", mutated(t, `"tax_group":"G"`, `"tax_group":"X"`), tenantID, duty, http.StatusUnprocessableEntity, "TAX-0003"},
		{"unconfigured provider", mutated(t, `"party"`, `"provider":"avalara","party"`), tenantID, duty, http.StatusUnprocessableEntity, "TAX-0007"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(fakeDB{}, nil)
			rec := do(r, http.MethodPost, calculatePath, tc.body, tc.tenant, tc.duties)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestCalculateReturnsTheDeterminationResult(t *testing.T) {
	r, _ := newRouter(fakeDB{}, nil)
	rec := do(r, http.MethodPost, calculatePath, workedBody, testkit.TenantID(1), duty)
	var res domain.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Lines) != 1 || res.Lines[0].Base.String() != "100" || res.Lines[0].Taxes[0].TaxAmount.String() != "19" {
		t.Fatalf("unexpected result %s", rec.Body.String())
	}
	if len(res.DeterminationTrace) == 0 {
		t.Error("response carries no determination trace")
	}
}

func TestCalculateProblemEnvelopeCarriesEveryKey(t *testing.T) {
	r, _ := newRouter(fakeDB{}, nil)
	rec := do(r, http.MethodPost, calculatePath, mutated(t, `"rate_pct":"19"`, `"rate_pct":"-1"`), testkit.TenantID(1), duty)
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("content type %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"type", "title", "status", "detail", "instance", "code", "serviceCode", "traceId", "errors"} {
		if _, ok := body[key]; !ok {
			t.Errorf("problem body missing %q: %s", key, rec.Body.String())
		}
	}
	if body["serviceCode"] != "TAX-0002" || body["instance"] != calculatePath {
		t.Errorf("unexpected problem %v", body)
	}
}

func TestCalculateProviderFailover(t *testing.T) {
	withProvider := mutated(t, `"party"`, `"provider":"avalara","party"`)
	tenantID := testkit.TenantID(1)
	t.Run("provider outage falls back to the internal engine", func(t *testing.T) {
		r, a := newRouter(fakeDB{}, map[string]domain.TaxProvider{"avalara": downProvider{}})
		rec := do(r, http.MethodPost, calculatePath, withProvider, tenantID, duty)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"fallback":true`) || !strings.Contains(rec.Body.String(), "TAX-0007") {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
		metrics := httptest.NewRecorder()
		a.Metrics().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if !strings.Contains(metrics.Body.String(), "tax_provider_fallbacks_total 1") {
			t.Errorf("fallback not counted: %s", metrics.Body.String())
		}
	})
	t.Run("provider rejection is surfaced without fallback", func(t *testing.T) {
		r, _ := newRouter(fakeDB{}, map[string]domain.TaxProvider{"avalara": rejectingProvider{}})
		rec := do(r, http.MethodPost, calculatePath, withProvider, tenantID, duty)
		if rec.Code != http.StatusUnprocessableEntity || strings.Contains(rec.Body.String(), `"fallback":true`) {
			t.Fatalf("got %d %s", rec.Code, rec.Body.String())
		}
	})
}

func TestMetricsCountCalculationsAndFailures(t *testing.T) {
	r, a := newRouter(fakeDB{}, nil)
	tenantID := testkit.TenantID(1)
	do(r, http.MethodPost, calculatePath, workedBody, tenantID, duty)
	do(r, http.MethodPost, calculatePath, `{`, tenantID, duty)
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"tax_calculations_total 1", "tax_calculation_failures_total 1", "tax_provider_fallbacks_total 0"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics missing %q: %s", want, rec.Body.String())
		}
	}
}

func TestProbes(t *testing.T) {
	down := fakeDB{err: errors.New("db down")}
	tests := []struct {
		name       string
		db         fakeDB
		path       string
		wantStatus int
	}{
		{"readyz ok", fakeDB{}, "/readyz", http.StatusOK},
		{"readyz fails closed when database is down", down, "/readyz", http.StatusServiceUnavailable},
		{"healthz stays up when database is down", down, "/healthz", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.db, nil)
			rec := do(r, http.MethodGet, tc.path, "", "", "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("got %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusServiceUnavailable && !strings.Contains(rec.Body.String(), `"unavailable"`) {
				t.Fatalf("body %s", rec.Body.String())
			}
		})
	}
}
