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
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/identity"
	"github.com/BenjaminSRussell/passage/services/identity/migrations"
)

const (
	operatorID = "90000000-0000-0000-0000-000000000009"
	catalogue  = `{"duties": {"identity.security-role.maintain": {"permissions": [{"resource": "/v1/authz/roles{,/**}", "methods": ["GET"]}]}}}`
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
		"no database":               {map[string]string{"IDENTITY_DUTY_CATALOGUE_PATH": "/data.json"}, "DATABASE_URL"},
		"no catalogue":              {map[string]string{"DATABASE_URL": "postgres://x"}, "IDENTITY_DUTY_CATALOGUE_PATH"},
		"bad operator":              {with(map[string]string{"IDENTITY_OPERATOR_TENANT_ID": "urn:uuid:" + operatorID}), "IDENTITY_OPERATOR_TENANT_ID"},
		"scim without opa":          {with(map[string]string{"IDENTITY_SCIM_ISSUER_BASE": "https://sso.test/realms", "IDENTITY_SCIM_AUDIENCE": "passage"}), "IDENTITY_OPA_URL"},
		"bundle without key":        {with(map[string]string{"IDENTITY_BUNDLE_TOKEN": "t", "IDENTITY_OPERATOR_TENANT_ID": operatorID}), "IDENTITY_BUNDLE_SIGNING_KEY_PATH"},
		"bundle on the public port": {with(map[string]string{"IDENTITY_BUNDLE_TOKEN": "t", "IDENTITY_OPERATOR_TENANT_ID": operatorID, "IDENTITY_BUNDLE_SIGNING_KEY_PATH": "/k", "IDENTITY_BUNDLE_KEY_ID": "k", "IDENTITY_BUNDLE_TLS_CERT_PATH": "/c", "IDENTITY_BUNDLE_TLS_KEY_PATH": "/k", "IDENTITY_BUNDLE_ADDR": ":8080"}), "differ from HTTP_ADDR"},
		"bundle without operator":   {with(map[string]string{"IDENTITY_BUNDLE_TOKEN": "t", "IDENTITY_BUNDLE_SIGNING_KEY_PATH": "/k.pem", "IDENTITY_BUNDLE_KEY_ID": "k"}), "IDENTITY_OPERATOR_TENANT_ID"},
		"bundle key without token":  {with(map[string]string{"IDENTITY_BUNDLE_KEY_ID": "k"}), "IDENTITY_BUNDLE_TOKEN"},
		"scim without issuer":       {with(map[string]string{"IDENTITY_OPA_URL": "http://127.0.0.1:8181"}), "IDENTITY_SCIM_ISSUER_BASE"},
		"keycloak without secrets":  {with(map[string]string{"IDENTITY_KEYCLOAK_ADMIN_URL": "https://kc.test", "IDENTITY_OPERATOR_TENANT_ID": operatorID}), "IDENTITY_KEYCLOAK_SECRETS_DIR"},
		"keycloak without operator": {with(map[string]string{"IDENTITY_KEYCLOAK_ADMIN_URL": "https://kc.test", "IDENTITY_KEYCLOAK_SECRETS_DIR": "/s"}), "IDENTITY_OPERATOR_TENANT_ID"},
		"keycloak settings, no URL": {with(map[string]string{"IDENTITY_KEYCLOAK_SECRETS_DIR": "/s"}), "IDENTITY_KEYCLOAK_ADMIN_URL"},
		"keycloak interval too short": {with(map[string]string{"IDENTITY_KEYCLOAK_ADMIN_URL": "https://kc.test", "IDENTITY_KEYCLOAK_SECRETS_DIR": "/s", "IDENTITY_OPERATOR_TENANT_ID": operatorID,
			"IDENTITY_KEYCLOAK_DEPROVISION_INTERVAL": "1s"}), "IDENTITY_KEYCLOAK_DEPROVISION_INTERVAL"},
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

	t.Run("bundle rego path defaults beside the catalogue and can be overridden", func(t *testing.T) {
		bundle := map[string]string{"IDENTITY_BUNDLE_TOKEN": "t", "IDENTITY_OPERATOR_TENANT_ID": operatorID, "IDENTITY_BUNDLE_SIGNING_KEY_PATH": "/k", "IDENTITY_BUNDLE_KEY_ID": "k", "IDENTITY_BUNDLE_TLS_CERT_PATH": "/c", "IDENTITY_BUNDLE_TLS_KEY_PATH": "/k", "IDENTITY_BUNDLE_ADDR": ":8443"}
		cfg, err := configFrom(env(with(bundle)))
		if err != nil || cfg.bundleRegoPath != "/authz.rego" {
			t.Fatalf("default = %q err = %v", cfg.bundleRegoPath, err)
		}
		bundle["IDENTITY_BUNDLE_REGO_PATH"] = "/policy/custom.rego"
		cfg, err = configFrom(env(with(bundle)))
		if err != nil || cfg.bundleRegoPath != "/policy/custom.rego" {
			t.Fatalf("override = %q err = %v", cfg.bundleRegoPath, err)
		}
	})

	t.Run("keycloak deprovisioning settings", func(t *testing.T) {
		kc := map[string]string{"IDENTITY_KEYCLOAK_ADMIN_URL": "https://kc.test", "IDENTITY_KEYCLOAK_SECRETS_DIR": "/var/run/passage/keycloak", "IDENTITY_OPERATOR_TENANT_ID": operatorID}
		cfg, err := configFrom(env(with(kc)))
		if err != nil || !cfg.keycloakDeprovisionEnabled() || cfg.keycloakClientID != defaultKeycloakClientID || cfg.deprovisionInterval != identity.DefaultDeprovisionInterval {
			t.Fatalf("defaults: cfg = %+v err = %v", cfg, err)
		}
		admin, err := newKeycloakAdmin(cfg)
		if err != nil || admin == nil {
			t.Fatalf("admin = %v, %v", admin, err)
		}
		if worker, err := newDeprovisioner(nil, cfg, admin); err != nil || worker == nil {
			t.Fatalf("worker = %v, %v", worker, err)
		}
		kc["IDENTITY_KEYCLOAK_CLIENT_ID"], kc["IDENTITY_KEYCLOAK_DEPROVISION_INTERVAL"] = "custom", "2m"
		if cfg, err = configFrom(env(with(kc))); err != nil || cfg.keycloakClientID != "custom" || cfg.deprovisionInterval != 2*time.Minute {
			t.Fatalf("overrides: cfg = %+v err = %v", cfg, err)
		}
		kc["IDENTITY_KEYCLOAK_ADMIN_URL"] = "http://keycloak.hello-ledger.svc:8080"
		cfg, _ = configFrom(env(with(kc)))
		if _, err := newKeycloakAdmin(cfg); err == nil {
			t.Fatal("a plain-http Keycloak admin URL off loopback was accepted")
		}
		if cfg, err := configFrom(env(base)); err != nil || cfg.keycloakDeprovisionEnabled() {
			t.Fatalf("off by default: %+v %v", cfg, err)
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
		verifiers, err := identity.NewTenantVerifiers(nil, "https://sso.test/realms", "passage", nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		opa, err := identity.NewOPAClient("http://127.0.0.1:1", nil)
		if err != nil {
			t.Fatal(err)
		}
		scim = identity.NewSCIMAPI(nil, verifiers, opa)
	}
	return newRouter(nil, cat, operatorID, scim, identity.NewCustomerContactsAPI(nil, nil, identity.Invitation{}))
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
	for _, path := range []string{"/v1/authz/roles", "/v1/authz/duties", "/v1/identity/users", "/v1/legal-entities", "/v1/tenants", "/v1/authz/assignments",
		"/v1/identity/users/" + operatorID + "/keycloak-deprovisions"} {
		if rec := get(router, path, tenantOnly); rec.Code != http.StatusForbidden {
			t.Errorf("%s without a duty = %d, want 403", path, rec.Code)
		}
	}
	withDuty := map[string]string{"X-Passage-Tenant": operatorID, "X-Passage-User": "u", "X-Passage-Duties": "identity.security-role.maintain"}
	rec := get(router, "/v1/authz/roles", withDuty)
	var body struct {
		Items []struct{ Name string } `json:"items"`
	}
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&body); err != nil || rec.Code != http.StatusOK || len(body.Items) != 1 || body.Items[0].Name != "identity.security-role.maintain" {
		t.Fatalf("roles = %d %s", rec.Code, rec.Body.String())
	}
}

func TestSCIMRoutesAreBearerOnlyAndOptional(t *testing.T) {
	headers := map[string]string{"X-Passage-Tenant": operatorID, "X-Passage-User": "u", "X-Passage-Duties": "identity.user.provision"}
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
