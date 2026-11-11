package identity

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/tenant"
)

const (
	parityResources = 100
	parityTenant    = contractTenant
)

func loadSeededCatalogue(t *testing.T) *Catalogue {
	t.Helper()
	raw, err := os.ReadFile("../../policies/authz/data.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadCatalogue(raw)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func sampleRoutes(routes []Route, n int) []Route {
	sampled := make([]Route, n)
	for i := range sampled {
		sampled[i] = routes[i*len(routes)/n]
	}
	return sampled
}

func everyNthDuty(c *Catalogue, n int) []string {
	var duties []string
	for i, name := range c.names {
		if i%n == 0 {
			duties = append(duties, name)
		}
	}
	return duties
}

func parityPrincipals(c *Catalogue) map[string][]string {
	return map[string][]string{
		"no-duties":       nil,
		"viewer":          {"party.vendor.view"},
		"viewer-and-edit": {"party.vendor.view", "party.vendor.maintain", "party.vendor.view"},
		"finance":         {"ledger.journal.read", "reporting.statement.view", "ledger.journal.post", "inventory.read"},
		"unknown-only":    {"not-a-seeded-duty"},
		"flags-only":      {"ledger.journal.postdirect", "budget.override", "pii.read"},
		"identity-admin":  {"identity.user.maintain", "identity.effective-permission.view", "identity.security-role.maintain"},
		"broad":           everyNthDuty(c, 3),
	}
}

func TestEffectivePermissionsMatchOPAOver100Resources(t *testing.T) {
	base := startRealPolicy(t)
	catalogue := loadSeededCatalogue(t)
	client, err := NewOPAClient(base, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	principals := parityPrincipals(catalogue)
	putAssignments(t, base, principals)

	routes := catalogue.Routes()
	if len(routes) < parityResources {
		t.Fatalf("catalogue has %d routes, want at least %d", len(routes), parityResources)
	}
	sampled := sampleRoutes(routes, parityResources)
	services := map[string]bool{}
	for _, route := range sampled {
		services[strings.Join(strings.SplitN(route.Resource, "/", 4)[:3], "/")] = true
	}
	if len(services) < 10 {
		t.Fatalf("sample spans %d services, want at least 10", len(services))
	}

	names := make([]string, 0, len(principals))
	for name := range principals {
		names = append(names, name)
	}
	sort.Strings(names)
	allowed := 0
	for _, name := range names {
		for _, route := range sampled {
			resource := SampleResource(route.Resource)
			want, err := client.Decide(context.Background(), tenant.Principal{TenantID: parityTenant, UserID: name}, route.Method, resource)
			if err != nil {
				t.Fatal(err)
			}
			got := catalogue.Evaluate(catalogue.HeldIn(principals[name], false), route.Method, resource)
			if got.Allow != want.Allow || got.DutyMissing != want.DutyMissing {
				t.Errorf("%s %s %s: engine allow=%v missing=%q, OPA allow=%v missing=%q", name, route.Method, resource, got.Allow, got.DutyMissing, want.Allow, want.DutyMissing)
			}
			if got.Allow {
				allowed++
			}
		}
	}
	if allowed == 0 || allowed == len(names)*len(sampled) {
		t.Fatalf("allowed %d of %d decisions; the fixtures exercise only one outcome", allowed, len(names)*len(sampled))
	}
}

func TestEffectivePermissionsMatchOPAOnEdgePaths(t *testing.T) {
	base := startRealPolicy(t)
	catalogue := loadSeededCatalogue(t)
	client, err := NewOPAClient(base, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	principals := parityPrincipals(catalogue)
	putAssignments(t, base, principals)

	var resources []string
	for i, route := range catalogue.Routes() {
		if i%7 != 0 {
			continue
		}
		sample := SampleResource(route.Resource)
		resources = append(resources, sample+"/a:b", sample+"/", sample+"/x/y/z/w", strings.Replace(sample, "item-1", "a:b", 1))
	}
	resources = append(resources, "/v1/unknown-prefix/item-1", "/v1/unknown-prefix/", "/", "/v1")

	for _, name := range []string{"viewer-and-edit", "finance", "identity-admin", "broad"} {
		for _, resource := range resources {
			for _, method := range []string{http.MethodGet, http.MethodPost} {
				want, err := client.Decide(context.Background(), tenant.Principal{TenantID: parityTenant, UserID: name}, method, resource)
				if err != nil {
					t.Fatal(err)
				}
				got := catalogue.Evaluate(catalogue.HeldIn(principals[name], false), method, resource)
				if got.Allow != want.Allow || got.DutyMissing != want.DutyMissing {
					t.Errorf("%s %s %s: engine allow=%v missing=%q, OPA allow=%v missing=%q", name, method, resource, got.Allow, got.DutyMissing, want.Allow, want.DutyMissing)
				}
			}
		}
	}
}

func TestEffectiveReportEqualsOPAForEveryRoute(t *testing.T) {
	base := startRealPolicy(t)
	catalogue := loadSeededCatalogue(t)
	client, err := NewOPAClient(base, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	principals := parityPrincipals(catalogue)
	putAssignments(t, base, principals)

	for _, name := range []string{"viewer-and-edit", "finance", "identity-admin", "broad"} {
		report := catalogue.Effective(catalogue.HeldIn(principals[name], false))
		if len(report) != len(catalogue.Routes()) {
			t.Fatalf("%s: report has %d rows, want %d", name, len(report), len(catalogue.Routes()))
		}
		for _, row := range report {
			want, err := client.Decide(context.Background(), tenant.Principal{TenantID: parityTenant, UserID: name}, row.Method, SampleResource(row.Resource))
			if err != nil {
				t.Fatal(err)
			}
			if row.Allow != want.Allow || row.DutyMissing != want.DutyMissing {
				t.Errorf("%s %s %s: report allow=%v missing=%q, OPA allow=%v missing=%q", name, row.Method, row.Resource, row.Allow, row.DutyMissing, want.Allow, want.DutyMissing)
			}
		}
	}
}

func TestCatalogueRejectsGlobsOutsideTheSupportedSubset(t *testing.T) {
	for _, resource := range []string{"/v1/a/**", "/v1/a/?", "/v1/a/[x]", "/v1/{a,b}", "/v1/a{,/**}/b"} {
		raw := fmt.Sprintf(`{"duties":{"d":{"permissions":[{"resource":%q,"methods":["GET"]}]}}}`, resource)
		if _, err := LoadCatalogue([]byte(raw)); err == nil {
			t.Errorf("%s: want error", resource)
		}
	}
}

func TestEvaluateReportsGrantingAndMissingDuty(t *testing.T) {
	c, err := LoadCatalogue([]byte(`{"duties":{
		"Wide":{"permissions":[{"resource":"/v1/x{,/**}","methods":["GET","POST"]}]},
		"Narrow":{"permissions":[{"resource":"/v1/x/*","methods":["GET"]}]},
		"Other":{"permissions":[{"resource":"/v1/x/*","methods":["GET"]}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		duties       []string
		method, path string
		allow        bool
		duty, missed string
	}{
		{"first sorted holder grants", []string{"Wide", "Narrow"}, "GET", "/v1/x/1", true, "Narrow", ""},
		{"unknown duty grants nothing", []string{"Ghost"}, "GET", "/v1/x/1", false, "", "Narrow"},
		{"least privileged missing duty", nil, "GET", "/v1/x/1", false, "", "Narrow"},
		{"only wide covers post", nil, "POST", "/v1/x/1", false, "", "Wide"},
		{"star stops at slash", []string{"Narrow"}, "GET", "/v1/x/1/2", false, "", "Wide"},
		{"no candidate", []string{"Wide"}, "DELETE", "/v1/x/1", false, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := c.Evaluate(tt.duties, tt.method, tt.path)
			if got.Allow != tt.allow || got.Duty != tt.duty || got.DutyMissing != tt.missed {
				t.Fatalf("got %+v, want allow=%v duty=%q missing=%q", got, tt.allow, tt.duty, tt.missed)
			}
		})
	}
}

func TestOperatorOnlyDutiesAreHeldOnlyInTheOperatorTenant(t *testing.T) {
	catalogue := loadSeededCatalogue(t)
	duties := []string{"ops.failover.execute", "ops.region.view", "party.vendor.view"}
	if got := catalogue.HeldIn(duties, false); !slices.Equal(got, []string{"party.vendor.view"}) {
		t.Fatalf("outside the operator tenant: %v", got)
	}
	if got := catalogue.HeldIn(duties, true); !slices.Equal(got, duties) {
		t.Fatalf("in the operator tenant: %v", got)
	}
	if g := catalogue.Evaluate(catalogue.HeldIn(duties, false), http.MethodPost, "/v1/ops/failover"); g.Allow || g.DutyMissing != "ops.failover.execute" {
		t.Fatalf("outside: %+v", g)
	}
	if g := catalogue.Evaluate(catalogue.HeldIn(duties, true), http.MethodPost, "/v1/ops/failover"); !g.Allow {
		t.Fatalf("operator: %+v", g)
	}
}
