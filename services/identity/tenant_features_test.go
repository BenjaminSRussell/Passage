package identity_test

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

const dutyFeatures = "identity.feature.maintain"

type featureRow struct {
	Key       string     `json:"key"`
	Enabled   bool       `json:"enabled"`
	ChangedBy string     `json:"changedBy"`
	ChangedAt *time.Time `json:"changedAt"`
}

type featuresBody struct {
	Features  []featureRow `json:"features"`
	Templates []struct {
		Key string `json:"key"`
	} `json:"templates"`
}

func (b featuresBody) row(key string) featureRow {
	for _, f := range b.Features {
		if f.Key == key {
			return f
		}
	}
	return featureRow{}
}

func TestTenantFeaturesDefaultToEverythingOn(t *testing.T) {
	f := newAdminFixture(t)
	reader := caller{tenantID: f.tenantA, user: "reader-1"}
	body := decode[featuresBody](t, f.do(reader, http.MethodGet, "/v1/tenant/features", nil), http.StatusOK)
	if len(body.Features) == 0 || len(body.Templates) < 3 {
		t.Fatalf("body = %+v, want the catalog and templates", body)
	}
	for _, row := range body.Features {
		if !row.Enabled || row.ChangedAt != nil {
			t.Errorf("%s = %+v, want on and unchanged for a tenant that never touched it", row.Key, row)
		}
	}
}

func TestTenantFeaturesToggleIsAuditedAndTenantScoped(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.adminOf(f.tenantA, dutyFeatures)

	body := decode[featuresBody](t, f.do(admin, http.MethodPut, "/v1/tenant/features", map[string]any{"features": map[string]bool{"edi": false, "edifact": false}}), http.StatusOK)
	if body.row("edi").Enabled || body.row("edifact").Enabled || !body.row("sales").Enabled {
		t.Fatalf("body after toggle = %+v", body.Features)
	}
	if row := body.row("edi"); row.ChangedBy != "admin-1" || row.ChangedAt == nil {
		t.Fatalf("edi = %+v, want who and when recorded", row)
	}

	t.Run("the change is audited and emits one event", func(t *testing.T) {
		audit, events := f.auditAndEvents(t, f.tenantA)
		if len(audit) != 1 || audit[0] != "tenantfeatures.updated|admin-1" || len(events) != 1 || events[0] != "dev.passage.identity.tenantfeatures.updated.v1" {
			t.Fatalf("audit = %v events = %v", audit, events)
		}
	})

	t.Run("a repeated change writes nothing new", func(t *testing.T) {
		decode[featuresBody](t, f.do(admin, http.MethodPut, "/v1/tenant/features", map[string]any{"features": map[string]bool{"edi": false}}), http.StatusOK)
		if audit, _ := f.auditAndEvents(t, f.tenantA); len(audit) != 1 {
			t.Fatalf("audit = %v, want the no-op to leave it alone", audit)
		}
	})

	t.Run("another tenant is not affected", func(t *testing.T) {
		other := decode[featuresBody](t, f.do(caller{tenantID: f.tenantB, user: "reader-2"}, http.MethodGet, "/v1/tenant/features", nil), http.StatusOK)
		if !other.row("edi").Enabled || !other.row("edifact").Enabled {
			t.Fatalf("tenant B = %+v, want its own defaults", other.Features)
		}
	})

	t.Run("a feature can be turned back on", func(t *testing.T) {
		body := decode[featuresBody](t, f.do(admin, http.MethodPut, "/v1/tenant/features", map[string]any{"features": map[string]bool{"edi": true}}), http.StatusOK)
		if !body.row("edi").Enabled || body.row("edifact").Enabled {
			t.Fatalf("body = %+v", body.Features)
		}
	})
}

