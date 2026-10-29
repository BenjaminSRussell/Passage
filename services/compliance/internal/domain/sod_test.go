package domain

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

var now = time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)

type fixture struct {
	Roles map[string][]string `json:"roles"`
	Users []struct {
		UserID string   `json:"userId"`
		Roles  []string `json:"roles"`
		Expect []string `json:"expect"`
	} `json:"users"`
}

func rolesOf(t *testing.T, catalog map[string][]string, names []string) []Role {
	t.Helper()
	out := make([]Role, 0, len(names))
	for _, n := range names {
		duties, ok := catalog[n]
		if !ok {
			t.Fatalf("unknown role %q in fixture", n)
		}
		out = append(out, Role{Name: n, Duties: duties})
	}
	return out
}

func ruleIDs(cs []Conflict) []string {
	ids := []string{}
	for _, c := range cs {
		ids = append(ids, c.RuleID)
	}
	return ids
}

func TestSeedRulesAreValid(t *testing.T) {
	seen := map[string]bool{}
	for _, r := range SeedRules() {
		if err := ValidateRule(r); err != nil {
			t.Errorf("seed %s invalid: %v", r.RuleID, err)
		}
		if seen[r.RuleID] {
			t.Errorf("duplicate seed %s", r.RuleID)
		}
		seen[r.RuleID] = true
	}
	if len(seen) != 11 {
		t.Errorf("want 11 seed rules, got %d", len(seen))
	}
}

