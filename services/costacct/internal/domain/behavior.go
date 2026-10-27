package domain

// Cost behavior classes (FR-38.16).
const (
	BehaviorFixed        = "Fixed"
	BehaviorVariable     = "Variable"
	BehaviorUnclassified = "Unclassified"
)

// BehaviorRule classifies one cost element for cost objects of one kind
// (empty kind = every kind) from EffectiveFrom (YYYY-MM-DD) onward.
type BehaviorRule struct {
	ElementID     string
	ObjectKind    string
	Behavior      string
	EffectiveFrom string
}

// ValidBehavior reports whether s can be stored as a behavior. Rules and
// element defaults never assign Unclassified; that is the fallback only.
func ValidBehavior(s string) bool {
	return s == BehaviorFixed || s == BehaviorVariable
}

// ClassifyBehavior applies the FR-38.16 order to one entry of a period
// (YYYY-MM): an explicit rule, then the element default, then Unclassified.
// A rule applies when it names the element, names the object's kind or no
// kind, and is effective on or before the first day of the period, so a rule
// dated mid-month applies from the next period. A rule for the object's kind
// overrides a rule for any kind; among rules of the same kind the latest
// effective date wins.
func ClassifyBehavior(rules []BehaviorRule, elementID, elementDefault, objectKind, period string) string {
	start := period + "-01"
	best := -1
	for i, r := range rules {
		if r.ElementID != elementID || r.EffectiveFrom > start {
			continue
		}
		if r.ObjectKind != "" && r.ObjectKind != objectKind {
			continue
		}
		if best < 0 || moreSpecific(r, rules[best]) {
			best = i
		}
	}
	if best >= 0 {
		return rules[best].Behavior
	}
	if ValidBehavior(elementDefault) {
		return elementDefault
	}
	return BehaviorUnclassified
}

// moreSpecific reports whether a beats b: a kind-specific rule beats a rule for
// any kind, then the later effective date wins.
func moreSpecific(a, b BehaviorRule) bool {
	if (a.ObjectKind != "") != (b.ObjectKind != "") {
		return a.ObjectKind != ""
	}
	return a.EffectiveFrom > b.EffectiveFrom
}
