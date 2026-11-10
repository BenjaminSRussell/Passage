package devenv

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func loadSeed(t *testing.T) Seed {
	t.Helper()
	seed, err := LoadSeed(filepath.Join(repoRoot(t), "deploy", "local", "seed.json"))
	if err != nil {
		t.Fatal(err)
	}
	return seed
}

func TestSeedDutiesAllExistInTheAuthzCatalogue(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "policies", "authz", "data.json"))
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Duties map[string]json.RawMessage `json:"duties"`
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatal(err)
	}
	for _, u := range loadSeed(t).Users {
		for _, duty := range u.Duties {
			if _, ok := data.Duties[duty]; !ok {
				t.Errorf("%s holds %q, which policies/authz/data.json does not define", u.Username, duty)
			}
		}
	}
}

func TestSeedHasAnAdminAndANonAdmin(t *testing.T) {
	seed := loadSeed(t)
	admin, ok := seed.User("admin")
	if !ok {
		t.Fatal("seed has no admin user")
	}
	clerk, ok := seed.User("clerk")
	if !ok {
		t.Fatal("seed has no clerk user")
	}
	for _, duty := range []string{"audit.read", "numbering.write", "identity.user.maintain"} {
		if !slices.Contains(admin.Duties, duty) {
			t.Errorf("admin lacks %s", duty)
		}
		if slices.Contains(clerk.Duties, duty) {
			t.Errorf("clerk holds admin duty %s", duty)
		}
	}
}

