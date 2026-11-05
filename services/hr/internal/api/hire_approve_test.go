package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func TestCycleApprovalNeedsItsOwnDuty(t *testing.T) {
	target := "/v1/hr/compensation/cycles/" + testkit.TenantID(9) + "/approve"
	for _, duties := range [][]string{{dutyCompAdmin}, {dutyMssApprove}, {dutyCompAdmin, dutyMssApprove, dutyActionMaintain}} {
		if rec := serve(t, tid(), http.MethodPost, target, "", duties...); rec.Code != http.StatusForbidden {
			t.Errorf("approve with %v: status %d, want 403", duties, rec.Code)
		}
	}
	if rec := serve(t, tid(), http.MethodPost, "/v1/hr/compensation/cycles/not-a-uuid/approve", "", dutyCompApprove); rec.Code != http.StatusNotFound {
		t.Errorf("approve with a bad id: status %d, want 404", rec.Code)
	}
	if rec := serve(t, tid(), http.MethodPost, "/v1/hr/compensation/cycles/"+testkit.TenantID(9)+"/apply", "", dutyCompApprove); rec.Code != http.StatusForbidden {
		t.Errorf("apply with only hr.compensation.approve: status %d, want 403", rec.Code)
	}
}

func TestHireFromOfferNewWorker(t *testing.T) {
	offer, le, plan := testkit.TenantID(7), testkit.TenantID(8), testkit.TenantID(9)
	target := "/v1/hr/offers/" + offer + "/hire"
	newWorker := `"newWorker":{"personnelNumber":"WRK-9","workerType":"EMPLOYEE","partyPersonId":"` + plan + `","nationalId":"123"}`
	body := `{` + newWorker + `,"legalEntityId":"` + le + `","employmentType":"FULL_TIME","compPlanId":"` + plan + `"}`

	rec := serve(t, tid(), http.MethodPost, target, body, dutyActionMaintain, dutyCompAdmin)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), dutyWorkerMaintain) {
		t.Fatalf("newWorker without hr.worker.maintain: status %d body %s, want 403", rec.Code, rec.Body.String())
	}

	both := `{"workerId":"` + plan + `",` + newWorker + `,"legalEntityId":"` + le + `","employmentType":"FULL_TIME"}`
	rec = serve(t, tid(), http.MethodPost, target, both, dutyActionMaintain, dutyWorkerMaintain)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not both") {
		t.Fatalf("workerId and newWorker: status %d body %s, want 400", rec.Code, rec.Body.String())
	}

	bad := `{"newWorker":{"personnelNumber":"!","workerType":"X","partyPersonId":"no","nationalId":""},"legalEntityId":"` + le + `","employmentType":"FULL_TIME"}`
	rec = serve(t, tid(), http.MethodPost, target, bad, dutyActionMaintain, dutyWorkerMaintain)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad newWorker: status %d, want 400", rec.Code)
	}
	for _, field := range []string{"newWorker.personnelNumber", "newWorker.workerType", "newWorker.partyPersonId", "newWorker.nationalId"} {
		if !strings.Contains(rec.Body.String(), field) {
			t.Errorf("response does not name %q: %s", field, rec.Body.String())
		}
	}
	if strings.Contains(rec.Body.String(), "newWorker.legalName") {
		t.Errorf("legalName is optional on hire (the candidate's name): %s", rec.Body.String())
	}

	neither := `{"legalEntityId":"` + le + `","employmentType":"FULL_TIME"}`
	rec = serve(t, tid(), http.MethodPost, target, neither, dutyActionMaintain)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "workerId") {
		t.Fatalf("neither workerId nor newWorker: status %d body %s, want 400", rec.Code, rec.Body.String())
	}
}
