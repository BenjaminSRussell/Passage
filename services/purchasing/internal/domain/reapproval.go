package domain

import "github.com/shopspring/decimal"

// Reapproval rule kinds (FR-17.8). A confirmed PO change that trips any rule
// returns the PO to IN_REVIEW; a change that trips none is a new version only.
const (
	RuleAnyChange          = "ANY_CHANGE"
	RuleQtyIncreasePct     = "QTY_INCREASE_PCT"
	RulePriceIncreasePct   = "PRICE_INCREASE_PCT"
	RuleNewLine            = "NEW_LINE"
	RuleDeliveryDateChange = "DELIVERY_DATE_CHANGE"
)

const pctDivPrecision = 12

// ReapprovalRule is one rule of the policy's reapproval_rules set.
type ReapprovalRule struct {
	Kind      string
	Threshold decimal.Decimal
}

// LineState is the part of a PO line that change management compares.
type LineState struct {
	LineNo       int
	Qty          decimal.Decimal
	UnitPrice    decimal.Decimal
	DeliveryDate Day
}

// DefaultReapprovalRules is the FR-17.8 default: quantity or price up by more
// than 5 % and any new line need approval. Delivery date changes do not.
func DefaultReapprovalRules() []ReapprovalRule {
	five := decimal.NewFromInt(5)
	return []ReapprovalRule{
		{Kind: RuleQtyIncreasePct, Threshold: five},
		{Kind: RulePriceIncreasePct, Threshold: five},
		{Kind: RuleNewLine},
	}
}

// ReapprovalRequired reports whether any rule fires for the change from before
// to after. Lines are matched by line number; a line missing from after counts
// as a change only under ANY_CHANGE.
func ReapprovalRequired(before, after []LineState, rules []ReapprovalRule) bool {
	has := func(kind string) bool {
		for _, r := range rules {
			if r.Kind == kind {
				return true
			}
		}
		return false
	}
	byNo := make(map[int]LineState, len(before))
	for _, b := range before {
		byNo[b.LineNo] = b
	}
	seen := make(map[int]bool, len(after))
	for _, a := range after {
		seen[a.LineNo] = true
		b, existed := byNo[a.LineNo]
		if !existed {
			if has(RuleNewLine) || has(RuleAnyChange) {
				return true
			}
			continue
		}
		if has(RuleAnyChange) && (!a.Qty.Equal(b.Qty) || !a.UnitPrice.Equal(b.UnitPrice) || !a.DeliveryDate.Equal(b.DeliveryDate.Time)) {
			return true
		}
		if has(RuleDeliveryDateChange) && !a.DeliveryDate.Equal(b.DeliveryDate.Time) {
			return true
		}
		for _, r := range rules {
			switch r.Kind {
			case RuleQtyIncreasePct:
				if increaseExceeds(b.Qty, a.Qty, r.Threshold) {
					return true
				}
			case RulePriceIncreasePct:
				if increaseExceeds(b.UnitPrice, a.UnitPrice, r.Threshold) {
					return true
				}
			}
		}
	}
	for _, b := range before {
		if !seen[b.LineNo] && has(RuleAnyChange) {
			return true
		}
	}
	return false
}

// increaseExceeds reports whether to is more than threshold percent above from.
// An increase from zero exceeds any threshold.
func increaseExceeds(from, to, threshold decimal.Decimal) bool {
	if to.LessThanOrEqual(from) {
		return false
	}
	if from.IsZero() {
		return true
	}
	return to.Sub(from).Mul(decimal.NewFromInt(100)).DivRound(from, pctDivPrecision).GreaterThan(threshold)
}