func TestTenantFeaturesRefuseBrokenDependencies(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.adminOf(f.tenantA, dutyFeatures)

	rec := f.do(admin, http.MethodPut, "/v1/tenant/features", map[string]any{"features": map[string]bool{"inventory": false}})
	p := wantProblem(t, rec, http.StatusUnprocessableEntity, "IDN-FEATURE-DEPENDENCY")
	for _, name := range []string{"Warehouse operations", "Catch weight", "Process manufacturing"} {
		if !strings.Contains(p.Errors["inventory"], name) {
			t.Errorf("message %q does not name %s", p.Errors["inventory"], name)
		}
	}

	body := decode[featuresBody](t, f.do(admin, http.MethodGet, "/v1/tenant/features", nil), http.StatusOK)
	if !body.row("inventory").Enabled {
		t.Fatal("a refused change must leave the feature on")
	}
	if audit, _ := f.auditAndEvents(t, f.tenantA); len(audit) != 0 {
		t.Fatalf("audit = %v, want none for a refused change", audit)
	}

	decode[featuresBody](t, f.do(admin, http.MethodPut, "/v1/tenant/features", map[string]any{"features": map[string]bool{"edifact": false, "edi": false}}), http.StatusOK)
	p = wantProblem(t, f.do(admin, http.MethodPut, "/v1/tenant/features", map[string]any{"features": map[string]bool{"edifact": true}}), http.StatusUnprocessableEntity, "IDN-FEATURE-DEPENDENCY")
	if !strings.Contains(p.Errors["edifact"], "Turn on Electronic data interchange first") {
		t.Fatalf("errors = %v", p.Errors)
	}
}

func TestTenantFeaturesTemplateAndValidation(t *testing.T) {
	f := newAdminFixture(t)
	admin := f.adminOf(f.tenantA, dutyFeatures)

	body := decode[featuresBody](t, f.do(admin, http.MethodPut, "/v1/tenant/features", map[string]any{"template": "distribution"}), http.StatusOK)
	for _, off := range []string{"manufacturing", "process-manufacturing", "payroll", "edi", "edifact"} {
		if body.row(off).Enabled {
			t.Errorf("%s is on after the distribution template", off)
		}
	}
	for _, on := range []string{"sales", "purchasing", "inventory", "batch-tracking"} {
		if !body.row(on).Enabled {
			t.Errorf("%s is off after the distribution template", on)
		}
	}

	for name, req := range map[string]map[string]any{
		"unknown template": {"template": "nope"},
		"unknown feature":  {"features": map[string]bool{"time-travel": true}},
		"both":             {"template": "services", "features": map[string]bool{"sales": true}},
		"neither":          {},
	} {
		t.Run(name, func(t *testing.T) {
			wantProblem(t, f.do(admin, http.MethodPut, "/v1/tenant/features", req), http.StatusBadRequest, "COM-0001")
		})
	}
}

func TestTenantFeaturesWritesNeedATenantAdministrator(t *testing.T) {
	f := newAdminFixture(t)
	change := map[string]any{"features": map[string]bool{"edi": false}}

	cases := map[string]caller{
		"no duty":                  {tenantID: f.tenantA, user: "u", duties: []string{dutyAllScope, dutyTenantAdmin}},
		"another identity duty":    f.adminOf(f.tenantA, dutyLegalEntities),
		"maintain without admin":   {tenantID: f.tenantA, user: "u", duties: []string{dutyFeatures}},
		"scoped to a legal entity": {tenantID: f.tenantA, legalEntity: legalA, user: "u", duties: []string{dutyFeatures, dutyTenantAdmin}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if rec := f.do(c, http.MethodPut, "/v1/tenant/features", change); rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body)
			}
		})
	}

	noActor := f.adminOf(f.tenantA, dutyFeatures)
	noActor.user = ""
	wantProblem(t, f.do(noActor, http.MethodPut, "/v1/tenant/features", change), http.StatusUnauthorized, "AUTH-0001")

	body := decode[featuresBody](t, f.do(caller{tenantID: f.tenantA, user: "reader"}, http.MethodGet, "/v1/tenant/features", nil), http.StatusOK)
	if !body.row("edi").Enabled {
		t.Fatal("a refused write must change nothing")
	}
	if rec := f.do(caller{user: "reader"}, http.MethodGet, "/v1/tenant/features", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a request without a tenant got %d, want 401", rec.Code)
	}
}
