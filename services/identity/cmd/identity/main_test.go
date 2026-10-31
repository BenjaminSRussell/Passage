package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/identity"
)

const (
	operatorID = "90000000-0000-0000-0000-000000000009"
	catalogue  = `{"duties": {"Maintain security roles": {"permissions": [{"resource": "/v1/authz/roles{,/**}", "methods": ["GET"]}]}}}`
)

func env(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestConfigFromRejectsIncompleteOrMalformedSettings(t *testing.T) {
	valid := map[string]string{"DATABASE_URL": "postgres://x", "IDENTITY_DUTY_CATALOGUE_PATH": "/data.json"}
	for name, tc := range map[string]struct {
		set  map[string]string
		want string
	}{
		"no database":      {map[string]string{"IDENTITY_DUTY_CATALOGUE_PATH": "/data.json"}, "DATABASE_URL"},
		"no catalogue":     {map[string]string{"DATABASE_URL": "postgres://x"}, "IDENTITY_DUTY_CATALOGUE_PATH"},
		"bad operator":     {map[string]string{"DATABASE_URL": "postgres://x", "IDENTITY_DUTY_CATALOGUE_PATH": "/d", "IDENTITY_OPERATOR_TENANT_ID": "urn:uuid:" + operatorID}, "IDENTITY_OPERATOR_TENANT_ID"},
		"unreadable store": {valid, "read duty catalogue"},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), env(tc.set))
			if _, cfgErr := configFrom(env(tc.set)); cfgErr != nil {
				err = cfgErr
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}

	t.Run("defaults and canonical operator", func(t *testing.T) {
		cfg, err := configFrom(env(map[string]string{"DATABASE_URL": "postgres://x", "IDENTITY_DUTY_CATALOGUE_PATH": "/d", "IDENTITY_OPERATOR_TENANT_ID": strings.ToUpper(operatorID)}))
		if err != nil || cfg.httpAddr != defaultHTTPAddr || cfg.operatorTenantID != operatorID {
			t.Fatalf("cfg = %+v err = %v", cfg, err)
		}
	})
}

func TestRouterServesHealthAndGuardsAdminRoutes(t *testing.T) {
	cat, err := identity.LoadCatalogue([]byte(catalogue))
	if err != nil {
		t.Fatal(err)
	}
	router := newRouter(nil, cat, operatorID)
	do := func(path string, headers map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	if rec := do("/healthz", nil); rec.Code != http.StatusOK {
		t.Fatalf("healthz = %d", rec.Code)
	}
	if rec := do("/v1/authz/roles", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no tenant = %d, want 401", rec.Code)
	}
	tenantOnly := map[string]string{"X-Passage-Tenant": operatorID, "X-Passage-User": "u"}
	for _, path := range []string{"/v1/authz/roles", "/v1/authz/duties", "/v1/identity/users", "/v1/legal-entities", "/v1/tenants", "/v1/authz/assignments"} {
		if rec := do(path, tenantOnly); rec.Code != http.StatusForbidden {
			t.Errorf("%s without a duty = %d, want 403", path, rec.Code)
		}
	}
	withDuty := map[string]string{"X-Passage-Tenant": operatorID, "X-Passage-User": "u", "X-Passage-Duties": "Maintain security roles"}
	rec := do("/v1/authz/roles", withDuty)
	var body struct {
		Items []struct{ Name string } `json:"items"`
	}
	if err := json.NewDecoder(bytes.NewReader(rec.Body.Bytes())).Decode(&body); err != nil || rec.Code != http.StatusOK || len(body.Items) != 1 || body.Items[0].Name != "Maintain security roles" {
		t.Fatalf("roles = %d %s", rec.Code, rec.Body.String())
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
