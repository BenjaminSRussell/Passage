package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// TestBenefitRoutesRequireTheirDuty: every benefit route answers 403 before
// any validation or store call without its duty; an employee (EssBenefits)
// cannot author plans, simulate or end enrollments.
func TestBenefitRoutesRequireTheirDuty(t *testing.T) {
	id := testkit.TenantID(8)
	cases := []struct {
		method, target, body string
	}{
		{http.MethodPost, "/v1/hr/benefits/plans", `{}`},
		{http.MethodGet, "/v1/hr/benefits/plans", ""},
		{http.MethodGet, "/v1/hr/benefits/plans/" + id, ""},
		{http.MethodGet, "/v1/hr/benefits/plans/" + id + "/simulate?asOf=2026-01-01", ""},
		{http.MethodPost, "/v1/hr/benefits/life-events", `{}`},
		{http.MethodPost, "/v1/hr/benefits/enrollments", `{}`},
		{http.MethodPost, "/v1/hr/benefits/enrollments/" + id + "/end", `{}`},
		{http.MethodGet, "/v1/hr/workers/" + id + "/benefits", ""},
	}
	for _, c := range cases {
		if rec := serve(t, tid(), c.method, c.target, c.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without a duty: status = %d, want 403", c.method, c.target, rec.Code)
		}
	}
	for _, c := range []struct{ method, target, body string }{
		{http.MethodPost, "/v1/hr/benefits/plans", `{}`},
		{http.MethodGet, "/v1/hr/benefits/plans/" + id + "/simulate?asOf=2026-01-01", ""},
		{http.MethodPost, "/v1/hr/benefits/enrollments/" + id + "/end", `{}`},
	} {
		if rec := serve(t, tid(), c.method, c.target, c.body, dutyEssBenefits); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as an employee: status = %d, want 403", c.method, c.target, rec.Code)
		}
	}
}

// TestCreateBenefitPlanNamesEveryBadField lets an administrator fix the whole
// plan form at once (FR-42.8): a rule that does not compile is refused before
// it is stored.
func TestCreateBenefitPlanNamesEveryBadField(t *testing.T) {
	body := `{"code":"x y","planType":"DENTIST","currency":"usd","eligibilityRule":"service_days >=",
		"enrollmentStart":"2026-13-01","enrollmentEnd":"2026-01-01","waitingDays":-1,
		"providerPartyId":"nope","options":[]}`
	rec := serve(t, tid(), http.MethodPost, "/v1/hr/benefits/plans", body, dutySetupAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{`"code"`, `"planType"`, `"currency"`, `"eligibilityRule"`, `"enrollmentStart"`, `"waitingDays"`, `"providerPartyId"`, `"options"`} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("response does not name %s: %s", field, rec.Body.String())
		}
	}
	if !strings.Contains(rec.Body.String(), "rule syntax") {
		t.Errorf("the rule error does not say why: %s", rec.Body.String())
	}
}

// TestCreateBenefitPlanWindowAndOptions: the window ends on or after it
// starts, option codes are unique, and rates are non-negative decimals with at
// most the currency's minor-unit places (two for USD, none for JPY).
func TestCreateBenefitPlanWindowAndOptions(t *testing.T) {
	body := `{"code":"MED-GOLD","planType":"MEDICAL","currency":"USD","eligibilityRule":"service_days >= 30",
		"enrollmentStart":"2026-11-30","enrollmentEnd":"2026-11-01",
		"options":[
			{"code":"EE","coverageLevel":"","employeeRate":"12.5","employerRate":"100.00","rateBasis":"WEEKLY"},
			{"code":"ee","coverageLevel":"Family","employeeRate":"-1","employerRate":"1.005"}]}`
	rec := serve(t, tid(), http.MethodPost, "/v1/hr/benefits/plans", body, dutySetupAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	for _, field := range []string{"enrollmentEnd", "options[0].coverageLevel", "options[0].rateBasis", "options[1].code", "options[1].employeeRate", "options[1].employerRate"} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("response does not name %q: %s", field, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), "options[0].employeeRate") {
		t.Errorf("12.5 USD was refused: %s", rec.Body.String())
	}

	yen := `{"code":"MED-JP","planType":"MEDICAL","currency":"JPY","eligibilityRule":"true",
		"enrollmentStart":"2026-11-01","enrollmentEnd":"2026-11-30",
		"options":[{"code":"EE","coverageLevel":"Employee","employeeRate":"1500.5","employerRate":"9000"}]}`
	rec = serve(t, tid(), http.MethodPost, "/v1/hr/benefits/plans", yen, dutySetupAdmin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "options[0].employeeRate") ||
		strings.Contains(rec.Body.String(), "options[0].employerRate") {
		t.Fatalf("JPY rates: status %d body %s; want only employeeRate refused", rec.Code, rec.Body.String())
	}
}

