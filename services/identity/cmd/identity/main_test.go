package main

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/identity"
	"github.com/BenjaminSRussell/passage/services/identity/migrations"
)

const (
	operatorID = "90000000-0000-0000-0000-000000000009"
	catalogue  = `{"duties": {"Maintain security roles": {"permissions": [{"resource": "/v1/authz/roles{,/**}", "methods": ["GET"]}]}}}`
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestConfigFromRejectsIncompleteOrMalformedSettings(t *testing.T) {
	base := map[string]string{"DATABASE_URL": "postgres://x", "IDENTITY_DUTY_CATALOGUE_PATH": "/data.json"}
	with := func(extra map[string]string) map[string]string {
		out := map[string]string{}
		maps.Copy(out, base)
		maps.Copy(out, extra)
		return out
	}
	for name, tc := range map[string]struct {
		set  map[string]string
		want string
	}{
		"no database":         {map[string]string{"IDENTITY_DUTY_CATALOGUE_PATH": "/data.json"}, "DATABASE_URL"},
		"no catalogue":        {map[string]string{"DATABASE_URL": "postgres://x"}, "IDENTITY_DUTY_CATALOGUE_PATH"},
		"bad operator":        {with(map[string]string{"IDENTITY_OPERATOR_TENANT_ID": "urn:uuid:" + operatorID}), "IDENTITY_OPERATOR_TENANT_ID"},
		"scim without opa":    {with(map[string]string{"IDENTITY_SCIM_ISSUER_BASE": "https://sso.test/realms", "IDENTITY_SCIM_AUDIENCE": "passage"}), "IDENTITY_OPA_URL"},
		"scim without issuer": {with(map[string]string{"IDENTITY_OPA_URL": "http://127.0.0.1:8181"}), "IDENTITY_SCIM_ISSUER_BASE"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := configFrom(env(tc.set)); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	t.Run("defaults and canonical operator", func(t *testing.T) {
		cfg, err := configFrom(env(with(map[string]string{"IDENTITY_OPERATOR_TENANT_ID": strings.ToUpper(operatorID)})))
		if err != nil || cfg.httpAddr != defaultHTTPAddr || cfg.operatorTenantID != operatorID || cfg.scimEnabled() {
			t.Fatalf("cfg = %+v err = %v", cfg, err)
		}
	})

	t.Run("scim settings", func(t *testing.T) {
		cfg, err := configFrom(env(with(map[string]string{"IDENTITY_SCIM_ISSUER_BASE": "https://sso.test/realms", "IDENTITY_SCIM_AUDIENCE": "passage", "IDENTITY_OPA_URL": "http://127.0.0.1:8181"})))
		if err != nil || !cfg.scimEnabled() {
			t.Fatalf("cfg = %+v err = %v", cfg, err)
		}
	})
}

func TestRunFailsOnUnreadableCatalogue(t *testing.T) {
	err := run(context.Background(), env(map[string]string{"DATABASE_URL": "postgres://x", "IDENTITY_DUTY_CATALOGUE_PATH": "/does/not/exist.json"}))
	if err == nil || !strings.Contains(err.Error(), "read duty catalogue") {
		t.Fatalf("err = %v", err)
	}
}

func testRouter(t *testing.T, withSCIM bool) http.Handler {
	t.Helper()
	cat, err := identity.LoadCatalogue([]byte(catalogue))
	if err != nil {
		t.Fatal(err)
	}
	var scim *identity.SCIMAPI
	if withSCIM {
		verifiers, err := identity.NewTenantVerifiers(nil, "https://sso.test/realms", "passage", nil)
		if err != nil {
			t.Fatal(err)
		}
		opa, err := identity.NewOPAClient("http://127.0.0.1:1", nil)
		if err != nil {
			t.Fatal(err)
		}
		scim = identity.NewSCIMAPI(nil, verifiers, opa)
	}
	return newRouter(nil, cat, operatorID, scim)
}

func get(router http.Handler, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestRouterServesHealthAndGuardsAdminRoutes(t *testing.T) {
	router := testRouter(t, false)
	if rec := get(router, "/healthz", nil); rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
	if rec := get(router, "/v1/authz/roles", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no tenant = %d, want 401", rec.Code)
	}
	tenantOnly := map[string]string{"X-Passage-Tenant": operatorID, "X-Passage-User": "u"}
	for _, path := range []string{"/v1/authz/roles", "/v1/authz/duties", "/v1/identity/users", "/v1/legal-entities", "/v1/tenants", "/v1/authz/assignments"} {
		if rec := get(router, path, tenantOnly); rec.Code != http.StatusForbidden {
			t.Errorf("%s without a duty = %d, want 403", path, rec.Code)
		}
	}
	withDuty := map[string]string{"X-Passage-Tenant": operatorID, "X-Passage-User": "u", "X-Passage-Duties": "Maintain security roles"}
	rec := get(router, "/v1/authz/roles", withDuty)
	var body struct {
		Items []struct{ Name string } `json:"items"`
	}
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&body); err != nil || rec.Code != http.StatusOK || len(body.Items) != 1 || body.Items[0].Name != "Maintain security roles" {
		t.Fatalf("roles = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSCIMRoutesAreBearerOnlyAndOptional(t *testing.T) {
	headers := map[string]string{"X-Passage-Tenant": operatorID, "X-Passage-User": "u", "X-Passage-Duties": "Provision users via SCIM"}
	if rec := get(testRouter(t, false), "/scim/v2/Users", headers); rec.Code != http.StatusNotFound {
		t.Fatalf("scim unconfigured = %d, want 404", rec.Code)
	}
	router := testRouter(t, true)
	rec := get(router, "/scim/v2/Users", headers)
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("Content-Type") != "application/scim+json" {
		t.Fatalf("gateway headers must not authenticate SCIM: %d %s %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
	}
	if rec := get(router, "/healthz", nil); rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
}

func TestOperatorTenantMustMatchTheRegisteredRow(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := context.Background()
	if err := dbx.Migrate(ctx, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	other := testkit.TenantID(7)
	for _, id := range []string{operatorID, other} {
		if _, err := admin.Exec(ctx, `insert into identity_tenants (tenant_id, realm_name, display_name) values ($1, $2, 'T')`, id, "realm-"+id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(ctx, `insert into identity_operator (tenant_id) values ($1)`, operatorID); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{operatorID: true, other: false} {
		if got, err := operatorRegistered(ctx, app, id); err != nil || got != want {
			t.Errorf("operatorRegistered(%s) = %v, %v; want %v", id, got, err, want)
		}
	}
}

func TestLoggerNamesTheService(t *testing.T) {
	var out bytes.Buffer
	newLogger(&out).Info("started")
	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil || line["service"] != "identity" || line["message"] != "started" || line["timestamp"] == nil {
		t.Fatalf("log line = %s err = %v", out.String(), err)
	}
}
