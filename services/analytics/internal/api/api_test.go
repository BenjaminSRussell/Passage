package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

// newTestServer mounts the routes behind the tenant middleware, the way main
// does. The store is nil: none of these routes reach the database.
func newTestServer() http.Handler {
	r := httpx.NewRouter()
	New(nil).Register(r)
	return tenant.Middleware(r)
}

func do(t *testing.T, h http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(tenant.HeaderTenant, "00000000-0000-7000-8000-000000000001")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var viewer = map[string]string{tenant.HeaderDuties: DutyView}

func withLE(le string) map[string]string {
	return map[string]string{tenant.HeaderDuties: DutyView, tenant.HeaderLegalEntity: le}
}

func TestListMetrics_RequiresDutyAndReturnsPublishedCatalog(t *testing.T) {
	h := newTestServer()
	if rec := do(t, h, http.MethodGet, "/v1/analytics/metrics", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("without duty: status %d, want 403", rec.Code)
	}
	rec := do(t, h, http.MethodGet, "/v1/analytics/metrics", "", map[string]string{tenant.HeaderDuties: DutyView})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got MetricsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, m := range got.Items {
		ids[m.ID] = true
	}
	for _, want := range []string{"revenue", "cogs", "otif_pct", "oee_pct"} {
		if !ids[want] {
			t.Errorf("catalog is missing %s", want)
		}
	}
}

func TestQuery_MirrorsLegalEntityScopeIntoPlan(t *testing.T) {
	h := newTestServer()
	body := `{"measures":["revenue"],"dimensions":["account_code"],"from":"2026-01-01","to":"2026-03-31"}`
	rec := do(t, h, http.MethodPost, "/v1/analytics/query", body, withLE("USMF"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var got QueryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Plan.Model != "fin_journal_line" || got.Plan.Reason != "" {
		t.Fatalf("plan = %+v", got.Plan)
	}
	if len(got.Plan.Filters) != 1 || got.Plan.Filters[0].Member != "fin_journal_line.legal_entity_id" ||
		got.Plan.Filters[0].Values[0] != "USMF" {
		t.Fatalf("filters = %+v, want legal entity USMF", got.Plan.Filters)
	}
}

func TestQuery_NoHeaderMeansWholeTenant(t *testing.T) {
	h := newTestServer()
	body := `{"measures":["revenue"],"from":"2026-01-01","to":"2026-01-31"}`
	rec := do(t, h, http.MethodPost, "/v1/analytics/query", body, withLE(""))
	var got QueryResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d err %v", rec.Code, err)
	}
	if len(got.Plan.Filters) != 0 || got.Plan.Reason != "" {
		t.Fatalf("unrestricted plan = %+v", got.Plan)
	}
	// The OpenAPI Plan requires arrays: an empty filter or dimension list is [], never null.
	if body := rec.Body.String(); !strings.Contains(body, `"filters":[]`) || !strings.Contains(body, `"dimensions":[]`) {
		t.Fatalf("empty lists must encode as []: %s", body)
	}
}

func TestQuery_Problems(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		code   string
		field  string
	}{
		{"not json", `{"measures":`, http.StatusBadRequest, domain.CodeValidation, ""},
		{"no measures", `{"from":"2026-01-01","to":"2026-01-31"}`, http.StatusBadRequest, domain.CodeValidation, "measures"},
		{"bad date", `{"measures":["revenue"],"from":"01/01/2026","to":"2026-01-31"}`, http.StatusBadRequest, domain.CodeValidation, ""},
		{"span over five years", `{"measures":["revenue"],"from":"2020-01-01","to":"2026-06-01"}`, http.StatusBadRequest, domain.CodeValidation, "to"},
		{"unknown metric", `{"measures":["nope"],"from":"2026-01-01","to":"2026-01-31"}`, http.StatusBadRequest, domain.CodeValidation, "measures"},
		{"mixed grain", `{"measures":["revenue","otif_pct"],"from":"2026-01-01","to":"2026-01-31"}`, http.StatusBadRequest, domain.CodeValidation, "measures"},
		{"dimension not allowed", `{"measures":["dso_days"],"dimensions":["account_code"],"from":"2026-01-01","to":"2026-01-31"}`, http.StatusBadRequest, domain.CodeValidation, "dimensions"},
		{"site is not a finance dimension", `{"measures":["revenue"],"dimensions":["site_id"],"from":"2026-01-01","to":"2026-01-31"}`, http.StatusBadRequest, domain.CodeValidation, "dimensions"},
	}
	h := newTestServer()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, "/v1/analytics/query", tc.body, withLE("USMF"))
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("body %s does not carry code %s", rec.Body.String(), tc.code)
			}
			if tc.field != "" {
				var p httpx.Problem
				if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || p.Errors[tc.field] == "" {
					t.Fatalf("problem %s must name field %s: %v", rec.Body.String(), tc.field, err)
				}
			}
		})
	}
}

func TestQuery_RequiresTenant(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/analytics/query", bytes.NewBufferString("{}"))
	newTestServer().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", rec.Code)
	}
}

func TestReconciliation_RejectsBadDateBeforeStore(t *testing.T) {
	h := newTestServer()
	headers := map[string]string{tenant.HeaderDuties: DutyFinance}
	rec := do(t, h, http.MethodGet, "/v1/analytics/reconciliation?date=yesterday", "", headers)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), domain.CodeValidation) {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/v1/analytics/reconciliation?date=2026-01-31", "", viewer); rec.Code != http.StatusForbidden {
		t.Fatalf("without FinanceView: status %d, want 403", rec.Code)
	}
}
