package api_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/api"
)

const evaluatePath = "/v1/receiving/matching:evaluate"

type fakeStore struct{ pingErr error }

func (f fakeStore) Ping(context.Context) error { return f.pingErr }

func newRouter(s fakeStore) (*chi.Mux, *api.API) {
	a := api.New(s)
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

const line = `{"qty":10,"unit":"EA","unitPrice":101.5,"poUnitPrice":100,"receiptState":"POSTED","receivedUninvoicedQty":10}`

func body(lines string) string {
	return `{"currency":"USD","headerTotal":1015,"lines":[` + lines + `]}`
}

func TestEvaluate(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		duties     string
		withTenant bool
		wantStatus int
		wantBody   string
	}{
		{"matched with ppv posting", body(line), "MatchingPolicyMaintain", true, http.StatusOK, `"status":"MATCHED"`},
		{"ppv amount rendered as money", body(line), "MatchingPolicyMaintain", true, http.StatusOK, `"amount":"15.00","currency":"USD"`},
		{"unit echoed", body(line), "MatchingPolicyMaintain", true, http.StatusOK, `"unit":"EA"`},
		{"custom policy turns price into variance", `{"policy":{"priceTolPct":0,"priceTolAbs":0},"currency":"USD","headerTotal":1015,"lines":[` + line + `]}`, "MatchingPolicyMaintain", true, http.StatusOK, `"status":"PRICE_VARIANCE"`},
		{"no tenant", body(line), "", false, http.StatusUnauthorized, "AUTH-0001"},
		{"tenant without duty", body(line), "APInquire", true, http.StatusForbidden, "AUTH-0003"},
		{"malformed json", `{`, "MatchingPolicyMaintain", true, http.StatusBadRequest, "COM-0001"},
		{"unknown field", `{"currency":"USD","extra":1}`, "MatchingPolicyMaintain", true, http.StatusBadRequest, "COM-0001"},
		{"empty lines", `{"currency":"USD","headerTotal":0,"lines":[]}`, "MatchingPolicyMaintain", true, http.StatusBadRequest, `"lines"`},
		{"null lines", `{"currency":"USD","headerTotal":0,"lines":null}`, "MatchingPolicyMaintain", true, http.StatusBadRequest, `"lines"`},
		{"object for lines", `{"currency":"USD","headerTotal":0,"lines":{}}`, "MatchingPolicyMaintain", true, http.StatusBadRequest, "COM-0001"},
		{"string for quantity", body(`{"qty":"10","unitPrice":1,"poUnitPrice":1}`), "MatchingPolicyMaintain", true, http.StatusBadRequest, "COM-0001"},
		{"negative quantity", body(`{"qty":-1,"unitPrice":1,"poUnitPrice":1}`), "MatchingPolicyMaintain", true, http.StatusBadRequest, "lines[0].qty"},
		{"zero quantity", body(`{"qty":0,"unitPrice":1,"poUnitPrice":1}`), "MatchingPolicyMaintain", true, http.StatusBadRequest, "lines[0].qty"},
		{"missing price", body(`{"qty":1,"poUnitPrice":1}`), "MatchingPolicyMaintain", true, http.StatusBadRequest, "lines[0].unitPrice"},
		{"NaN literal", body(`{"qty":NaN,"unitPrice":1,"poUnitPrice":1}`), "MatchingPolicyMaintain", true, http.StatusBadRequest, "COM-0001"},
		{"exponent overflow", body(`{"qty":1,"unitPrice":1e999999999,"poUnitPrice":1}`), "MatchingPolicyMaintain", true, http.StatusBadRequest, "COM-0001"},
		{"missing header total", `{"currency":"USD","lines":[` + line + `]}`, "MatchingPolicyMaintain", true, http.StatusBadRequest, "headerTotal"},
		{"bad currency", `{"currency":"usd","headerTotal":1,"lines":[` + line + `]}`, "MatchingPolicyMaintain", true, http.StatusBadRequest, `"currency"`},
		{"string tolerance", `{"policy":{"priceTolPct":"2"},"currency":"USD","headerTotal":1,"lines":[` + line + `]}`, "MatchingPolicyMaintain", true, http.StatusBadRequest, "COM-0001"},
		{"negative tolerance", `{"policy":{"qtyTolPct":-1},"currency":"USD","headerTotal":1,"lines":[` + line + `]}`, "MatchingPolicyMaintain", true, http.StatusBadRequest, "policy.qtyTolPct"},
		{"unknown match level", `{"policy":{"matchLevel":"FOUR_WAY"},"currency":"USD","headerTotal":1,"lines":[` + line + `]}`, "MatchingPolicyMaintain", true, http.StatusBadRequest, "policy.matchLevel"},
		{"unknown receipt state", body(`{"qty":1,"unitPrice":1,"poUnitPrice":1,"receiptState":"DONE"}`), "MatchingPolicyMaintain", true, http.StatusBadRequest, "lines[0].receiptState"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(fakeStore{})
			rec := do(r, http.MethodPost, evaluatePath, tc.body, tc.duties, tc.withTenant)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s; want %d containing %s", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}

func TestEvaluate_RejectsOverlongLineList(t *testing.T) {
	r, _ := newRouter(fakeStore{})
	lines := strings.TrimSuffix(strings.Repeat(line+",", 501), ",")
	rec := do(r, http.MethodPost, evaluatePath, body(lines), "MatchingPolicyMaintain", true)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"lines"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestProbes(t *testing.T) {
	down := fakeStore{pingErr: errors.New("db down")}
	tests := []struct {
		name       string
		store      fakeStore
		path       string
		wantStatus int
	}{
		{"readyz ok", fakeStore{}, "/readyz", http.StatusOK},
		{"readyz fails closed when database is down", down, "/readyz", http.StatusServiceUnavailable},
		{"healthz stays up when database is down", down, "/healthz", http.StatusOK},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, http.MethodGet, tc.path, "", "", false)
			if rec.Code != tc.wantStatus {
				t.Fatalf("got %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusServiceUnavailable && !strings.Contains(rec.Body.String(), `"status":"unavailable"`) {
				t.Fatalf("body %s", rec.Body.String())
			}
		})
	}
}

func TestMetricsCountBusinessOperationsAndHTTP(t *testing.T) {
	r, a := newRouter(fakeStore{})
	h := a.Instrument(r)
	do(h, http.MethodPost, evaluatePath, body(line), "MatchingPolicyMaintain", true)
	do(h, http.MethodPost, evaluatePath, `{`, "MatchingPolicyMaintain", true)
	do(h, http.MethodPost, evaluatePath, body(line), "", false)
	do(h, http.MethodGet, "/healthz", "", "", false)

	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{
		"receiving_matches_evaluated_total 1",
		"receiving_evaluate_rejected_total 1",
		`receiving_http_requests_total{route="/v1/receiving/matching:evaluate",status="200"} 1`,
		`receiving_http_requests_total{route="/v1/receiving/matching:evaluate",status="400"} 1`,
		`receiving_http_requests_total{route="/healthz",status="200"} 1`,
	} {
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
	r, _ := newRouter(fakeStore{})
	err = chi.Walk(r, func(_, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.Contains(string(spec), "\n  "+route+":") {
			t.Errorf("openapi.yaml does not document %s", route)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(spec), "\n  /metrics:") {
		t.Error("openapi.yaml does not document /metrics")
	}
}
