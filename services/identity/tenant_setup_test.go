package identity_test

import (
	"encoding/json"
	"net/http"
	"testing"
)

const dutySetup = "identity.setup.maintain"

type setupBody struct {
	Steps []struct {
		Key       string `json:"key"`
		Status    string `json:"status"`
		ChangedBy string `json:"changedBy"`
	} `json:"steps"`
	Total    int  `json:"total"`
	Finished int  `json:"finished"`
	Complete bool `json:"complete"`
}

func (b setupBody) status(key string) string {
	for _, s := range b.Steps {
		if s.Key == key {
			return s.Status
		}
	}
	return ""
}

func TestTenantSetupStartsEmptyAndTracksSteps(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.adminOf(f.tenantA, dutySetup)

	body := decode[setupBody](t, f.do(caller{tenantID: f.tenantA, user: "reader"}, http.MethodGet, "/v1/tenant/setup", nil), http.StatusOK)
	if body.Total < 8 || body.Finished != 0 || body.Complete || body.status("company") != "todo" {
		t.Fatalf("new tenant = %+v, want every step to do", body)
	}

	body = decode[setupBody](t, f.do(admin, http.MethodPut, "/v1/tenant/setup/steps/company", map[string]string{"status": "done"}), http.StatusOK)
	if body.status("company") != "done" || body.Finished != 1 || body.Steps[1].ChangedBy != "admin-1" {
		t.Fatalf("after done = %+v", body)
	}
	body = decode[setupBody](t, f.do(admin, http.MethodPut, "/v1/tenant/setup/steps/tax", map[string]string{"status": "skipped"}), http.StatusOK)
	if body.status("tax") != "skipped" || body.Finished != 2 {
		t.Fatalf("after skip = %+v", body)
	}
	body = decode[setupBody](t, f.do(admin, http.MethodPut, "/v1/tenant/setup/steps/company", map[string]string{"status": "todo"}), http.StatusOK)
	if body.status("company") != "todo" || body.Finished != 1 {
		t.Fatalf("after undo = %+v", body)
	}

	other := decode[setupBody](t, f.do(caller{tenantID: f.tenantB, user: "reader"}, http.MethodGet, "/v1/tenant/setup", nil), http.StatusOK)
	if other.Finished != 0 {
		t.Fatalf("another tenant sees %d finished steps, want 0", other.Finished)
	}

	for _, step := range body.Steps {
		decode[setupBody](t, f.do(admin, http.MethodPut, "/v1/tenant/setup/steps/"+step.Key, map[string]string{"status": "done"}), http.StatusOK)
	}
	if done := decode[setupBody](t, f.do(admin, http.MethodGet, "/v1/tenant/setup", nil), http.StatusOK); !done.Complete {
		t.Fatalf("every step finished but complete = false: %+v", done)
	}

	audit, events := f.auditAndEvents(t, f.tenantA)
	if len(audit) == 0 || audit[0][:len("tenantsetup.updated")] != "tenantsetup.updated" || len(events) != len(audit) {
		t.Fatalf("audit = %v events = %v", audit, events)
	}
}

func TestTenantSetupValidationAndAccess(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.adminOf(f.tenantA, dutySetup)

	wantProblem(t, f.do(admin, http.MethodPut, "/v1/tenant/setup/steps/company", map[string]string{"status": "maybe"}), http.StatusBadRequest, "COM-0001")
	wantProblem(t, f.do(admin, http.MethodPut, "/v1/tenant/setup/steps/time-travel", map[string]string{"status": "done"}), http.StatusNotFound, "COM-0404")

	for name, c := range map[string]caller{
		"no duty":                {tenantID: f.tenantA, user: "u", duties: []string{dutyAllScope, dutyTenantAdmin}},
		"maintain without admin": {tenantID: f.tenantA, user: "u", duties: []string{dutySetup}},
		"scoped to an entity":    {tenantID: f.tenantA, legalEntity: legalA, user: "u", duties: []string{dutySetup, dutyTenantAdmin}},
	} {
		t.Run(name, func(t *testing.T) {
			if rec := f.do(c, http.MethodPut, "/v1/tenant/setup/steps/company", map[string]string{"status": "done"}); rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
			}
		})
	}
	if body := decode[setupBody](t, f.do(admin, http.MethodGet, "/v1/tenant/setup", nil), http.StatusOK); body.Finished != 0 {
		t.Fatalf("a refused write changed %d steps", body.Finished)
	}
}

func TestTenantWorkspaceConfigRoundTrip(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.adminOf(f.tenantA, dutySetup)
	reader := caller{tenantID: f.tenantA, user: "reader"}

	type cfgBody struct {
		Config json.RawMessage `json:"config"`
	}
	if got := decode[cfgBody](t, f.do(reader, http.MethodGet, "/v1/tenant/workspace-config", nil), http.StatusOK); string(got.Config) != "null" {
		t.Fatalf("untouched tenant config = %s, want null", got.Config)
	}

	override := map[string]any{"config": map[string]any{"hide": []string{"controller"}}}
	decode[cfgBody](t, f.do(admin, http.MethodPut, "/v1/tenant/workspace-config", override), http.StatusOK)
	got := decode[map[string]map[string][]string](t, f.do(reader, http.MethodGet, "/v1/tenant/workspace-config", nil), http.StatusOK)
	if len(got["config"]["hide"]) != 1 || got["config"]["hide"][0] != "controller" {
		t.Fatalf("config = %v", got)
	}
	if other := decode[cfgBody](t, f.do(caller{tenantID: f.tenantB, user: "reader"}, http.MethodGet, "/v1/tenant/workspace-config", nil), http.StatusOK); string(other.Config) != "null" {
		t.Fatalf("another tenant sees %s", other.Config)
	}

	wantProblem(t, f.do(admin, http.MethodPut, "/v1/tenant/workspace-config", map[string]any{"config": map[string]any{"theme": "dark"}}), http.StatusBadRequest, "COM-0001")
	wantProblem(t, f.do(admin, http.MethodPut, "/v1/tenant/workspace-config", map[string]any{"config": []string{"x"}}), http.StatusBadRequest, "COM-0001")
	if rec := f.do(f.adminOf(f.tenantA), http.MethodPut, "/v1/tenant/workspace-config", override); rec.Code != http.StatusForbidden {
		t.Fatalf("without the duty got %d, want 403", rec.Code)
	}

	decode[cfgBody](t, f.do(admin, http.MethodPut, "/v1/tenant/workspace-config", map[string]any{"config": nil}), http.StatusOK)
	if got := decode[cfgBody](t, f.do(reader, http.MethodGet, "/v1/tenant/workspace-config", nil), http.StatusOK); string(got.Config) != "null" {
		t.Fatalf("after reset config = %s, want null", got.Config)
	}
}
