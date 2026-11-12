package cloudprofile

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const (
	canonicalTable = "../../../deploy/cloud-profiles/profiles.json"
	valuesDir      = "../../../deploy/cloud-profiles/values/"
)

func mustResolve(t *testing.T, base string, overlays ...string) *Profile {
	t.Helper()
	p, err := Resolve(base, overlays...)
	if err != nil {
		t.Fatalf("Resolve(%s %v): %v", base, overlays, err)
	}
	return p
}

// The embedded copy is generated from deploy/cloud-profiles/profiles.json; a drifted copy would give Go
// services different endpoints than the charts.
func TestEmbeddedTableIsTheCanonicalFile(t *testing.T) {
	want, err := os.ReadFile(canonicalTable)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(want, profilesJSON) {
		t.Fatal("platform/go/cloudprofile/profiles.json differs from deploy/cloud-profiles/profiles.json: run python3 deploy/cloud-profiles/cloudprofile.py sync")
	}
}

// Key parity: a profile that lacks an endpoint another profile has fails here, as it does in Python and TypeScript.
func TestEveryProfileHasEveryEndpointWithASource(t *testing.T) {
	d, err := Table()
	if err != nil {
		t.Fatal(err)
	}
	if len(d.EndpointKeys) == 0 {
		t.Fatal("endpointKeys is empty")
	}
	for _, name := range ProfileNames() {
		eps := d.Profiles[name].Endpoints
		for _, key := range d.EndpointKeys {
			ep, ok := eps[key]
			switch {
			case !ok:
				t.Errorf("%s: endpoint %s is missing", name, key)
			case ep.Value == "":
				t.Errorf("%s: endpoint %s has no value", name, key)
			case !strings.HasPrefix(ep.Source, "https://learn.microsoft.com/"):
				t.Errorf("%s: endpoint %s needs a Microsoft Learn source, got %q", name, key, ep.Source)
			}
		}
		for key := range eps {
			if !slices.Contains(d.EndpointKeys, key) {
				t.Errorf("%s: endpoint %s is not in endpointKeys", name, key)
			}
		}
	}
}

func TestAuthoritiesPerCloud(t *testing.T) {
	for name, want := range map[string]struct{ authority, graph string }{
		"commercial": {"https://login.microsoftonline.com", "https://graph.microsoft.com"},
		// GCC is a Microsoft 365 environment on the worldwide Entra ID: it must not move to .us.
		"gcc":      {"https://login.microsoftonline.com", "https://graph.microsoft.com"},
		"gcc-high": {"https://login.microsoftonline.us", "https://graph.microsoft.us"},
		"dod":      {"https://login.microsoftonline.us", "https://dod-graph.microsoft.us"},
		"china":    {"https://login.partner.microsoftonline.cn", "https://microsoftgraph.chinacloudapi.cn"},
	} {
		p := mustResolve(t, name)
		if p.Endpoint("entraAuthority") != want.authority || p.Endpoint("graphRoot") != want.graph {
			t.Errorf("%s: authority %s graph %s, want %s %s", name, p.Endpoint("entraAuthority"), p.Endpoint("graphRoot"), want.authority, want.graph)
		}
	}
}

func TestFIPSAndRegions(t *testing.T) {
	for name, wantFIPS := range map[string]bool{"commercial": false, "gcc": true, "gcc-high": true, "dod": true, "china": false} {
		if got := mustResolve(t, name).FIPS; got != wantFIPS {
			t.Errorf("%s: fips = %v, want %v", name, got, wantFIPS)
		}
	}
	gcc := mustResolve(t, "gcc")
	if !gcc.AllowsRegion("eastus2") || !gcc.AllowsRegion("centralus") || gcc.AllowsRegion("westeurope") || gcc.AllowsRegion("usgovvirginia") {
		t.Errorf("gcc regions = %v, want US commercial regions only", gcc.AllowedRegions)
	}
	if !mustResolve(t, "commercial").AllowsRegion("anywhere") {
		t.Error("commercial has no region restriction")
	}
}

