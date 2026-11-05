package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

func TestSelfServiceRoutesRequireTheirDuty(t *testing.T) {
	for _, c := range []struct{ target, duty string }{
		{"/v1/hr/me", dutyMssBase},
		{"/v1/hr/me/team", dutyEssBase},
		{"/v1/hr/me/team/" + testkit.TenantID(9), dutyEssBase},
	} {
		if rec := serve(t, tid(), http.MethodGet, c.target, "", c.duty, dutyWorkerInquire, dutyPIIRead); rec.Code != http.StatusForbidden {
			t.Errorf("%s with %s only: status = %d, want 403", c.target, c.duty, rec.Code)
		}
	}
}

// TestProfileChangeValidationBeforeAnyLookup: a bad body is 400 with the field
// named, before the store resolves the caller's worker.
func TestProfileChangeValidationBeforeAnyLookup(t *testing.T) {
	for _, c := range []struct{ body, field string }{
		{`{"kind":"SHOE_SIZE"}`, "kind"},
		{`{"kind":"PREFERRED_NAME"}`, "preferredName"},
		{`{"kind":"BANK_ACCOUNT","bankAccount":"12-34"}`, "bankAccount"},
		{`{"kind":"HOME_ADDRESS"}`, "address"},
		{`{"kind":"HOME_ADDRESS","address":{"line1":"1 Main","city":"X","postalCode":"1","country":"USA"}}`, "address.country"},
	} {
		rec := serve(t, tid(), http.MethodPost, "/v1/hr/me/profile-changes", c.body, dutyEssBase)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"`+c.field+`"`) {
			t.Errorf("%s: status %d body %s, want 400 naming %s", c.body, rec.Code, rec.Body.String(), c.field)
		}
	}
	for _, target := range []string{"/v1/hr/profile-changes/" + testkit.TenantID(9) + "/decide", "/v1/hr/me/team/profile-changes/" + testkit.TenantID(9) + "/decide"} {
		duty := dutyWorkerMaintain
		if strings.Contains(target, "/me/") {
			duty = dutyMssApprove
		}
		if rec := serve(t, tid(), http.MethodPost, target, `{"decision":"MAYBE"}`, duty); rec.Code != http.StatusBadRequest {
			t.Errorf("%s bad decision: status %d", target, rec.Code)
		}
	}
	if rec := serve(t, tid(), http.MethodGet, "/v1/hr/profile-changes?status=DONE", "", dutyWorkerMaintain); rec.Code != http.StatusBadRequest {
		t.Errorf("bad status filter: %d", rec.Code)
	}
}
