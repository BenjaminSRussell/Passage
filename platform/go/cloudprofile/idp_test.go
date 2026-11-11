package cloudprofile

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

const (
	testEntraTenant = "11111111-2222-3333-4444-555555555555"
	testClientID    = "66666666-7777-8888-9999-000000000000"
	goldenDir       = "../../../deploy/keycloak/idp/"
)

func render(t *testing.T, selector, entra, client, tid string) string {
	t.Helper()
	base, overlays := ParseSelector(selector)
	p := mustResolve(t, base, overlays...)
	idp, err := EntraOIDC(p, entra, client, tid)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.MarshalIndent(idp, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func TestEntraIdPRendersFromTheProfile(t *testing.T) {
	gccHigh := render(t, "gcc-high", testEntraTenant, testClientID, "tenant-1")
	if strings.Contains(gccHigh, "login.microsoftonline.com") || strings.Contains(gccHigh, "graph.microsoft.com") {
		t.Errorf("a gcc-high render must not name the commercial authority or Graph:\n%s", gccHigh)
	}
	if !strings.Contains(gccHigh, `"issuer": "https://login.microsoftonline.us/`+testEntraTenant+`/v2.0"`) ||
		!strings.Contains(gccHigh, `"userInfoUrl": "https://graph.microsoft.us/oidc/userinfo"`) {
		t.Errorf("gcc-high issuer or userinfo wrong:\n%s", gccHigh)
	}
	for _, selector := range []string{"commercial", "gcc"} {
		out := render(t, selector, testEntraTenant, testClientID, "tenant-1")
		for _, authority := range []string{"login.microsoftonline.us", "graph.microsoft.us", "chinacloudapi", "partner.microsoftonline.cn"} {
			if strings.Contains(out, authority) {
				t.Errorf("%s render names %s", selector, authority)
			}
		}
		if !strings.Contains(out, "https://login.microsoftonline.com/"+testEntraTenant+"/oauth2/v2.0/authorize") {
			t.Errorf("%s authorize URL wrong:\n%s", selector, out)
		}
	}
	if out := render(t, "dod", testEntraTenant, testClientID, "tenant-1"); !strings.Contains(out, "dod-graph.microsoft.us/oidc/userinfo") {
		t.Errorf("dod render must use the DoD Graph root:\n%s", out)
	}
}

func TestEntraIdPUsesPKCEAndNoSecretAndTheSecondFactor(t *testing.T) {
	p := mustResolve(t, "commercial")
	idp, err := EntraOIDC(p, testEntraTenant, testClientID, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	cfg := idp.Provider["config"].(map[string]string)
	if cfg["pkceEnabled"] != "true" || cfg["pkceMethod"] != "S256" || cfg["validateSignature"] != "true" {
		t.Errorf("authorization code with PKCE and signature validation required: %v", cfg)
	}
	for k := range cfg {
		if strings.Contains(strings.ToLower(k), "secret") && k != "clientAuthMethod" {
			t.Errorf("rendered IdP must not hold %s", k)
		}
	}
	if idp.Provider["postBrokerLoginFlowAlias"] != PostBrokerFlow || idp.Provider["enabled"] != true {
		t.Errorf("provider = %v", idp.Provider)
	}
}

func TestEntraIdPRefusesMultiTenantAuthoritiesAndStaysDisabledWithPlaceholders(t *testing.T) {
	p := mustResolve(t, "commercial")
	for _, tenant := range []string{"common", "organizations", "consumers", "contoso.onmicrosoft.com", ""} {
		if _, err := EntraOIDC(p, tenant, testClientID, "t"); err == nil {
			t.Errorf("tenant %q must be refused: only a tenant GUID pins the issuer", tenant)
		}
	}
	idp, err := EntraOIDC(p, PlaceholderEntraTenant, PlaceholderClientID, PlaceholderPassageTID)
	if err != nil || idp.Provider["enabled"] != false {
		t.Errorf("a placeholder render must ship disabled: %v %v", idp, err)
	}
}

// The tid stays bound to the realm: the only mapper that writes tenant_id is a hardcoded one with sync mode
// FORCE, and no mapper copies an upstream claim into tenant_id, legal_entity_id, duties or tier.
func TestEntraIdPMappersNeverWriteTokenAttributesFromClaims(t *testing.T) {
	idp, err := EntraOIDC(mustResolve(t, "gcc"), testEntraTenant, testClientID, "tenant-1")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{"tenant_id": true, "legal_entity_id": true, "duties": true, "tier": true}
	var tenantMapped bool
	for _, m := range idp.Mappers {
		cfg := m["config"].(map[string]string)
		attribute := cfg["attribute"] + cfg["user.attribute"]
		switch {
		case m["identityProviderMapper"] == "hardcoded-attribute-idp-mapper" && attribute == "tenant_id":
			tenantMapped = cfg["syncMode"] == "FORCE" && cfg["attribute.value"] == "tenant-1"
		case forbidden[attribute]:
			t.Errorf("mapper %v writes token attribute %s", m["name"], attribute)
		case m["identityProviderMapper"] == "oidc-user-attribute-idp-mapper" && cfg["claim"] == "tid":
			t.Errorf("mapper %v copies the Entra tid claim", m["name"])
		}
	}
	if !tenantMapped {
		t.Error("the realm's tenant must come from a FORCE hardcoded tenant_id mapper")
	}
}

// deploy/keycloak/idp/entra-oidc.<profile>.json are the rendered examples operators copy from. UPDATE_GOLDEN=1
// rewrites them.
func TestRenderedEntraExamplesAreCurrent(t *testing.T) {
	for _, name := range ProfileNames() {
		want := render(t, name, PlaceholderEntraTenant, PlaceholderClientID, PlaceholderPassageTID)
		path := goldenDir + "entra-oidc." + name + ".json"
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (run with UPDATE_GOLDEN=1)", err)
		}
		var a, b any
		_ = json.Unmarshal(got, &a)
		_ = json.Unmarshal([]byte(want), &b)
		if !reflect.DeepEqual(a, b) {
			t.Errorf("%s is stale: run UPDATE_GOLDEN=1 go test ./cloudprofile/", path)
		}
	}
}