func TestValidateRule_Table(t *testing.T) {
	base := seed("P2P-009", "x", "A", "B", SeverityHigh)
	tests := []struct {
		name   string
		mutate func(*Rule)
		want   bool
	}{
		{"ok", func(*Rule) {}, true},
		{"bad id", func(r *Rule) { r.RuleID = "p2p9" }, false},
		{"same duty", func(r *Rule) { r.DutyB = "A" }, false},
		{"empty duty", func(r *Rule) { r.DutyA = "" }, false},
		{"bad pattern", func(r *Rule) { r.DutyA = "[" }, false},
		{"bad severity", func(r *Rule) { r.Severity = "CRITICAL" }, false},
		{"no risk", func(r *Rule) { r.RiskID = "" }, false},
		{"cap too big", func(r *Rule) { r.MaxMitigationDays = 731 }, false},
		{"cap zero", func(r *Rule) { r.MaxMitigationDays = 0 }, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			tc.mutate(&r)
			if got := ValidateRule(r) == nil; got != tc.want {
				t.Errorf("valid = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetect_PlantedFixtureNoFalseNegatives(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/sod-50.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx fixture
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Users) != 50 {
		t.Fatalf("fixture must plant 50 users, has %d", len(fx.Users))
	}
	detected := 0
	for _, u := range fx.Users {
		got := ruleIDs(Detect(u.UserID, SeedRules(), rolesOf(t, fx.Roles, u.Roles)))
		if !reflect.DeepEqual(got, u.Expect) {
			t.Errorf("%s: detected %v, want %v", u.UserID, got, u.Expect)
		}
		if len(got) > 0 {
			detected++
		}
	}
	if detected != 50 {
		t.Errorf("detected %d of 50 planted conflicts", detected)
	}
}

func TestDetect_Table(t *testing.T) {
	tests := []struct {
		name  string
		roles []Role
		want  []string
	}{
		{"no roles", nil, []string{}},
		{"one side only", []Role{{Name: "Maint", Duties: []string{"MaintainVendors"}}}, []string{}},
		{"same role grants both", []Role{{Name: "Super", Duties: []string{"CreatePurchaseOrders", "ApprovePurchaseOrders"}}}, []string{"P2P-002"}},
		{"wildcard posting", []Role{{Name: "Admin", Duties: []string{"MaintainUsersAndRoles"}}, {Name: "GL", Duties: []string{"PostGeneralJournals"}}}, []string{"SEC-001"}},
		{"payroll and bank details", []Role{{Name: "Pay", Duties: []string{"RunPayroll"}}, {Name: "Bank", Duties: []string{"MaintainWorkerBankDetails"}}}, []string{"HR-001"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ruleIDs(Detect("U-1", SeedRules(), tc.roles))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestDetect_RetiredRuleIgnored(t *testing.T) {
	rules := SeedRules()
	rules[0].Status = RuleRetired
	roles := []Role{{Name: "M", Duties: []string{"MaintainVendors"}}, {Name: "P", Duties: []string{"ProcessVendorPayments"}}}
	if got := Detect("U-1", rules, roles); len(got) != 0 {
		t.Errorf("retired rule still fired: %v", got)
	}
}

func TestSimulate_BlocksNewHighConflictOnly(t *testing.T) {
	rules := SeedRules()
	current := []Role{{Name: "Maint", Duties: []string{"MaintainVendors"}}}
	proposed := []Role{{Name: "Pay", Duties: []string{"ProcessVendorPayments"}}}

	res := Simulate("U-1", rules, current, proposed, nil, true)
	if !res.Blocked || !reflect.DeepEqual(res.BlockingRuleIDs, []string{"P2P-001"}) {
		t.Fatalf("expected block on P2P-001, got %+v", res)
	}
	if res := Simulate("U-1", rules, current, proposed, nil, false); res.Blocked {
		t.Error("blockHigh=false must not block")
	}
	if res := Simulate("U-1", rules, current, proposed, map[string]string{"P2P-001": StateMitigated}, true); res.Blocked || res.NewConflicts[0].State != StateMitigated {
		t.Errorf("mitigated conflict must not block: %+v", res)
	}
	// A RESOLVED record does not count: re-adding the role raises the conflict again.
	if res := Simulate("U-1", rules, current, proposed, map[string]string{"P2P-001": StateResolved}, true); !res.Blocked {
		t.Error("a RESOLVED record must not exempt a re-introduced conflict")
	}
	both := append(append([]Role{}, current...), proposed...)
	extra := []Role{{Name: "Extra", Duties: []string{"ViewReports"}}}
	// AC-48.3: an existing OPEN HIGH conflict blocks any further assignment, but is not reported as new.
	res = Simulate("U-1", rules, both, extra, map[string]string{"P2P-001": StateOpen}, true)
	if !res.Blocked || len(res.NewConflicts) != 0 || !reflect.DeepEqual(res.BlockingRuleIDs, []string{"P2P-001"}) {
		t.Errorf("existing OPEN HIGH conflict must block and not be new: %+v", res)
	}
	// Once mitigated, the existing conflict no longer blocks.
	if res := Simulate("U-1", rules, both, extra, map[string]string{"P2P-001": StateMitigated}, true); res.Blocked {
		t.Errorf("mitigated existing conflict blocked: %+v", res)
	}
}

func TestSimulate_MediumNeverBlocks(t *testing.T) {
	current := []Role{{Name: "GR", Duties: []string{"ReceiveGoods"}}}
	proposed := []Role{{Name: "AP", Duties: []string{"PostVendorInvoices"}}}
	res := Simulate("U-1", SeedRules(), current, proposed, nil, true)
	if res.Blocked || len(res.NewConflicts) != 1 || res.NewConflicts[0].RuleID != "P2P-004" {
		t.Fatalf("MEDIUM P2P-004 must be reported without blocking: %+v", res)
	}
}

func TestMitigate_Rules(t *testing.T) {
	rule := seed("P2P-001", "x", "MaintainVendors", "ProcessVendorPayments", SeverityHigh)
	open := func() Conflict {
		return Conflict{UserID: "U-1", RuleID: "P2P-001", Severity: SeverityHigh, State: StateOpen}
	}
	good := Mitigation{MitigationID: "m1", ControlRef: "CTL-DUAL-APPROVE", Reason: "dual approval", ApproverID: "U-2", ExpiresAt: now.AddDate(0, 0, 180)}
	tests := []struct {
		name    string
		mutate  func(*Conflict, *Rule, *Mitigation)
		code    string
		tenant  int
		wantOK  bool
		wantSet string
	}{
		{"approved with 180 days", func(*Conflict, *Rule, *Mitigation) {}, "", 0, true, StateMitigated},
		{"self approval refused", func(_ *Conflict, _ *Rule, m *Mitigation) { m.ApproverID = "U-1" }, CodeSelfApproval, 0, false, ""},
		{"self approval ignores case", func(_ *Conflict, _ *Rule, m *Mitigation) { m.ApproverID = "u-1" }, CodeSelfApproval, 0, false, ""},
		{"missing approver refused", func(_ *Conflict, _ *Rule, m *Mitigation) { m.ApproverID = "" }, CodeMissingField, 0, false, ""},
		{"blank control refused", func(_ *Conflict, _ *Rule, m *Mitigation) { m.ControlRef = "" }, CodeMissingField, 0, false, ""},
		{"exactly at the cap", func(_ *Conflict, _ *Rule, m *Mitigation) { m.ExpiresAt = now.AddDate(0, 0, 365) }, "", 0, true, StateMitigated},
		{"blank reason refused", func(_ *Conflict, _ *Rule, m *Mitigation) { m.Reason = "  " }, CodeMissingField, 0, false, ""},
		{"not allowed by rule", func(_ *Conflict, r *Rule, _ *Mitigation) { r.MitigationAllowed = false }, CodeNotMitigatable, 0, false, ""},
		{"expiry past the rule cap", func(_ *Conflict, _ *Rule, m *Mitigation) { m.ExpiresAt = now.AddDate(0, 0, 400) }, CodeExpiryTooLong, 0, false, ""},
		{"tenant cap is stricter", func(_ *Conflict, _ *Rule, _ *Mitigation) {}, CodeExpiryTooLong, 90, false, ""},
		{"expiry in the past", func(_ *Conflict, _ *Rule, m *Mitigation) { m.ExpiresAt = now.Add(-time.Hour) }, CodeExpiryTooLong, 0, false, ""},
		{"not open", func(c *Conflict, _ *Rule, _ *Mitigation) { c.State = StateResolved }, CodeNotOpen, 0, false, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, r, m := open(), rule, good
			tc.mutate(&c, &r, &m)
			err := c.Mitigate(r, m, now, tc.tenant)
			if tc.wantOK {
				if err != nil || c.State != tc.wantSet || c.MitigationID != "m1" || c.ExpiresAt == nil || !c.ExpiresAt.Equal(m.ExpiresAt) {
					t.Fatalf("want MITIGATED until %s, got state=%s expires=%v err=%v", m.ExpiresAt, c.State, c.ExpiresAt, err)
				}
				return
			}
			if c.State != StateOpen && tc.name != "not open" {
				t.Errorf("a refused mitigation changed the state to %s", c.State)
			}
			de, ok := err.(*Error)
			if !ok || de.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
		})
	}
}

func TestExpireReopensLapsedMitigation(t *testing.T) {
	at := now
	c := Conflict{RuleID: "P2P-001", Severity: SeverityHigh, State: StateMitigated, MitigationID: "m1", ExpiresAt: &at}
	if c.Expire(now.Add(-time.Second)) {
		t.Fatal("must not expire before ExpiresAt")
	}
	if !c.Expire(now) || c.State != StateOpen || c.MitigationID != "" || c.ExpiresAt != nil {
		t.Fatalf("expected reopen, got %+v", c)
	}
	if c.Expire(now.Add(time.Hour)) {
		t.Error("an OPEN conflict cannot expire again")
	}
}

func TestConflictJSONOmitsUnsetExpiry(t *testing.T) {
	raw, err := json.Marshal(Conflict{RuleID: "P2P-001", State: StateOpen})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "expiresAt") {
		t.Errorf("an OPEN conflict must not carry an expiry: %s", raw)
	}
}

func TestAcceptRisk(t *testing.T) {
	tests := []struct {
		name     string
		conflict Conflict
		reason   string
		code     string
	}{
		{"medium accepted", Conflict{Severity: SeverityMedium, State: StateOpen}, "compensated by monthly review", ""},
		{"low accepted", Conflict{Severity: SeverityLow, State: StateOpen}, "small site", ""},
		{"high refused", Conflict{Severity: SeverityHigh, State: StateOpen}, "x", CodeAcceptRisk},
		{"reason required", Conflict{Severity: SeverityMedium, State: StateOpen}, " ", CodeMissingField},
		{"only from OPEN", Conflict{Severity: SeverityMedium, State: StateMitigated}, "x", CodeNotOpen},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.conflict
			err := c.AcceptRisk(tc.reason)
			if tc.code == "" {
				if err != nil || c.State != StateAcceptedRisk {
					t.Fatalf("want ACCEPTED_RISK, got %s %v", c.State, err)
				}
				return
			}
			if de, ok := err.(*Error); !ok || de.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, err)
			}
			if c.State != tc.conflict.State {
				t.Errorf("refused acceptance changed state to %s", c.State)
			}
		})
	}
}

func TestResolve(t *testing.T) {
	at := now
	for _, from := range []string{StateOpen, StateMitigated, StateAcceptedRisk} {
		c := Conflict{State: from, MitigationID: "m1", ExpiresAt: &at}
		if err := c.Resolve(); err != nil || c.State != StateResolved || c.MitigationID != "" || c.ExpiresAt != nil {
			t.Fatalf("resolve from %s: %+v %v", from, c, err)
		}
		if err := c.Resolve(); err == nil {
			t.Error("resolving a RESOLVED conflict must fail")
		}
	}
}

// Property: any role set that carries both duties of a seeded rule, in any
// order and with arbitrary extra duties, reports that rule. No false negatives.
func TestDetect_PropertyNeverMissesSeededConflict(t *testing.T) {
	rules := SeedRules()
	rapid.Check(t, func(t *rapid.T) {
		rule := rules[rapid.IntRange(0, len(rules)-1).Draw(t, "rule")]
		a, b := concrete(rule.DutyA), concrete(rule.DutyB)
		extra := rapid.SliceOfN(rapid.StringMatching(`[a-z]{3,8}`), 0, 6).Draw(t, "extra")
		roles := []Role{{Name: "RoleA", Duties: []string{a}}, {Name: "RoleB", Duties: []string{b}}}
		for i, d := range extra {
			roles = append(roles, Role{Name: "X" + string(rune('a'+i)), Duties: []string{d}})
		}
		for i := range roles {
			j := rapid.IntRange(0, len(roles)-1).Draw(t, "swap")
			roles[i], roles[j] = roles[j], roles[i]
		}

		found := false
		for _, c := range Detect("U-1", rules, roles) {
			if c.RuleID == rule.RuleID {
				found = true
			}
		}
		if !found {
			t.Fatalf("missed %s with roles %+v", rule.RuleID, roles)
		}
	})
}

// concrete turns a wildcard duty pattern into one duty it matches.
func concrete(p string) string {
	if p == "*Post*" {
		return "PostCustomerPayments"
	}
	return p
}