func TestOverlays(t *testing.T) {
	eu := mustResolve(t, "commercial", "eu-data-boundary")
	if eu.AllowsRegion("eastus2") || !eu.AllowsRegion("westeurope") {
		t.Errorf("commercial+eu-data-boundary regions = %v", eu.AllowedRegions)
	}
	if _, err := Resolve("gcc", "eu-data-boundary"); err == nil {
		t.Error("gcc cannot be combined with eu-data-boundary: its regions are all in the US")
	}
	hipaa := mustResolve(t, "commercial", "hipaa")
	if hipaa.PagerDutyAllowed || slices.Contains(hipaa.LLMProviders, "anthropic") || hipaa.AllowsHost("events.pagerduty.com") {
		t.Errorf("hipaa overlay must remove paging and hosted LLMs without a BAA: %v", hipaa.EgressAllowlist)
	}
	if hipaa.Keycloak.EventRetentionDays < 2190 {
		t.Errorf("hipaa retention = %d days", hipaa.Keycloak.EventRetentionDays)
	}
	if _, err := Resolve("commercial", "nope"); err == nil {
		t.Error("an unknown overlay must be an error")
	}
	if _, err := Resolve("azure-stack"); err == nil {
		t.Error("an unknown profile must be an error, not a fallback to commercial")
	}
}

func TestParseSelector(t *testing.T) {
	base, overlays := ParseSelector(" gcc+hipaa + eu-data-boundary ")
	if base != "gcc" || !slices.Equal(overlays, []string{"hipaa", "eu-data-boundary"}) {
		t.Errorf("got %q %v", base, overlays)
	}
	if base, _ := ParseSelector(""); base != "commercial" {
		t.Errorf("empty selector = %q, want commercial", base)
	}
	if got := mustResolve(t, "gcc", "hipaa").Selector(); got != "gcc+hipaa" {
		t.Errorf("Selector = %q", got)
	}
}

func TestHostMatching(t *testing.T) {
	for _, c := range []struct {
		host, pattern string
		want          bool
	}{
		{"login.microsoftonline.com", "login.microsoftonline.com", true},
		{"kv1.vault.azure.net", "*.vault.azure.net", true},
		{"vault.azure.net", "*.vault.azure.net", false},
		{"evil-vault.azure.net", "*.vault.azure.net", false},
		{"a.b.vault.azure.net", "*.vault.azure.net", true},
		{"bedrock-runtime.us-east-1.amazonaws.com", "bedrock-runtime.*.amazonaws.com", true},
		{"bedrock-runtime.amazonaws.com", "bedrock-runtime.*.amazonaws.com", false},
		{"Login.MicrosoftOnline.com", "login.microsoftonline.com", true},
	} {
		if got := HostMatches(c.host, c.pattern); got != c.want {
			t.Errorf("HostMatches(%q, %q) = %v, want %v", c.host, c.pattern, got, c.want)
		}
	}
	gcc := mustResolve(t, "gcc")
	if !gcc.AllowsURL("https://res.openai.azure.com/openai/deployments/x") || gcc.AllowsURL("https://api.anthropic.com") || gcc.AllowsURL("https://ghcr.io/x") {
		t.Errorf("gcc allowlist = %v", gcc.EgressAllowlist)
	}
}

// The Helm values are generated by the Python tool; the Go allowlist must come out identical so a chart and a
// service agree on what is allowed.
func TestAllowlistMatchesGeneratedHelmValues(t *testing.T) {
	line := regexp.MustCompile(`(?m)^  egressAllowlist: (\[.*\])$`)
	for _, name := range ProfileNames() {
		raw, err := os.ReadFile(valuesDir + name + ".yaml")
		if err != nil {
			t.Fatal(err)
		}
		m := line.FindSubmatch(raw)
		if m == nil {
			t.Fatalf("%s.yaml has no egressAllowlist line", name)
		}
		var want []string
		if err := json.Unmarshal(m[1], &want); err != nil {
			t.Fatal(err)
		}
		if got := mustResolve(t, name).EgressAllowlist; !slices.Equal(got, want) {
			t.Errorf("%s: Go allowlist %v != values file %v", name, got, want)
		}
	}
}

func TestNoAzureHostnameOutsideTheTable(t *testing.T) {
	// This package's own Go code must not spell an Azure cloud hostname; it reads them from the table.
	for _, file := range []string{"cloudprofile.go", "fips.go", "idp.go"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, host := range []string{"microsoftonline", "graph.microsoft", "usgovcloudapi", "chinacloudapi", "vault.azure", "azurecr", "windows.net"} {
			if bytes.Contains(raw, []byte(host)) {
				t.Errorf("%s hard-codes %q; read it from the profile table", file, host)
			}
		}
	}
}

