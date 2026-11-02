package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// rollbackBoth approves a rollback as two different CutoverManagers (CUT-1603): the first is
// answered 202 pending, the second's answer executes it and is returned. A non-empty key makes the
// two requests idempotent under key-1 and key-2.
func rollbackBoth(t *testing.T, h http.Handler, path, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	k1, k2 := "", ""
	if key != "" {
		k1, k2 = key+"-1", key+"-2"
	}
	first := do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: "CutoverManager", body: body, key: k1, user: "alice"})
	if first.Code != http.StatusAccepted {
		t.Fatalf("first rollback approval: %d %s", first.Code, first.Body)
	}
	return do(t, h, request{method: "POST", path: path, tenant: tenantA, duties: "CutoverManager", body: body, key: k2, user: "bob"})
}

// CUT-1605: approvals are recorded under a person. A call made with a service token (the service's
// duties, the user only as its actor claim) or by a background service principal approves nothing.
func TestApproverID_RefusesServiceCallers(t *testing.T) {
	cases := []struct {
		name string
		p    tenant.Principal
		want string
	}{
		{"signed-in user", tenant.Principal{UserID: "alice"}, "alice"},
		{"service token acting for a user", tenant.Principal{UserID: "alice", Service: "workflow"}, ""},
		{"background service principal", tenant.Principal{UserID: tenant.ServiceActor("migration-loader")}, ""},
	}
	for _, c := range cases {
		if got := approverID(c.p); got != c.want {
			t.Errorf("%s: approverID = %q, want %q", c.name, got, c.want)
		}
	}
}
