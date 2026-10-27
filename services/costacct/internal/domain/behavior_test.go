package domain

import (
	"testing"

	"pgregory.net/rapid"
)

// TestClassifyBehavior_Order covers FR-38.16: explicit rule, element default, Unclassified.
func TestClassifyBehavior_Order(t *testing.T) {
	rules := []BehaviorRule{
		{ElementID: "e1", Behavior: BehaviorFixed, EffectiveFrom: "2026-01-01"},
		{ElementID: "e1", ObjectKind: "InternalOrder", Behavior: BehaviorVariable, EffectiveFrom: "2026-01-01"},
		{ElementID: "e1", ObjectKind: "CostCenter", Behavior: BehaviorVariable, EffectiveFrom: "2026-07-01"},
		{ElementID: "e1", Behavior: BehaviorVariable, EffectiveFrom: "2026-10-01"},
		{ElementID: "e2", ObjectKind: "CostCenter", Behavior: BehaviorFixed, EffectiveFrom: "2026-01-01"},
		{ElementID: "e4", Behavior: BehaviorFixed, EffectiveFrom: "2026-03-15"},
	}
	cases := []struct {
		name, element, def, kind, period, want string
	}{
		{"generic rule applies to any kind", "e1", "", "Product", "2026-03", BehaviorFixed},
		{"kind-specific beats generic of the same date", "e1", "", "InternalOrder", "2026-03", BehaviorVariable},
		{"kind-specific rule from its date", "e1", "", "CostCenter", "2026-09", BehaviorVariable},
		{"rule not yet effective falls back to the generic one", "e1", "", "CostCenter", "2026-06", BehaviorFixed},
		{"later generic rule replaces the earlier generic one", "e1", "", "Product", "2026-10", BehaviorVariable},
		{"later generic rule does not override a kind-specific one", "e1", "", "InternalOrder", "2026-11", BehaviorVariable},
		{"kind rule does not match other kinds", "e2", "", "Product", "2026-03", BehaviorUnclassified},
		{"rule dated mid-month applies from the next period", "e4", "", "Product", "2026-03", BehaviorUnclassified},
		{"rule dated mid-month in the next period", "e4", "", "Product", "2026-04", BehaviorFixed},
		{"element default when no rule", "e3", BehaviorVariable, "CostCenter", "2026-03", BehaviorVariable},
		{"rule beats element default", "e1", BehaviorVariable, "Product", "2026-03", BehaviorFixed},
		{"element default before the first rule", "e1", BehaviorVariable, "Product", "2025-12", BehaviorVariable},
		{"no rule and no default", "e3", "", "CostCenter", "2026-03", BehaviorUnclassified},
		{"Unclassified is never a default", "e3", BehaviorUnclassified, "CostCenter", "2026-03", BehaviorUnclassified},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ClassifyBehavior(rules, c.element, c.def, c.kind, c.period); got != c.want {
				t.Fatalf("got %s, want %s", got, c.want)
			}
		})
	}
}

// TestClassifyBehavior_Property: the result is a known class; with no
// applicable rule it is the element default (or Unclassified); with applicable
// rules it is the behavior of the latest rule of the most specific tier.
func TestClassifyBehavior_Property(t *testing.T) {
	kinds := []string{"", "CostCenter", "InternalOrder", "Product", "Project"}
	dates := []string{"2026-01-01", "2026-04-01", "2026-09-01"}
	rapid.Check(t, func(t *rapid.T) {
		// One rule per (kind, date) at most, as the store's unique index enforces.
		used := map[string]bool{}
		var rules []BehaviorRule
		for i := rapid.IntRange(0, 8).Draw(t, "rules"); i > 0; i-- {
			r := BehaviorRule{
				ElementID:     "e",
				ObjectKind:    rapid.SampledFrom(kinds).Draw(t, "kind"),
				Behavior:      rapid.SampledFrom([]string{BehaviorFixed, BehaviorVariable}).Draw(t, "behavior"),
				EffectiveFrom: rapid.SampledFrom(dates).Draw(t, "from"),
			}
			if !used[r.ObjectKind+r.EffectiveFrom] {
				used[r.ObjectKind+r.EffectiveFrom] = true
				rules = append(rules, r)
			}
		}
		def := rapid.SampledFrom([]string{"", BehaviorFixed, BehaviorVariable, BehaviorUnclassified}).Draw(t, "default")
		kind := rapid.SampledFrom(kinds[1:]).Draw(t, "objectKind")
		period := rapid.SampledFrom([]string{"2025-12", "2026-01", "2026-05", "2026-12"}).Draw(t, "period")

		got := ClassifyBehavior(rules, "e", def, kind, period)
		var specific, generic *BehaviorRule
		for i := range rules {
			r := &rules[i]
			if r.EffectiveFrom > period+"-01" {
				continue
			}
			switch r.ObjectKind {
			case kind:
				if specific == nil || r.EffectiveFrom > specific.EffectiveFrom {
					specific = r
				}
			case "":
				if generic == nil || r.EffectiveFrom > generic.EffectiveFrom {
					generic = r
				}
			}
		}
		want := BehaviorUnclassified
		switch {
		case specific != nil:
			want = specific.Behavior
		case generic != nil:
			want = generic.Behavior
		case ValidBehavior(def):
			want = def
		}
		if got != want {
			t.Fatalf("got %s, want %s (rules %+v, kind %s, period %s, default %q)", got, want, rules, kind, period, def)
		}
	})
}
