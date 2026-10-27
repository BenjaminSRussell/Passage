package domain

import (
	"github.com/BenjaminSRussell/passage/platform/ids"
)

// Trigger names a quality order trigger.
const (
	TriggerPurchaseReceipt = "PURCHASE_RECEIPT"
	TriggerProductionRAF   = "PRODUCTION_RAF"
	TriggerSalesPick       = "SALES_PICK"
	TriggerTransferReceipt = "TRANSFER_RECEIPT"
	TriggerQuarantine      = "QUARANTINE"
	TriggerManual          = "MANUAL"
	TriggerSourceInspect   = "SOURCE_INSPECTION"
)

// Scope values for an association dimension.
const (
	ScopeItem      = "ITEM"
	ScopeItemGroup = "ITEM_GROUP"
	ScopeParty     = "PARTY"
	ScopeSite      = "SITE"
	ScopeAll       = "ALL"
)

var validTriggers = map[string]bool{
	TriggerPurchaseReceipt: true, TriggerProductionRAF: true, TriggerSalesPick: true,
	TriggerTransferReceipt: true, TriggerQuarantine: true, TriggerManual: true, TriggerSourceInspect: true,
}

// ValidTrigger reports whether t is a known trigger.
func ValidTrigger(t string) bool { return validTriggers[t] }

// Association says when a trigger event creates a quality order and with which
// test group and sampling (FR-33.3).
type Association struct {
	ID          string       `json:"id"`
	Trigger     string       `json:"trigger"`
	ItemScope   string       `json:"itemScope"`
	ItemRef     string       `json:"itemRef,omitempty"`
	PartyScope  string       `json:"partyScope"`
	PartyRef    string       `json:"partyRef,omitempty"`
	SiteScope   string       `json:"siteScope"`
	SiteRef     string       `json:"siteRef,omitempty"`
	Sequence    int          `json:"sequence"`
	TestGroupID string       `json:"testGroupId"`
	Plan        SamplingPlan `json:"samplingPlan"`
}

// TriggerContext describes one trigger event to match against associations.
type TriggerContext struct {
	Trigger     string
	ItemID      string
	ItemGroupID string
	PartyID     string
	SiteID      string
}

// Score returns the specificity of a for t (BR-33.3):
// 4 for an item match, 3 for an item-group match, 2 for a party match and 1 for
// a site match; "all" scopes match with weight 0. ok is false when a does not match.
func Score(a Association, t TriggerContext) (int, bool) {
	if a.Trigger != t.Trigger {
		return 0, false
	}
	score := 0
	switch a.ItemScope {
	case ScopeItem:
		if a.ItemRef != t.ItemID {
			return 0, false
		}
		score += 4
	case ScopeItemGroup:
		if a.ItemRef != t.ItemGroupID {
			return 0, false
		}
		score += 3
	}
	if a.PartyScope == ScopeParty {
		if a.PartyRef != t.PartyID {
			return 0, false
		}
		score += 2
	}
	if a.SiteScope == ScopeSite {
		if a.SiteRef != t.SiteID {
			return 0, false
		}
		score++
	}
	return score, true
}

// BestAssociation picks the highest-scoring matching association; ties go to
// the lowest sequence. ok is false when nothing matches.
func BestAssociation(assocs []Association, t TriggerContext) (Association, bool) {
	var best Association
	bestScore, found := -1, false
	for _, a := range assocs {
		s, ok := Score(a, t)
		if !ok {
			continue
		}
		if !found || s > bestScore || (s == bestScore && a.Sequence < best.Sequence) {
			best, bestScore, found = a, s, true
		}
	}
	return best, found
}

// ValidateAssociation returns field errors for an association, empty when valid.
func ValidateAssociation(a Association) map[string]string {
	fields := map[string]string{}
	if !ValidTrigger(a.Trigger) || a.Trigger == TriggerManual {
		fields["trigger"] = "must be a non-manual trigger"
	}
	checkScope(fields, "itemScope", a.ItemScope, a.ItemRef, ScopeItem, ScopeItemGroup)
	checkScope(fields, "partyScope", a.PartyScope, a.PartyRef, ScopeParty)
	checkScope(fields, "siteScope", a.SiteScope, a.SiteRef, ScopeSite)
	if !ids.Valid(a.TestGroupID) {
		fields["testGroupId"] = "must be a UUID"
	}
	for k, v := range ValidatePlan("samplingPlan", a.Plan) {
		fields[k] = v
	}
	if a.Sequence < 0 {
		fields["sequence"] = "must not be negative"
	}
	return fields
}

// checkScope validates one dimension: a scope with a reference needs the ref
// to be a UUID; ALL has no reference.
func checkScope(fields map[string]string, name, scope, ref string, refScopes ...string) {
	if scope == ScopeAll {
		if ref != "" {
			fields[name] = "ALL takes no reference"
		}
		return
	}
	for _, s := range refScopes {
		if scope == s {
			if !ids.Valid(ref) {
				fields[name] = "a " + s + " scope needs a UUID reference"
			}
			return
		}
	}
	fields[name] = "unknown scope"
}
