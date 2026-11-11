package cloudprofile

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

const realmFile = "../../../deploy/keycloak/realm-passage.json"

func loadRealm(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(realmFile)
	if err != nil {
		t.Fatal(err)
	}
	var realm map[string]any
	if err := json.Unmarshal(b, &realm); err != nil {
		t.Fatal(err)
	}
	return realm
}

func TestRealmPatchFollowsTheProfile(t *testing.T) {
	gcc := mustResolve(t, "gcc-high")
	patch, err := RenderRealmPatch(gcc, RealmOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if patch.Set["passwordPolicy"] != "hashAlgorithm(pbkdf2-sha512) and length(14) and notUsername" {
		t.Errorf("passwordPolicy = %v", patch.Set["passwordPolicy"])
	}
	if patch.Set["eventsExpiration"] != 365*86400 {
		t.Errorf("a gcc-high realm keeps events 365 days, got %v", patch.Set["eventsExpiration"])
	}
	if _, ok := patch.Set["ssoSessionIdleTimeout"]; ok {
		t.Error("no idle session timeout without the hipaa overlay")
	}

	hipaa, err := RenderRealmPatch(mustResolve(t, "gcc-high", "hipaa"), RealmOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if hipaa.Set["eventsExpiration"] != 2190*86400 || hipaa.Set["adminEventsExpiration"] != 2190*86400 {
		t.Errorf("hipaa keeps events six years, got %v / %v", hipaa.Set["eventsExpiration"], hipaa.Set["adminEventsExpiration"])
	}
	if hipaa.Set["ssoSessionIdleTimeout"] != 15*60 {
		t.Errorf("hipaa SSO idle timeout = %v", hipaa.Set["ssoSessionIdleTimeout"])
	}
}

func TestApplyingTheHipaaPatchToTheRealmExport(t *testing.T) {
	realm := loadRealm(t)
	patch, _ := RenderRealmPatch(mustResolve(t, "commercial", "hipaa"), RealmOptions{})
	ApplyRealmPatch(realm, patch)
	if realm["eventsExpiration"] != 2190*86400 || realm["ssoSessionIdleTimeout"] != 900 {
		t.Errorf("realm = events %v idle %v", realm["eventsExpiration"], realm["ssoSessionIdleTimeout"])
	}
	// Everything else the realm had is untouched.
	if realm["bruteForceProtected"] != true || realm["accessTokenLifespan"] != float64(300) {
		t.Errorf("the patch must not change unrelated realm settings: %v %v", realm["bruteForceProtected"], realm["accessTokenLifespan"])
	}
}

func TestTrustIdPMFAFlowSkipsTheSecondFactorOnlyWhenAmrShowsMFA(t *testing.T) {
	p := mustResolve(t, "gcc-high")
	patch, err := RenderRealmPatch(p, RealmOptions{TrustIdPMFA: true})
	if err != nil {
		t.Fatal(err)
	}
	if patch.IdentityProviderFlow != TrustIdPMFAFlow {
		t.Fatalf("the IdP must be pointed at %s", TrustIdPMFAFlow)
	}
	// The condition is negated: the second factor runs when idp_amr does NOT match.
	cfg := patch.AuthenticatorConfig[0]["config"].(map[string]string)
	if cfg["attribute_name"] != AttrIdPAMR || cfg["not"] != "true" || cfg["regex"] != "true" {
		t.Errorf("condition config = %v", cfg)
	}
	re := regexp.MustCompile(cfg["attribute_expected_value"])
	for amr, want := range map[string]bool{
		`["pwd","mfa"]`: true, `pwd,mfa`: true, `mfa`: true, `["hwk"]`: true,
		`["pwd"]`: false, `["pwd","otp"]`: false, `["pwdmfa"]`: false, ``: false, `["mfaless"]`: false,
	} {
		if re.MatchString(amr) != want {
			t.Errorf("amr %q matches = %v, want %v", amr, !want, want)
		}
	}

	realm := loadRealm(t)
	ApplyRealmPatch(realm, patch)
	var haveTrust, haveGate, haveOriginal bool
	for _, f := range realm["authenticationFlows"].([]any) {
		switch f.(map[string]any)["alias"] {
		case TrustIdPMFAFlow:
			haveTrust = true
		case trustFlowGate:
			haveGate = true
		case PostBrokerFlow:
			haveOriginal = true
		}
	}
	if !haveTrust || !haveGate || !haveOriginal {
		t.Errorf("flows: trust=%v gate=%v original kept=%v", haveTrust, haveGate, haveOriginal)
	}
	for _, i := range realm["identityProviders"].([]any) {
		if i.(map[string]any)["postBrokerLoginFlowAlias"] != TrustIdPMFAFlow {
			t.Errorf("identity provider %v is not pointed at the trust flow", i.(map[string]any)["alias"])
		}
	}
	// The gate references the existing second-factor sub-flow, which must exist in the realm.
	if !strings.Contains(mustJSON(t, realm), `"alias":"`+PostBrokerFlow+` second factor"`) {
		t.Error("the trust flow's second factor sub-flow is missing from the realm")
	}
}

func TestTrustIdPMFAMappersRewriteTheClaimsAtEveryLogin(t *testing.T) {
	for _, m := range TrustIdPMFAMappers() {
		if m["config"].(map[string]string)["syncMode"] != "FORCE" {
			t.Errorf("mapper %v must use FORCE so a stale amr cannot carry over", m["name"])
		}
	}
}

func TestBadAMRRegexIsRefused(t *testing.T) {
	if _, err := RenderRealmPatch(mustResolve(t, "gcc-high"), RealmOptions{TrustIdPMFA: true, AMRRegex: "("}); err == nil {
		t.Error("an invalid regex must be refused")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
