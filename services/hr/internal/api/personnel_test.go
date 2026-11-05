package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/hr/internal/store"
)

func TestActionsThatTouchPayNeedCompAdmin(t *testing.T) {
	worker, employment, plan := testkit.TenantID(7), testkit.TenantID(8), testkit.TenantID(9)
	bodies := map[string]string{
		"comp change":              `{"actionType":"COMP_CHANGE","workerId":"` + worker + `","employmentId":"` + employment + `","effectiveDate":"2026-01-01","details":{"compPlanId":"` + plan + `","amount":"1.00","currency":"USD"}}`,
		"hire with pay":            `{"actionType":"HIRE","workerId":"` + worker + `","effectiveDate":"2026-01-01","details":{"legalEntityId":"` + plan + `","employmentType":"FULL_TIME","compPlanId":"` + plan + `","amount":"1.00","currency":"USD"}}`,
		"hire with amount only":    `{"actionType":"HIRE","workerId":"` + worker + `","effectiveDate":"2026-01-01","details":{"amount":"1.00"}}`,
		"transfer carrying a plan": `{"actionType":"TRANSFER","workerId":"` + worker + `","employmentId":"` + employment + `","effectiveDate":"2026-01-01","details":{"positionId":"` + plan + `","compPlanId":"` + plan + `"}}`,
	}
	for name, body := range bodies {
		rec := serve(t, tid(), http.MethodPost, "/v1/hr/actions", body, dutyActionMaintain)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "AUTH-0003") {
			t.Errorf("%s with only hr.action.maintain: status %d body %s, want 403", name, rec.Code, rec.Body.String())
		}
	}
	offerHire := `{"workerId":"` + worker + `","legalEntityId":"` + plan + `","employmentType":"FULL_TIME","compPlanId":"` + plan + `"}`
	rec := serve(t, tid(), http.MethodPost, "/v1/hr/offers/"+employment+"/hire", offerHire, dutyActionMaintain)
	if rec.Code != http.StatusForbidden {
		t.Errorf("hire with a comp plan and only hr.action.maintain: status %d, want 403", rec.Code)
	}
}

func TestActionValidation(t *testing.T) {
	body := `{"actionType":"TERMINATE","workerId":"bad","effectiveDate":"tomorrow","details":{"terminationReason":"!"}}`
	rec := serve(t, tid(), http.MethodPost, "/v1/hr/actions", body, dutyActionMaintain)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	for _, field := range []string{"workerId", "effectiveDate", "employmentId", "details.terminationReason"} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("response does not name %q: %s", field, rec.Body.String())
		}
	}
}

func TestPersonnelRoutesRequireTheirDuty(t *testing.T) {
	id := testkit.TenantID(9)
	cases := []struct{ method, target, duty string }{
		{http.MethodPost, "/v1/hr/jobs", dutyCompAdmin},
		{http.MethodGet, "/v1/hr/requisitions", dutyRecruitMaintain},
		{http.MethodGet, "/v1/hr/compensation/cycles/" + id, dutyCompAdmin},
		{http.MethodPost, "/v1/hr/candidates/purge", dutySetupAdmin},
	}
	for _, c := range cases {
		if rec := serve(t, tid(), c.method, c.target, `{}`, dutyWorkerInquire); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s without %s: status %d, want 403", c.method, c.target, c.duty, rec.Code)
		}
	}
}

func TestRetentionPurgeNeedsTheHoldCheck(t *testing.T) {
	rec := serve(t, tid(), http.MethodPost, "/v1/hr/candidates/purge", "", dutySetupAdmin)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "HCM-RET-HOLDS") {
		t.Fatalf("status %d body %s, want 503 HCM-RET-HOLDS", rec.Code, rec.Body.String())
	}
}

func TestRetentionPurgeKillSwitch(t *testing.T) {
	p, _ := tenant.FromContext(testkit.Ctx(tid(), dutySetupAdmin))
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), p)))
		})
	})
	New(nil).EnableRetentionPurge(false).Register(r)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/hr/candidates/purge", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "HCM-RET-DISABLED") {
		t.Fatalf("status %d body %s, want 503 HCM-RET-DISABLED", rec.Code, rec.Body.String())
	}
}

func TestWithoutPayHidesPlanAmountAndCurrency(t *testing.T) {
	act := store.PersonnelAction{Payload: store.ActionPayload{CompPlanID: "p", Amount: "1.00", Currency: "USD", PositionID: "x"}}.WithoutPay()
	if act.Payload.CompPlanID != "" || act.Payload.Amount != "" || act.Payload.Currency != "" || act.Payload.PositionID != "x" {
		t.Fatalf("payload = %+v", act.Payload)
	}
}
