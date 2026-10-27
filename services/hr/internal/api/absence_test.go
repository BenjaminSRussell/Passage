package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// Every absence route is behind its duty; without it the answer is 403 before
// any validation or store call.
func TestAbsenceRoutesRequireDuty(t *testing.T) {
	worker := testkit.TenantID(9)
	cases := []struct {
		method, target, body string
	}{
		{http.MethodPost, "/v1/hr/absence/plans", `{}`},
		{http.MethodGet, "/v1/hr/absence/plans", ""},
		{http.MethodGet, "/v1/hr/absence/plans/" + worker, ""},
		{http.MethodPost, "/v1/hr/absence/plans/" + worker + "/year-end", `{"planYearStart":"2026-01-01"}`},
		{http.MethodPost, "/v1/hr/absence/plans/" + worker + "/carry-over-expiry", `{"planYearStart":"2026-01-01"}`},
		{http.MethodPost, "/v1/hr/absence/accrue", `{}`},
		{http.MethodPost, "/v1/hr/absence/requests", `{}`},
		{http.MethodGet, "/v1/hr/absence/requests", ""},
		{http.MethodPost, "/v1/hr/absence/requests/" + worker + "/decide", `{"decision":"APPROVE"}`},
		{http.MethodPost, "/v1/hr/absence/requests/" + worker + "/cancel", ""},
		{http.MethodGet, "/v1/hr/workers/" + worker + "/absence/balances", ""},
	}
	for _, c := range cases {
		rec := serve(t, tid(), c.method, c.target, c.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without a duty: status = %d, want 403", c.method, c.target, rec.Code)
		}
	}
}

// TestCreateAbsencePlanValidatesFields names every bad field so a planner can
// fix the whole form at once (8.42.13 defaults apply to omitted fields).
func TestCreateAbsencePlanValidatesFields(t *testing.T) {
	body := `{"code":"us pto!","unit":"WEEKS","accrualRate":"1.23456","maxBalance":"-1",
		"accrualFrequency":"HOURLY","proration":"SOME","carryOverExpiryMonths":99,"planYearStart":"02-29"}`
	rec := serve(t, tid(), http.MethodPost, "/v1/hr/absence/plans", body, dutyAbsenceAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"code", "unit", "accrualRate", "maxBalance", "accrualFrequency", "proration", "carryOverExpiryMonths", "planYearStart"} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("response does not name field %q: %s", field, rec.Body.String())
		}
	}
}

// TestCreateAbsencePlanRejectsFloatsAndUnknownFields: money-like and rate
// values are decimal strings with bounded places (no floats), and unknown
// fields fail the decode.
func TestCreateAbsencePlanRejectsFloatsAndUnknownFields(t *testing.T) {
	rec := serve(t, tid(), http.MethodPost, "/v1/hr/absence/plans",
		`{"code":"PTO","unit":"DAYS","accrualRate":1.25,"maxBalance":"22.5"}`, dutyAbsenceAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("numeric rate: status = %d, want 400", rec.Code)
	}
	rec = serve(t, tid(), http.MethodPost, "/v1/hr/absence/plans", `{"code":"PTO","surprise":true}`, dutyAbsenceAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: status = %d, want 400", rec.Code)
	}
}

// TestRunAccrualRejectsBadPeriods: the period must be ordered and at most a
// year, and the plan id must be a UUID.
func TestRunAccrualRejectsBadPeriods(t *testing.T) {
	cases := map[string]string{
		"reversed":    `{"planId":"` + testkit.TenantID(1) + `","periodStart":"2026-03-31","periodEnd":"2026-03-01"}`,
		"too long":    `{"planId":"` + testkit.TenantID(1) + `","periodStart":"2026-01-01","periodEnd":"2028-01-01"}`,
		"bad plan id": `{"planId":"nope","periodStart":"2026-03-01","periodEnd":"2026-03-31"}`,
	}
	for name, body := range cases {
		rec := serve(t, tid(), http.MethodPost, "/v1/hr/absence/accrue", body, dutyAbsenceAdmin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400: %s", name, rec.Code, rec.Body.String())
		}
	}
}

// TestCreateAbsenceRequestValidatesDatesAndQuantity: quantities are positive
// with at most two places; a request ending before it starts is refused.
func TestCreateAbsenceRequestValidatesDatesAndQuantity(t *testing.T) {
	base := `{"workerId":"` + testkit.TenantID(2) + `","planId":"` + testkit.TenantID(3) +
		`","from":"2026-05-04","to":"%s","quantity":"%s"}`
	cases := []struct{ to, qty, field string }{
		{"2026-05-01", "1", "to"},
		{"2027-06-01", "1", "to"},
		{"2026-05-05", "0", "quantity"},
		{"2026-05-05", "1.005", "quantity"},
		{"2026-05-05", "-2", "quantity"},
	}
	for _, c := range cases {
		body := strings.Replace(strings.Replace(base, "%s", c.to, 1), "%s", c.qty, 1)
		rec := serve(t, tid(), http.MethodPost, "/v1/hr/absence/requests", body, dutyEssAbsence)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), c.field) {
			t.Errorf("to=%s qty=%s: status %d body %s; want 400 naming %s", c.to, c.qty, rec.Code, rec.Body.String(), c.field)
		}
	}
}

