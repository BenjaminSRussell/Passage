package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

const ownerID = "0193d6a0-0000-7000-8000-000000000001"

func validRule() DQRule {
	return DQRule{
		RuleID: "vendor_tax_id", AssetFQN: "core.vendor", Type: RuleCompleteness,
		Expression: "tax_id", ThresholdPct: DefaultThreshold(), Severity: SeverityHigh,
		OwnerUserID: ownerID, Status: RuleActive,
	}
}

func TestDQRuleValidate(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*DQRule)
		ok     bool
	}{
		{"valid", func(*DQRule) {}, true},
		{"bad rule id", func(r *DQRule) { r.RuleID = "Vendor-Tax" }, false},
		{"bad asset", func(r *DQRule) { r.AssetFQN = "vendor tax" }, false},
		{"unknown type", func(r *DQRule) { r.Type = "FUZZY" }, false},
		{"empty expression", func(r *DQRule) { r.Expression = "" }, false},
		{"threshold above 100", func(r *DQRule) { r.ThresholdPct = decimal.NewFromInt(101) }, false},
		{"threshold with three decimals", func(r *DQRule) { r.ThresholdPct = decimal.RequireFromString("97.125") }, false},
		{"negative threshold", func(r *DQRule) { r.ThresholdPct = decimal.NewFromInt(-1) }, false},
		{"bad severity", func(r *DQRule) { r.Severity = "CRITICAL" }, false},
		{"owner not uuid", func(r *DQRule) { r.OwnerUserID = "alice" }, false},
		{"status unknown", func(r *DQRule) { r.Status = "DRAFT" }, false},
		{"validity regex compiles", func(r *DQRule) { r.Type = RuleValidity; r.Expression = `^[A-Z]{2}\d+$` }, true},
		{"validity regex broken", func(r *DQRule) { r.Type = RuleValidity; r.Expression = `([` }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := validRule()
			c.mutate(&r)
			if err := r.Validate(); (err == nil) != c.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, c.ok)
			}
		})
	}
}

func TestPassPercent(t *testing.T) {
	cases := []struct {
		total, failed int64
		want          string
		wantErr       bool
	}{
		{100, 0, "100", false},
		{100, 3, "97", false},
		{1000, 1, "99.9", false},
		{3, 1, "66.67", false},
		{0, 0, "100", false},
		{10, 11, "", true},
		{-1, 0, "", true},
	}
	for _, c := range cases {
		got, err := PassPercent(c.total, c.failed)
		if (err != nil) != c.wantErr {
			t.Fatalf("PassPercent(%d,%d) err=%v", c.total, c.failed, err)
		}
		if err == nil && got.String() != c.want {
			t.Errorf("PassPercent(%d,%d)=%s want %s", c.total, c.failed, got, c.want)
		}
	}
}

func TestEvaluate_BelowThresholdBoundary(t *testing.T) {
	r := validRule()
	// 98% exactly passes: the rule fails only strictly below threshold.
	s, err := Evaluate(r, "2026-10-21", 100, 2)
	if err != nil || s.Below {
		t.Fatalf("98%% should not be below: %+v %v", s, err)
	}
	s, _ = Evaluate(r, "2026-10-21", 100, 3)
	if !s.Below {
		t.Fatalf("97%% should be below: %+v", s)
	}
	// 97.995% rounds half-even to 98.00 for display but is still below 98%.
	s, _ = Evaluate(r, "2026-10-21", 200_000, 4_010)
	if s.PassPct.String() != "98" || !s.Below {
		t.Fatalf("97.995%% must display 98 and be below: %+v", s)
	}
	// A 100% threshold fails on one bad row in a million, though it rounds to 100.00.
	r.ThresholdPct = decimal.NewFromInt(100)
	s, _ = Evaluate(r, "2026-10-21", 1_000_000, 1)
	if !s.Below {
		t.Fatalf("one failed row must be below a 100%% threshold: %+v", s)
	}
	// An empty population cannot fail.
	if s, _ = Evaluate(r, "2026-10-21", 0, 0); s.Below {
		t.Fatalf("empty population must pass: %+v", s)
	}
}

func TestRouteAndStreak_BR46_15(t *testing.T) {
	cases := []struct {
		name   string
		below  bool
		streak int
		muted  bool
		sent   Notified
		want   Routing
	}{
		{"passing silent", false, 0, false, Notified{}, Routing{}},
		{"muted never notifies", true, 5, true, Notified{}, Routing{}},
		{"first failure owner task", true, 1, false, Notified{}, Routing{OwnerTask: true}},
		{"second failure same day is silent", true, 2, false, Notified{OwnerToday: true}, Routing{}},
		{"third failure escalates once", true, 3, false, Notified{OwnerToday: true}, Routing{StewardEscalation: true}},
		{"fourth failure next day: owner again, steward already told", true, 4, false, Notified{StewardInStreak: true}, Routing{OwnerTask: true}},
		{"unmuted after a muted escalation point still escalates", true, 5, false, Notified{}, Routing{OwnerTask: true, StewardEscalation: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Route(c.below, c.streak, c.muted, c.sent); got != c.want {
				t.Fatalf("Route = %+v want %+v", got, c.want)
			}
		})
	}
	streak := 0
	for _, below := range []bool{true, true, false, true} {
		streak = NextBelowStreak(streak, below)
	}
	if streak != 1 {
		t.Fatalf("streak after pass reset = %d, want 1", streak)
	}
}

func TestTaskKeyOncePerRulePerDay(t *testing.T) {
	if TaskKey("vendor_tax_id", "2026-10-21") == TaskKey("vendor_tax_id", "2026-10-22") {
		t.Fatal("task key must change per day")
	}
	if TaskKey("a_rule", "2026-10-21") != TaskKey("a_rule", "2026-10-21") {
		t.Fatal("task key must be stable within a day")
	}
}

func TestMuteWindow(t *testing.T) {
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	until := MuteUntil(now)
	if !IsMuted(&until, now.AddDate(0, 0, 6)) {
		t.Fatal("rule must be muted for 7 days")
	}
	if IsMuted(&until, now.AddDate(0, 0, 7)) {
		t.Fatal("mute must end after 7 days")
	}
	if IsMuted(nil, now) {
		t.Fatal("a rule without a mute window is not muted")
	}
}

func TestFreshnessPasses(t *testing.T) {
	now := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	if !FreshnessPasses(now.Add(-time.Hour), now, 2*time.Hour) {
		t.Fatal("one hour old must pass a two hour limit")
	}
	if FreshnessPasses(now.Add(-3*time.Hour), now, 2*time.Hour) {
		t.Fatal("three hours old must fail a two hour limit")
	}
}

func TestPassPercentProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		total := rapid.Int64Range(0, 1_000_000).Draw(t, "total")
		failed := rapid.Int64Range(0, total).Draw(t, "failed")
		pass, err := PassPercent(total, failed)
		if err != nil {
			t.Fatal(err)
		}
		if pass.IsNegative() || pass.GreaterThan(decimal.NewFromInt(100)) {
			t.Fatalf("pass %s out of [0,100]", pass)
		}
		if failed == 0 && !pass.Equal(decimal.NewFromInt(100)) {
			t.Fatalf("no failures must pass fully, got %s", pass)
		}
	})
}
