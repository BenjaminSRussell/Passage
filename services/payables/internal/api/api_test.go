package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestAuthRejectionBodiesMatchKernel(t *testing.T) {
	r, _ := newRouter(fakeDB{})
	unauth := do(r, http.MethodPost, previewPath, workedBody, false, "")
	forbidden := do(r, http.MethodPost, previewPath, workedBody, true, "")
	malformedReq := httptest.NewRequest(http.MethodPost, previewPath, strings.NewReader(workedBody))
	malformedReq.Header.Set("X-Passage-Tenant", "not-a-uuid")
	malformedReq.Header.Set("X-Passage-Duties", "settlement.read")
	malformed := httptest.NewRecorder()
	r.ServeHTTP(malformed, malformedReq)

	if unauth.Code != http.StatusUnauthorized || forbidden.Code != http.StatusForbidden || malformed.Code != http.StatusUnauthorized {
		t.Fatalf("statuses %d %d %d", unauth.Code, forbidden.Code, malformed.Code)
	}
	if ct := unauth.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("401 content type %q", ct)
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{"403": forbidden, "malformed 401": malformed} {
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("%s content type %q", name, ct)
		}
	}
	decode := func(name string, rec *httptest.ResponseRecorder) map[string]any {
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s body is not JSON: %v", name, err)
		}
		return body
	}
	for _, key := range []string{"type", "title", "status", "detail"} {
		if _, ok := decode("403", forbidden)[key]; !ok {
			t.Errorf("403 body missing %q: %s", key, forbidden.Body.String())
		}
	}
	for name, rec := range map[string]*httptest.ResponseRecorder{"401": unauth, "403": forbidden} {
		if _, ok := decode(name, rec)["serviceCode"]; ok {
			t.Errorf("%s body carries serviceCode: %s", name, rec.Body.String())
		}
	}
	if body := decode("malformed 401", malformed); body["serviceCode"] != "AUTH-0001" || body["code"] != "forbidden_tenant" || body["traceId"] == "" {
		t.Errorf("malformed tenant problem %v", body)
	}
	if !strings.Contains(unauth.Body.String(), `"code":"AUTH-0001"`) || !strings.Contains(forbidden.Body.String(), `"code":"AUTH-0003"`) {
		t.Errorf("unexpected codes: %s / %s", unauth.Body.String(), forbidden.Body.String())
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

func scrape(a *api.API) string {
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestMetricsSeparateClientErrorsFromServerFaults(t *testing.T) {
	a := api.New(fakeDB{}, fixedNow)
	r := httpx.NewRouter()
	a.Register(r)
	h := a.Instrument(r)
	do(h, http.MethodPost, previewPath, workedBody, true, "settlement.read")
	do(h, http.MethodPost, previewPath, `{}`, true, "settlement.read")
	do(h, http.MethodPost, previewPath, workedBody, false, "")
	do(h, http.MethodPost, previewPath, workedBody, true, "")
	malformed := httptest.NewRequest(http.MethodPost, previewPath, strings.NewReader(workedBody))
	malformed.Header.Set("X-Passage-Tenant", "not-a-uuid")
	malformed.Header.Set("X-Passage-Duties", "settlement.read")
	h.ServeHTTP(httptest.NewRecorder(), malformed)
	got := scrape(a)
	for _, want := range []string{
		"payables_settlement_previews_total 1",
		"payables_settlement_preview_client_errors_total 2",
		"payables_settlement_preview_server_faults_total 0",
		`payables_http_requests_total{route="/v1/payables/settlements:preview",status="200"} 1`,
		`payables_http_requests_total{route="/v1/payables/settlements:preview",status="400"} 1`,
		`payables_http_requests_total{route="unmatched",status="401"} 1`,
		`payables_http_requests_total{route="/v1/payables/settlements:preview",status="401"} 1`,
		`payables_http_requests_total{route="/v1/payables/settlements:preview",status="403"} 1`,
		`payables_http_request_duration_seconds_bucket{route="/v1/payables/settlements:preview",le="+Inf"} 4`,
		`payables_http_request_duration_seconds_count{route="/v1/payables/settlements:preview"} 4`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("metrics missing %q in\n%s", want, got)
		}
	}
	assertBucketsMonotonic(t, got, `payables_http_request_duration_seconds_bucket{route="/v1/payables/settlements:preview"`)
	if !strings.Contains(got, `payables_http_request_duration_seconds_sum{route="/v1/payables/settlements:preview"}`) {
		t.Errorf("metrics missing histogram _sum in\n%s", got)
	}
}

func assertBucketsMonotonic(t *testing.T, scrape, prefix string) {
	t.Helper()
	previous, seen := int64(-1), 0
	for _, line := range strings.Split(scrape, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		var n int64
		if _, err := fmt.Sscan(line[strings.LastIndex(line, " ")+1:], &n); err != nil {
			t.Fatalf("bad bucket line %q", line)
		}
		if n < previous {
			t.Errorf("bucket count decreased at %q", line)
		}
		previous = n
		seen++
	}
	if seen < 2 {
		t.Errorf("found %d buckets for %s", seen, prefix)
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