// TestDecideAbsenceRequestNeedsDutyAndValidDecision: MssApprove gates the
// decision; only APPROVE or REJECT is accepted; a malformed id is a 404.
func TestDecideAbsenceRequestNeedsDutyAndValidDecision(t *testing.T) {
	target := "/v1/hr/absence/requests/" + testkit.TenantID(4) + "/decide"
	rec := serve(t, tid(), http.MethodPost, target, `{"decision":"APPROVE"}`, dutyEssAbsence)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("employee deciding: status = %d, want 403", rec.Code)
	}
	rec = serve(t, tid(), http.MethodPost, target, `{"decision":"MAYBE"}`, dutyMssApprove)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "decision") {
		t.Fatalf("bad decision: status %d body %s; want 400 naming decision", rec.Code, rec.Body.String())
	}
	rec = serve(t, tid(), http.MethodPost, "/v1/hr/absence/requests/not-a-uuid/decide", `{"decision":"APPROVE"}`, dutyMssApprove)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("malformed request id: status = %d, want 404", rec.Code)
	}
}

func TestAbsenceMetricsAreExposed(t *testing.T) {
	rec := httptest.NewRecorder()
	New(nil).Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, name := range []string{"hr_absence_plans_created_total", "hr_absence_accrual_runs_total",
		"hr_absence_requests_created_total", "hr_absence_decisions_total", "hr_absence_year_closes_total"} {
		if !strings.Contains(rec.Body.String(), name) {
			t.Errorf("metrics missing %s", name)
		}
	}
}

// TestAbsenceListAndCloseValidation: bad filters, dates and ids are refused
// before any store call, and an employee-only caller without a sign-in user
// cannot list requests at all.
func TestAbsenceListAndCloseValidation(t *testing.T) {
	rec := serve(t, tid(), http.MethodGet, "/v1/hr/absence/requests?status=PENDING&workerId=nope", "", dutyEssAbsence, dutyMssApprove)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "status") || !strings.Contains(rec.Body.String(), "workerId") {
		t.Fatalf("bad filters: status %d body %s", rec.Code, rec.Body.String())
	}
	rec = serve(t, tid(), http.MethodGet, "/v1/hr/absence/requests?limit=0", "", dutyEssAbsence)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("limit 0: status = %d, want 400", rec.Code)
	}
	p, _ := tenant.FromContext(testkit.Ctx(tid(), dutyEssAbsence))
	p.UserID = ""
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), p)))
		})
	})
	New(nil).Register(r)
	anon := httptest.NewRecorder()
	r.ServeHTTP(anon, httptest.NewRequest(http.MethodGet, "/v1/hr/absence/requests", nil))
	if anon.Code != http.StatusForbidden || !strings.Contains(anon.Body.String(), "HCM-ABS-NOT-OWN") {
		t.Fatalf("employee without a user: status %d body %s, want 403 HCM-ABS-NOT-OWN", anon.Code, anon.Body.String())
	}

	plan := "/v1/hr/absence/plans/" + testkit.TenantID(5)
	for _, target := range []string{plan + "/year-end", plan + "/carry-over-expiry"} {
		rec = serve(t, tid(), http.MethodPost, target, `{"planYearStart":"2026-13-01"}`, dutyAbsenceAdmin)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "planYearStart") {
			t.Errorf("%s bad date: status %d body %s", target, rec.Code, rec.Body.String())
		}
	}
	rec = serve(t, tid(), http.MethodPost, "/v1/hr/absence/plans/not-a-uuid/year-end", `{"planYearStart":"2026-01-01"}`, dutyAbsenceAdmin)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "HCM-ABS-PLAN-NOT-FOUND") {
		t.Fatalf("malformed plan id: status %d body %s", rec.Code, rec.Body.String())
	}
	rec = serve(t, tid(), http.MethodGet, "/v1/hr/workers/"+testkit.TenantID(6)+"/absence/balances?asOf=yesterday", "", dutyEssAbsence)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "asOf") {
		t.Fatalf("bad asOf: status %d body %s", rec.Code, rec.Body.String())
	}
	rec = serve(t, tid(), http.MethodPost, "/v1/hr/absence/requests/not-a-uuid/cancel", "", dutyEssAbsence)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "HCM-ABS-REQ-NOT-FOUND") {
		t.Fatalf("malformed request id: status %d body %s", rec.Code, rec.Body.String())
	}
}
