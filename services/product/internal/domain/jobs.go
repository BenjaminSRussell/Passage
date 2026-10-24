package domain

import "github.com/shopspring/decimal"

// OnHandLookup is the wave-16 callback. Before that wave it returns zero (FR-8.16).
type OnHandLookup func(releasedProductID string) decimal.Decimal

// ReferenceLookup is a later-wave "has open documents" callback. Missing callbacks mean no references.
type ReferenceLookup func(releasedProductID string) int

// RunGroupChange applies a model/dimension group change, skipping items with on-hand (BR-8.15, AC-8.12).
func RunGroupChange(items []GroupChangeItem, onHand OnHandLookup) []JobItemResult {
	out := make([]JobItemResult, 0, len(items))
	for _, item := range items {
		qty := item.OnHand
		if onHand != nil {
			qty = onHand(item.ReleasedProductID)
		}
		if qty.GreaterThan(decimal.Zero) {
			out = append(out, JobItemResult{ID: item.ReleasedProductID, Status: "skipped", Code: CodeGrpChgOnHand})
			continue
		}
		out = append(out, JobItemResult{ID: item.ReleasedProductID, Status: "changed"})
	}
	return out
}

// RunObsolete marks items obsolete, or deletes a released product when every callback reports zero references (FR-8.15).
func RunObsolete(ids []string, deleteRequested bool, refs ...ReferenceLookup) []JobItemResult {
	out := make([]JobItemResult, 0, len(ids))
	for _, id := range ids {
		n := 0
		for _, fn := range refs {
			if fn != nil {
				n += fn(id)
			}
		}
		if deleteRequested && n == 0 {
			out = append(out, JobItemResult{ID: id, Status: "deleted"})
			continue
		}
		out = append(out, JobItemResult{ID: id, Status: "obsolete"})
	}
	return out
}
