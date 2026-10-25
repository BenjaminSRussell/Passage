package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/close/internal/api"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

const checkPath = "/v1/close/period-statuses:check"

type fakeStore struct {
	rows    []domain.Row
	readErr error
	pingErr error
}

func (f fakeStore) PeriodRows(context.Context, string, string) ([]domain.Row, error) {
	return f.rows, f.readErr
}

func (f fakeStore) Ping(context.Context) error { return f.pingErr }

func body(source, group string) string {
	return `{"ledgerId":"` + testkit.TenantID(7) + `","fiscalPeriodId":"` + testkit.TenantID(8) + `","postingDate":"2026-01-15","sourceSystem":"` + source + `","groupId":"` + group + `"}`
}

func newRouter(s fakeStore) (*chi.Mux, *api.API) {
	a := api.New(s)
	r := httpx.NewRouter()
	a.Register(r)
	return r, a
}

func do(r http.Handler, method, path, payload string, tenantHeader bool, duties string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(payload))
	if tenantHeader {
		req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
		req.Header.Set("X-Passage-Duties", duties)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestCheck(t *testing.T) {
	apClosed := []domain.Row{{Module: domain.ModuleAP, Status: domain.StatusClosed}, {Module: domain.ModuleGL, Status: domain.StatusOpen}}
	held := []domain.Row{{Module: domain.ModuleAP, Status: domain.StatusOnHold, AllowedGroupID: "controllers"}}
	tests := []struct {
		name       string
		store      fakeStore
		payload    string
		tenant     bool
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"no tenant header", fakeStore{}, body("ap", ""), false, "", http.StatusUnauthorized, "AUTH-0001"},
		{"missing duty", fakeStore{}, body("ap", ""), true, "", http.StatusForbidden, "AUTH-0003"},
		{"AP closed rejects AP posting", fakeStore{rows: apClosed}, body("ap", ""), true, "period.check", http.StatusUnprocessableEntity, `"serviceCode":"LEDGER-0013"`},
		{"closed rejection uses the contract code", fakeStore{rows: apClosed}, body("ap", ""), true, "period.check", http.StatusUnprocessableEntity, `"code":"period_closed"`},
		{"never opened rejects", fakeStore{}, body("manual", ""), true, "period.check", http.StatusUnprocessableEntity, "LEDGER-0013"},
		{"on hold rejects other group", fakeStore{rows: held}, body("ap", "others"), true, "period.check", http.StatusForbidden, "LEDGER-0014"},
		{"on hold without group rejects", fakeStore{rows: held}, body("ap", ""), true, "period.check", http.StatusForbidden, "LEDGER-0014"},
		{"on hold allows listed group", fakeStore{rows: held}, body("ap", "controllers"), true, "period.check", http.StatusOK, `"allowed":true`},
		{"AP closed, manual GL allowed", fakeStore{rows: apClosed}, body("manual", ""), true, "period.check", http.StatusOK, `"module":"gl"`},
		{"unknown source with ALL open", fakeStore{rows: []domain.Row{{Module: domain.ModuleAll, Status: domain.StatusOpen}}}, body("mystery", ""), true, "period.check", http.StatusOK, `"module":"all"`},
		{"unknown source rejected when AP closed", fakeStore{rows: append([]domain.Row{{Module: domain.ModuleAll, Status: domain.StatusOpen}}, apClosed...)}, body("mystery", ""), true, "period.check", http.StatusUnprocessableEntity, "LEDGER-0013"},
		{"omitted source rejected when AP closed", fakeStore{rows: append([]domain.Row{{Module: domain.ModuleAll, Status: domain.StatusOpen}}, apClosed...)}, body("", ""), true, "period.check", http.StatusUnprocessableEntity, "LEDGER-0013"},
		{"store unavailable fails closed", fakeStore{readErr: errors.New("db down")}, body("ap", ""), true, "period.check", http.StatusInternalServerError, "COM-0500"},
		{"malformed JSON", fakeStore{}, `{`, true, "period.check", http.StatusBadRequest, "COM-0001"},
		{"unknown field", fakeStore{}, `{"x":1}`, true, "period.check", http.StatusBadRequest, "COM-0001"},
		{"empty body", fakeStore{}, ``, true, "period.check", http.StatusBadRequest, "COM-0001"},
		{"null body", fakeStore{}, `null`, true, "period.check", http.StatusBadRequest, "CLOSE-0004"},
		{"ledger id not a UUID", fakeStore{}, strings.Replace(body("ap", ""), testkit.TenantID(7), "ledger-1", 1), true, "period.check", http.StatusBadRequest, `"ledgerId"`},
		{"posting date not a date", fakeStore{}, strings.Replace(body("ap", ""), "2026-01-15", "15/01/2026", 1), true, "period.check", http.StatusBadRequest, `"postingDate"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := do(r, http.MethodPost, checkPath, tc.payload, tc.tenant, tc.duties)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s, want %d containing %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if tc.wantStatus >= 400 && tc.wantBody != "AUTH-0001" && tc.wantBody != "AUTH-0003" {
				var problem map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
					t.Fatal(err)
				}
				for _, key := range []string{"type", "title", "status", "instance", "code", "serviceCode", "traceId"} {
					if _, ok := problem[key]; !ok {
						t.Errorf("problem body missing %q: %s", key, rec.Body.String())
					}
				}
			}
		})
	}
}

func TestCheck_StoreFailureIsLoggedWithCorrelationID(t *testing.T) {
	var out bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&out, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	r, _ := newRouter(fakeStore{readErr: errors.New("db down")})
	do(r, http.MethodPost, checkPath, body("ap", ""), true, "period.check")
	if !strings.Contains(out.String(), "db down") || !strings.Contains(out.String(), "correlationId") {
		t.Fatalf("store failure must log the original error and correlationId, got %s", out.String())
	}
}

func TestReadyz(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{{"database reachable", nil, http.StatusOK}, {"database down", errors.New("down"), http.StatusServiceUnavailable}}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(fakeStore{pingErr: tc.err})
			if rec := do(r, http.MethodGet, "/readyz", "", false, ""); rec.Code != tc.want {
				t.Fatalf("got %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestHealthz(t *testing.T) {
	r, _ := newRouter(fakeStore{pingErr: errors.New("down")})
	if rec := do(r, http.MethodGet, "/healthz", "", false, ""); rec.Code != http.StatusOK {
		t.Fatalf("liveness must not depend on the database, got %d", rec.Code)
	}
}

func TestMetrics_CountChecksAndRejections(t *testing.T) {
	r, a := newRouter(fakeStore{rows: []domain.Row{{Module: domain.ModuleAP, Status: domain.StatusClosed}}})
	do(r, http.MethodPost, checkPath, body("ap", ""), true, "period.check")
	rec := httptest.NewRecorder()
	a.Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, want := range []string{"close_period_checks_total 1", "close_period_rejections_total 1", "close_period_check_failures_total 0"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("metrics missing %q: %s", want, rec.Body.String())
		}
	}
}