// TestBenefitEnrollmentAndLifeEventValidation: the life event kind is closed,
// dependents are bounded, ids are UUIDs and dates are YYYY-MM-DD.
func TestBenefitEnrollmentAndLifeEventValidation(t *testing.T) {
	rec := serve(t, tid(), http.MethodPost, "/v1/hr/benefits/life-events",
		`{"workerId":"nope","kind":"WINNING_THE_LOTTERY","eventDate":"yesterday"}`, dutyEssBenefits)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("life event: status = %d, want 400", rec.Code)
	}
	for _, field := range []string{"workerId", "kind", "eventDate"} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("life event response does not name %q: %s", field, rec.Body.String())
		}
	}

	rec = serve(t, tid(), http.MethodPost, "/v1/hr/benefits/enrollments",
		`{"workerId":"`+testkit.TenantID(1)+`","planId":"bad","optionCode":"","effectiveDate":"2026-11-31","lifeEventId":"x","dependentCount":21}`, dutyEssBenefits)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("enrollment: status = %d, want 400", rec.Code)
	}
	for _, field := range []string{"planId", "optionCode", "effectiveDate", "lifeEventId", "dependentCount"} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("enrollment response does not name %q: %s", field, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), `"workerId"`) {
		t.Errorf("a valid workerId was refused: %s", rec.Body.String())
	}

	rec = serve(t, tid(), http.MethodPost, "/v1/hr/benefits/enrollments/"+testkit.TenantID(3)+"/end",
		`{"lastDay":"2026-02-30","reason":"RETIRED"}`, dutyWorkerMaintain)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "lastDay") || !strings.Contains(rec.Body.String(), "reason") {
		t.Fatalf("end: status %d body %s; want 400 naming lastDay and reason", rec.Code, rec.Body.String())
	}
}

// TestSimulateNeedsAsOf: the simulation is as of a stated date.
func TestSimulateNeedsAsOf(t *testing.T) {
	rec := serve(t, tid(), http.MethodGet, "/v1/hr/benefits/plans/"+testkit.TenantID(2)+"/simulate", "", dutySetupAdmin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "asOf") {
		t.Fatalf("status %d body %s; want 400 naming asOf", rec.Code, rec.Body.String())
	}
}

// TestBenefitIDsAreNotFoundWithTheirCode: a malformed plan, enrollment or
// worker id is a 404 with the benefit or worker code, never a 500 from the
// database cast.
func TestBenefitIDsAreNotFoundWithTheirCode(t *testing.T) {
	cases := []struct {
		duty, method, target, body, code string
	}{
		{dutyEssBenefits, http.MethodGet, "/v1/hr/benefits/plans/not-a-uuid", "", "HCM-BEN-PLAN-NOT-FOUND"},
		{dutySetupAdmin, http.MethodGet, "/v1/hr/benefits/plans/not-a-uuid/simulate?asOf=2026-01-01", "", "HCM-BEN-PLAN-NOT-FOUND"},
		{dutyWorkerMaintain, http.MethodPost, "/v1/hr/benefits/enrollments/not-a-uuid/end", `{"lastDay":"2026-06-01","reason":"TERMINATION"}`, "HCM-BEN-ENR-NOT-FOUND"},
		{dutyEssBenefits, http.MethodGet, "/v1/hr/workers/not-a-uuid/benefits", "", "HCM-WRK-NOT-FOUND"},
	}
	for _, c := range cases {
		rec := serve(t, tid(), c.method, c.target, c.body, c.duty)
		if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), c.code) {
			t.Errorf("%s %s: status = %d body %s, want 404 %s", c.method, c.target, rec.Code, rec.Body.String(), c.code)
		}
	}
}

func TestBenefitMetricsAreExposed(t *testing.T) {
	rec := httptest.NewRecorder()
	New(nil).Metrics().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, name := range []string{"hr_benefit_plans_created_total", "hr_benefit_simulations_total", "hr_benefit_enrollments_total", "hr_benefit_enrollments_ended_total"} {
		if !strings.Contains(rec.Body.String(), name) {
			t.Errorf("metrics missing %s", name)
		}
	}
}
