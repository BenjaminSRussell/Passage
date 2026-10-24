package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/payables/internal/api"
)

const (
	previewPath = "/v1/payables/settlements:preview"
	workedBody  = `{"settlementDate":"2026-01-10","accountingCurrency":"USD",` +
		`"credit":{"currency":"EUR","amount":"980.00","rate":"1.08"},` +
		`"invoices":[{"id":"inv-1","currency":"EUR","amount":"1000.00","rate":"1.10","dueDate":"2026-01-30","discountPct":"2","discountDate":"2026-01-11"}]}`
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func fixedNow() time.Time { return time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC) }

func newRouter(db fakeDB) (*chi.Mux, *api.API) {
	a := api.New(db, fixedNow)
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func do(r http.Handler, method, path, body string, tenantHeader bool, duties string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if tenantHeader {
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
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

func TestPreview(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		tenant     bool
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"worked example", workedBody, true, "settlement.read", http.StatusOK, `"amount":"1058.40"`},
		{"no tenant header", workedBody, false, "", http.StatusUnauthorized, "AUTH-0001"},
		{"missing duty", workedBody, true, "", http.StatusForbidden, "AUTH-0003"},
		{"malformed JSON", `{`, true, "settlement.read", http.StatusBadRequest, "COM-0001"},
		{"unknown field", mutated(t, `"accountingCurrency"`, `"x":1,"accountingCurrency"`), true, "settlement.read", http.StatusBadRequest, "COM-0001"},
		{"amount as JSON number", mutated(t, `"980.00"`, `980`), true, "settlement.read", http.StatusBadRequest, "COM-0001"},
		{"null body", `null`, true, "settlement.read", http.StatusBadRequest, "PAY-0001"},
		{"no invoices", `{"settlementDate":"2026-01-10","accountingCurrency":"USD","credit":{"currency":"EUR","amount":"1.00","rate":"1.1"},"invoices":[]}`, true, "settlement.read", http.StatusBadRequest, "PAY-0001"},
		{"zero rate", mutated(t, `"rate":"1.10"`, `"rate":"0"`), true, "settlement.read", http.StatusBadRequest, "PAY-0001"},
		{"non-ISO date", mutated(t, `2026-01-10`, `10-01-2026`), true, "settlement.read", http.StatusBadRequest, "PAY-0001"},
		{"discount above 100", mutated(t, `"discountPct":"2"`, `"discountPct":"150"`), true, "settlement.read", http.StatusBadRequest, "PAY-0001"},
		{"foreign currency without a rate", mutated(t, `"rate":"1.08"`, `"rate":""`), true, "settlement.read", http.StatusUnprocessableEntity, "PAY-0002"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(fakeDB{})
			rec := do(r, http.MethodPost, previewPath, tc.body, tc.tenant, tc.duties)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestPreviewProblemEnvelopeCarriesEveryKey(t *testing.T) {
	r, _ := newRouter(fakeDB{})
	rec := do(r, http.MethodPost, previewPath, `{`, true, "settlement.read")
	if got := rec.Header().Get("Content-Type"); got != "application/problem+json" {
		t.Fatalf("content type %q", got)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"type", "title", "status", "detail", "instance", "code", "serviceCode", "traceId"} {
		if _, ok := body[key]; !ok {
			t.Errorf("problem body missing %q: %s", key, rec.Body.String())
		}
	}
	if body["code"] != "validation_failed" || body["serviceCode"] != "COM-0001" || body["instance"] != previewPath {
		t.Errorf("unexpected problem %v", body)
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
			r, _ := newRouter(tc.db)
			rec := do(r, http.MethodGet, tc.path, "", false, "")
			if rec.Code != tc.wantStatus {
				t.Fatalf("got %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusServiceUnavailable && !strings.Contains(rec.Body.String(), `"unavailable"`) {
				t.Fatalf("body %s", rec.Body.String())
			}
		})
	}
}

func TestReadinessFailureIsLoggedWithCause(t *testing.T) {
	var out bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	r, _ := newRouter(fakeDB{err: errors.New("db down")})
	do(r, http.MethodGet, "/readyz", "", false, "")
	if !strings.Contains(out.String(), "db down") || !strings.Contains(out.String(), "correlationId") {
		t.Fatalf("log line missing cause or correlation id: %s", out.String())
	}
}

func TestMetricsCountPreviews(t *testing.T) {
	r, a := newRouter(fakeDB{})
	do(r, http.MethodPost, previewPath, workedBody, true, "settlement.read")
	do(r, http.MethodPost, previewPath, `{}`, true, "settlement.read")
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"payables_settlement_previews_total 1", "payables_settlement_preview_failures_total 1"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("metrics missing %q in %s", want, rec.Body.String())
		}
	}
}

func TestOpenAPIDocumentsEveryRoute(t *testing.T) {
	spec, err := os.ReadFile("../../api/openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, _ := newRouter(fakeDB{})
	err = chi.Walk(r, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.Contains(string(spec), "\n  "+route+":") {
			t.Errorf("openapi.yaml does not document %s", route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"x-passage-duty: settlement.read", "/metrics", "/healthz", "/readyz"} {
		if !strings.Contains(string(spec), want) {
			t.Errorf("openapi.yaml missing %q", want)
		}
	}
}
