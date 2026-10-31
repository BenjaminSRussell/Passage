package domain

import (
	"encoding/json"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type distinctFixture struct {
	Roles map[string][]string `json:"roles"`
	Users []struct {
		UserID string   `json:"userId"`
		Roles  []string `json:"roles"`
		Expect []string `json:"expect"`
	} `json:"users"`
	NearMisses []struct {
		UserID string   `json:"userId"`
		Roles  []string `json:"roles"`
		Expect []string `json:"expect"`
	} `json:"nearMisses"`
}

// The seed rules of FR-48.4 rated MEDIUM; every other seed rule is HIGH.
var mediumSeedRules = map[string]bool{"P2P-004": true, "GL-002": true}

// AC2 on fx-sod-50-distinct: 50 pairwise distinct conflicting role
// combinations (single-duty roles, one role holding both duties of a rule,
// several conflicts per user, all 11 seed rules) are all detected with exactly
// the hand-derived rules, 12 near misses raise nothing, and simulating each
// planted assignment blocks exactly when a HIGH rule is among its conflicts.
func TestDetect_FiftyDistinctCombinationsAndNearMisses(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/sod-50-distinct.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx distinctFixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	combos := map[string]bool{}
	covered := map[string]bool{}
	conflicts := 0
	for _, u := range fx.Users {
		set := append([]string(nil), u.Roles...)
		sort.Strings(set)
		combos[strings.Join(set, "+")] = true
		roles := rolesOf(t, fx.Roles, u.Roles)
		got := Detect(u.UserID, SeedRules(), roles)
		if ids := ruleIDs(got); !reflect.DeepEqual(ids, u.Expect) {
			t.Errorf("%s %v: detected %v, want %v", u.UserID, u.Roles, ids, u.Expect)
		}
		for _, c := range got {
			covered[c.RuleID] = true
			if len(c.RolesA) == 0 || len(c.RolesB) == 0 || c.DutyA == c.DutyB {
				t.Errorf("%s %s: conflict lacks granting roles or distinct duties: %+v", u.UserID, c.RuleID, c)
			}
		}
		conflicts += len(got)
		wantBlocked := false
		for _, id := range u.Expect {
			if !mediumSeedRules[id] {
				wantBlocked = true
			}
		}
		if res := Simulate(u.UserID, SeedRules(), nil, roles, nil, true); res.Blocked != wantBlocked {
			t.Errorf("%s: simulate blocked = %v, want %v (%v)", u.UserID, res.Blocked, wantBlocked, u.Expect)
		}
	}
	if len(fx.Users) != 50 || len(combos) != 50 {
		t.Fatalf("want 50 planted users with 50 distinct role combinations, got %d and %d", len(fx.Users), len(combos))
	}
	if len(covered) != len(SeedRules()) {
		t.Errorf("planted conflicts cover %d of %d seed rules", len(covered), len(SeedRules()))
	}
	if conflicts != 81 { // hand count of the expect lists; sod_ref.py prints the same total
		t.Errorf("detected %d conflicts, want 81", conflicts)
	}
	for _, u := range fx.NearMisses {
		if got := Detect(u.UserID, SeedRules(), rolesOf(t, fx.Roles, u.Roles)); len(got) != 0 {
			t.Errorf("near miss %s %v raised %v", u.UserID, u.Roles, ruleIDs(got))
		}
	}
}

// The fixture's expectations agree with an independent stdlib Python
// restatement of the seed rules (fnmatch patterns).
func TestSoDDistinctFixtureMatchesReference(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not on PATH")
	}
	out, err := exec.Command(py, "-I", "../../testdata/sod_ref.py", "../../testdata/sod-50-distinct.json").CombinedOutput()
	if err != nil || !strings.HasPrefix(string(out), "ok: 50 distinct combinations, 81 conflicts") {
		t.Fatalf("reference disagrees: %v\n%s", err, out)
	}
}