// GCC High, DoD, EU Data Boundary and HIPAA settings (#602 #603 #605 #606): Go resolves what Python and TypeScript do.
func TestCloudEnvironmentSettings(t *testing.T) {
	for _, name := range []string{"gcc-high", "dod"} {
		p := mustResolve(t, name)
		d := p.Deployment
		if d.AzureEnvironment != "usgovernment" || !d.PrivateCluster || !d.CustomerManagedKeys || !d.ManagedHSM || d.PostgresSSLMode != "verify-full" {
			t.Errorf("%s deployment = %+v", name, d)
		}
		if !p.EnforceProviders || !slices.Equal(p.LLMProviders, []string{"azure-openai"}) {
			t.Errorf("%s providers = %v enforce=%v", name, p.LLMProviders, p.EnforceProviders)
		}
		if !p.AllowsHost("res.openai.azure.us") || p.AllowsHost("res.openai.azure.com") || p.AllowsHost("ghcr.io") {
			t.Errorf("%s egress allowlist is wrong: %v", name, p.EgressAllowlist)
		}
	}
	if d := mustResolve(t, "dod").Deployment; !d.IsolatedNodes || slices.Contains(d.IsolatedNodesRegions, "usdodeast") || len(d.IsolatedNodesRegions) != 3 {
		t.Errorf("dod isolation = %+v", d)
	}
	if mustResolve(t, "gcc-high").Deployment.IsolatedNodes {
		t.Error("gcc-high needs no isolated nodes")
	}

	eu := mustResolve(t, "commercial", "eu-data-boundary")
	if !eu.EnforceProviders || !eu.RequireMirror || eu.PagerDutyAllowed || eu.AllowsHost("ghcr.io") || eu.AllowsHost("docker.io") || eu.AllowsHost("api.anthropic.com") {
		t.Errorf("eu overlay: enforce=%v mirror=%v allowlist=%v", eu.EnforceProviders, eu.RequireMirror, eu.EgressAllowlist)
	}
	if eu.Endpoint("entraAuthority") != "https://login.microsoftonline.com" {
		t.Error("the Entra authority stays global for EU tenants")
	}

	h := mustResolve(t, "gcc-high", "hipaa")
	if !h.HasOverlay("hipaa") || h.IdleTimeoutMinutes != 15 || !h.EnforceProviders || h.Keycloak.EventRetentionDays < 2190 {
		t.Errorf("hipaa = idle %d enforce %v retention %d", h.IdleTimeoutMinutes, h.EnforceProviders, h.Keycloak.EventRetentionDays)
	}
	if !slices.Contains(h.BAACoveredServices, "Azure OpenAI Service") || h.AzurePolicyInitiative == "" || !slices.Contains(h.PhiServices, "hr") {
		t.Errorf("hipaa lists = %v %q %v", h.BAACoveredServices, h.AzurePolicyInitiative, h.PhiServices)
	}
	if mustResolve(t, "commercial").IdleTimeoutMinutes != 0 {
		t.Error("no idle timeout without the hipaa overlay")
	}
}

func TestPostgresTLSIsVerifiedUnderTheGovernmentProfiles(t *testing.T) {
	noEnv := func(string) string { return "" }
	for _, name := range []string{"gcc", "gcc-high", "dod"} {
		p := mustResolve(t, name)
		for _, bad := range []string{
			"postgres://u@db.example.invalid/x?sslmode=disable",
			"postgres://u@db.example.invalid/x?sslmode=require",
			"postgres://u@db.example.invalid/x",
			"host=db user=u sslmode=verify-ca",
		} {
			if err := p.CheckPostgresConn(bad, noEnv); err == nil {
				t.Errorf("%s must refuse %q", name, bad)
			}
		}
		for _, ok := range []string{"postgres://u@db.example.invalid/x?sslmode=verify-full", "host=db user=u sslmode='verify-full'"} {
			if err := p.CheckPostgresConn(ok, noEnv); err != nil {
				t.Errorf("%s must accept %q: %v", name, ok, err)
			}
		}
		if err := p.CheckPostgresConn("postgres://u@db.example.invalid/x", func(k string) string {
			if k == "PGSSLMODE" {
				return "verify-full"
			}
			return ""
		}); err != nil {
			t.Errorf("%s: PGSSLMODE=verify-full in the environment is honoured: %v", name, err)
		}
	}
	// The default profile keeps local development working.
	if err := mustResolve(t, "commercial").CheckPostgresConn("postgres://u@localhost/x?sslmode=disable", noEnv); err != nil {
		t.Errorf("commercial must not enforce: %v", err)
	}
}