func TestSeedRejectsDuplicateAndIncompleteUsers(t *testing.T) {
	good := User{Username: "a", Password: "p", Sub: "1", Duties: []string{"x"}}
	cases := map[string]Seed{
		"no users":      {TenantID: "t", LegalEntityID: "l"},
		"no duties":     {TenantID: "t", LegalEntityID: "l", Users: []User{{Username: "a", Password: "p", Sub: "1"}}},
		"repeated sub":  {TenantID: "t", LegalEntityID: "l", Users: []User{good, {Username: "b", Password: "p", Sub: "1", Duties: []string{"x"}}}},
		"missing owner": {Users: []User{good}},
	}
	for name, seed := range cases {
		if err := seed.validate(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestRenderRealmKeepsProductionMappersAndAddsSeededUsers(t *testing.T) {
	root := repoRoot(t)
	base, err := os.ReadFile(filepath.Join(root, "deploy", "keycloak", "realm-passage.json"))
	if err != nil {
		t.Fatal(err)
	}
	seed := loadSeed(t)
	out, err := RenderRealm(base, seed, "http://localhost:5173")
	if err != nil {
		t.Fatal(err)
	}
	var realm struct {
		SSLRequired string `json:"sslRequired"`
		BrowserFlow string `json:"browserFlow"`
		Flows       []struct {
			Alias string `json:"alias"`
		} `json:"authenticationFlows"`
		Clients []struct {
			ClientID     string   `json:"clientId"`
			RedirectURIs []string `json:"redirectUris"`
			WebOrigins   []string `json:"webOrigins"`
			PublicClient bool     `json:"publicClient"`
			Mappers      []struct {
				Config map[string]string `json:"config"`
			} `json:"protocolMappers"`
		} `json:"clients"`
		Users []struct {
			ID          string              `json:"id"`
			Username    string              `json:"username"`
			Attributes  map[string][]string `json:"attributes"`
			Credentials []struct {
				Temporary bool `json:"temporary"`
			} `json:"credentials"`
		} `json:"users"`
	}
	if err := json.Unmarshal(out, &realm); err != nil {
		t.Fatal(err)
	}
	if realm.SSLRequired != "none" || realm.BrowserFlow != "passage-browser-dev" {
		t.Errorf("dev realm sslRequired=%q browserFlow=%q", realm.SSLRequired, realm.BrowserFlow)
	}
	flowDefined := false
	for _, f := range realm.Flows {
		flowDefined = flowDefined || f.Alias == realm.BrowserFlow
	}
	if !flowDefined {
		t.Errorf("browserFlow %q is not defined by the rendered realm", realm.BrowserFlow)
	}
	claims := map[string]bool{}
	for _, c := range realm.Clients {
		if c.ClientID != WebClientID {
			continue
		}
		if !c.PublicClient || len(c.RedirectURIs) != 1 || c.RedirectURIs[0] != "http://localhost:5173/auth/callback" || c.WebOrigins[0] != "http://localhost:5173" {
			t.Errorf("web client = %+v", c)
		}
		for _, m := range c.Mappers {
			claims[m.Config["claim.name"]] = true
		}
	}
	for _, claim := range []string{"tid", "le", "duties", "tier"} {
		if !claims[claim] {
			t.Errorf("web client no longer maps claim %q", claim)
		}
	}
	found := map[string]bool{}
	for _, u := range realm.Users {
		for _, want := range seed.Users {
			if u.Username != want.Username {
				continue
			}
			found[u.Username] = true
			if u.Attributes["tenant_id"][0] != seed.TenantID || u.Attributes["duties"][0] != want.DutyClaim() || u.Credentials[0].Temporary {
				t.Errorf("%s = %+v", u.Username, u)
			}
		}
	}
	if len(found) != len(seed.Users) {
		t.Errorf("realm has users %v, want every seeded user", found)
	}
	if bytes.Contains(base, []byte("localhost:5173")) {
		t.Error("the production export must not carry the dev redirect")
	}
}

func TestRenderedUserIDsAreTheAssignmentKeys(t *testing.T) {
	seed := loadSeed(t)
	assignments, err := RenderAssignments(seed)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Assignments map[string]map[string][]string `json:"assignments"`
	}
	if err := json.Unmarshal(assignments, &doc); err != nil {
		t.Fatal(err)
	}
	base, err := os.ReadFile(filepath.Join(repoRoot(t), "deploy", "keycloak", "realm-passage.json"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := RenderRealm(base, seed, "http://localhost:5173")
	if err != nil {
		t.Fatal(err)
	}
	var realm struct {
		Users []struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"users"`
	}
	if err := json.Unmarshal(out, &realm); err != nil {
		t.Fatal(err)
	}
	for _, u := range realm.Users {
		if _, seeded := seed.User(u.Username); !seeded {
			continue
		}
		if _, ok := doc.Assignments[seed.TenantID][u.ID]; !ok {
			t.Errorf("rendered user %s has id %q, which is not an assignments key", u.Username, u.ID)
		}
	}
}

func opaDecision(t *testing.T, assignments []byte, user, method, path string) (allow bool, dutyMissing string) {
	t.Helper()
	opa, err := exec.LookPath("opa")
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatal("opa is required in CI")
		}
		t.Skip("opa is not installed")
	}
	root := repoRoot(t)
	dir := t.TempDir()
	assignmentsPath := filepath.Join(dir, "assignments.json")
	if err := os.WriteFile(assignmentsPath, assignments, 0o600); err != nil {
		t.Fatal(err)
	}
	seed := loadSeed(t)
	input, _ := json.Marshal(map[string]any{"subject": map[string]string{"tenant_id": seed.TenantID, "user_id": user}, "action": method, "resource": path})
	inputPath := filepath.Join(dir, "input.json")
	if err := os.WriteFile(inputPath, input, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(opa, "eval", "--format", "json", "--data", filepath.Join(root, "policies", "authz", "authz.rego"),
		"--data", filepath.Join(root, "policies", "authz", "data.json"), "--data", assignmentsPath, "--input", inputPath, "data.authz.decision")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("opa eval: %v\n%s", err, out)
	}
	var result struct {
		Result []struct {
			Expressions []struct {
				Value struct {
					Allow       bool   `json:"allow"`
					DutyMissing string `json:"duty_missing"`
				} `json:"value"`
			} `json:"expressions"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &result); err != nil || len(result.Result) == 0 {
		t.Fatalf("opa output %s: %v", out, err)
	}
	v := result.Result[0].Expressions[0].Value
	return v.Allow, v.DutyMissing
}

func TestRenderedAssignmentsAllowAdminAndDenyClerkOnAdminRoutes(t *testing.T) {
	seed := loadSeed(t)
	assignments, err := RenderAssignments(seed)
	if err != nil {
		t.Fatal(err)
	}
	admin, _ := seed.User("admin")
	clerk, _ := seed.User("clerk")
	adminRoutes := []struct{ method, path string }{
		{"POST", "/v1/numbering/sequences"},
		{"GET", "/v1/numbering/sequences"},
		{"POST", "/v1/identity/users"},
	}
	for _, route := range adminRoutes {
		if allow, _ := opaDecision(t, assignments, admin.Sub, route.method, route.path); !allow {
			t.Errorf("admin denied %s %s", route.method, route.path)
		}
		allow, missing := opaDecision(t, assignments, clerk.Sub, route.method, route.path)
		if allow {
			t.Errorf("clerk allowed %s %s", route.method, route.path)
		}
		if missing == "" {
			t.Errorf("clerk denied %s %s without a duty_missing hint", route.method, route.path)
		}
	}
	if allow, _ := opaDecision(t, assignments, clerk.Sub, "GET", "/v1/ledger/journal-lines"); !strings.Contains(strings.Join(clerk.Duties, ","), "ledger.journal.read") || !allow {
		t.Error("clerk must keep read access to the journal")
	}
}
