package api

import (
	"net/http"
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
