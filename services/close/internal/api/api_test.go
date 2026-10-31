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
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/close/internal/api"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
	"github.com/BenjaminSRussell/passage/services/close/internal/store"
)

const checkPath = "/v1/close/period-statuses:check"

type fakeStore struct {
	rows        []domain.Row
	spans       []domain.Span
	readErr     error
	created     bool
	registerErr error
	pingErr     error
}

func (f fakeStore) PeriodRows(context.Context, string, string) ([]domain.Row, error) {
	return f.rows, f.readErr
}

func (f fakeStore) PeriodSpans(context.Context, string, string, time.Time) ([]domain.Span, error) {
	return f.spans, f.readErr
}

func (f fakeStore) RegisterPeriod(context.Context, store.PeriodRegistration) (bool, error) {
	return f.created, f.registerErr
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

const resolvePath = "/v1/close/periods:resolve"

func resolveQuery(ledger, date string) string {
	return resolvePath + "?ledgerId=" + ledger + "&postingDate=" + date + "&sourceSystem=ap"
}

func doResolve(r http.Handler, target, legalEntity, duties string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
	req.Header.Set("X-Passage-Duties", duties)
	if legalEntity != "" {
		req.Header.Set("X-Passage-Legal-Entity", legalEntity)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func span(id string, rows ...domain.Row) domain.Span {
	return domain.Span{FiscalPeriodID: id, Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC), Rows: rows}
}

func TestResolve(t *testing.T) {
	ledger, entity := testkit.TenantID(7), testkit.TenantID(9)
	period := testkit.TenantID(8)
	apClosed := span(period, domain.Row{Module: domain.ModuleAP, Status: domain.StatusClosed})
	tests := []struct {
		name       string
		store      fakeStore
		target     string
		entity     string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"open period", fakeStore{spans: []domain.Span{span(period, domain.Row{Module: domain.ModuleAP, Status: domain.StatusOpen})}}, resolveQuery(ledger, "2026-01-15"), entity, "period.check", http.StatusOK, `"fiscalPeriodId":"` + period + `","startDate":"2026-01-01","endDate":"2026-01-31","module":"ap","status":"open"`},
		{"closed period is returned, not rejected", fakeStore{spans: []domain.Span{apClosed}}, resolveQuery(ledger, "2026-01-15"), entity, "period.check", http.StatusOK, `"status":"closed"`},
		{"falls back to the ALL row", fakeStore{spans: []domain.Span{span(period, domain.Row{Module: domain.ModuleAll, Status: domain.StatusOnHold})}}, resolveQuery(ledger, "2026-01-15"), entity, "period.check", http.StatusOK, `"module":"all","status":"on_hold"`},
		{"no statuses yet is never opened", fakeStore{spans: []domain.Span{span(period)}}, resolveQuery(ledger, "2026-01-15"), entity, "period.check", http.StatusOK, `"status":"never_opened"`},
		{"no period", fakeStore{}, resolveQuery(ledger, "2026-01-15"), entity, "period.check", http.StatusNotFound, `"serviceCode":"CLOSE-0005"`},
		{"ambiguous", fakeStore{spans: []domain.Span{span(period), span(testkit.TenantID(10))}}, resolveQuery(ledger, "2026-01-15"), entity, "period.check", http.StatusConflict, `"serviceCode":"CLOSE-0006"`},
		{"missing duty", fakeStore{}, resolveQuery(ledger, "2026-01-15"), entity, "", http.StatusForbidden, "AUTH-0003"},
		{"no legal entity", fakeStore{}, resolveQuery(ledger, "2026-01-15"), "", "period.check", http.StatusBadRequest, `"legalEntityId"`},
		{"several legal entities", fakeStore{}, resolveQuery(ledger, "2026-01-15"), entity + "," + testkit.TenantID(11), "period.check", http.StatusBadRequest, `"legalEntityId"`},
		{"ledger not a UUID", fakeStore{}, resolveQuery("ledger-1", "2026-01-15"), entity, "period.check", http.StatusBadRequest, `"ledgerId"`},
		{"posting date not a date", fakeStore{}, resolveQuery(ledger, "15/01/2026"), entity, "period.check", http.StatusBadRequest, `"postingDate"`},
		{"store unavailable fails closed", fakeStore{readErr: errors.New("db down")}, resolveQuery(ledger, "2026-01-15"), entity, "period.check", http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			rec := doResolve(r, tc.target, tc.entity, tc.duties)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s, want %d containing %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
			if rec.Code >= 400 && rec.Code != http.StatusForbidden {
				if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
					t.Fatalf("content type %q", ct)
				}
			}
		})
	}
}

func TestRegisterPeriod(t *testing.T) {
	ledger, entity, period := testkit.TenantID(7), testkit.TenantID(9), testkit.TenantID(8)
	good := `{"ledgerId":"` + ledger + `","startDate":"2026-01-01","endDate":"2026-01-31"}`
	tests := []struct {
		name       string
		store      fakeStore
		payload    string
		entity     string
		duties     string
		wantStatus int
		wantBody   string
	}{
		{"created", fakeStore{created: true}, good, entity, "period.maintain", http.StatusCreated, `"startDate":"2026-01-01"`},
		{"replayed", fakeStore{}, good, entity, "period.maintain", http.StatusOK, `"endDate":"2026-01-31"`},
		{"check duty is not enough", fakeStore{}, good, entity, "period.check", http.StatusForbidden, "AUTH-0003"},
		{"no legal entity", fakeStore{}, good, "", "period.maintain", http.StatusBadRequest, `"legalEntityId"`},
		{"end before start", fakeStore{}, `{"ledgerId":"` + ledger + `","startDate":"2026-02-01","endDate":"2026-01-31"}`, entity, "period.maintain", http.StatusBadRequest, `"endDate"`},
		{"bad ledger", fakeStore{}, `{"ledgerId":"x","startDate":"2026-01-01","endDate":"2026-01-31"}`, entity, "period.maintain", http.StatusBadRequest, `"ledgerId"`},
		{"overlap conflicts", fakeStore{registerErr: &domain.Error{Code: domain.CodeInvalidTransition, Detail: "overlap"}}, good, entity, "period.maintain", http.StatusConflict, `"serviceCode":"CLOSE-0001"`},
		{"store failure is hidden", fakeStore{registerErr: errors.New("db down")}, good, entity, "period.maintain", http.StatusInternalServerError, "COM-0500"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newRouter(tc.store)
			req := httptest.NewRequest(http.MethodPut, "/v1/close/periods/"+period, strings.NewReader(tc.payload))
			req.Header.Set("X-Passage-Tenant", testkit.TenantID(1))
			req.Header.Set("X-Passage-Duties", tc.duties)
			if tc.entity != "" {
				req.Header.Set("X-Passage-Legal-Entity", tc.entity)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("got %d %s, want %d containing %q", rec.Code, rec.Body.String(), tc.wantStatus, tc.wantBody)
			}
		})
	}
}
