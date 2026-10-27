package domain

import (
	"slices"

	"github.com/shopspring/decimal"
)

// Batch is on-hand or supply of one lot with its expiry as a day index.
type Batch struct {
	ID        string
	Qty       decimal.Decimal
	ExpiryDay int
}

// BatchAllocation is the quantity of one batch pegged to a demand.
type BatchAllocation struct {
	BatchID string
	Qty     decimal.Decimal
}

// Usable reports whether a batch expiring on expiryDay may serve a demand on
// demandDay that needs requiredDays of remaining shelf life (BR-22.15: e - d >= r).
func Usable(expiryDay, demandDay, requiredDays int) bool {
	return expiryDay-demandDay >= requiredDays
}

// ProducedExpiryDay is the expiry assumed for planned production supply:
// delivery day plus the item's shelf life (BR-22.15).
func ProducedExpiryDay(deliveryDay, shelfLifeDays int) int {
	return deliveryDay + shelfLifeDays
}

// AllocateBatches pegs a demand to usable batches first-expiring-first (FEFO),
// ties broken by batch id. It returns the allocations and the shortfall that the
// planning engine must cover with planned supply (FR-22.20). The batches are not
// changed; callers subtract the allocations before pegging the next demand.
func AllocateBatches(batches []Batch, demandQty decimal.Decimal, demandDay, requiredDays int) ([]BatchAllocation, decimal.Decimal) {
	usable := make([]Batch, 0, len(batches))
	for _, b := range batches {
		if b.Qty.IsPositive() && Usable(b.ExpiryDay, demandDay, requiredDays) {
			usable = append(usable, b)
		}
	}
	slices.SortFunc(usable, func(a, b Batch) int {
		if a.ExpiryDay != b.ExpiryDay {
			return a.ExpiryDay - b.ExpiryDay
		}
		return compareStrings(a.ID, b.ID)
	})
	var allocs []BatchAllocation
	rem := demandQty
	for _, b := range usable {
		if !rem.IsPositive() {
			break
		}
		q := decimal.Min(b.Qty, rem)
		allocs = append(allocs, BatchAllocation{BatchID: b.ID, Qty: q})
		rem = rem.Sub(q)
	}
	if rem.IsNegative() {
		rem = decimal.Zero
	}
	return allocs, rem
}

// UnpeggedExpiring returns the batches that expire on or before horizonDay and
// still have quantity after the allocations (PLAN-0011). Input order is kept.
func UnpeggedExpiring(batches []Batch, allocs []BatchAllocation, horizonDay int) []Batch {
	used := map[string]decimal.Decimal{}
	for _, a := range allocs {
		used[a.BatchID] = used[a.BatchID].Add(a.Qty)
	}
	var out []Batch
	for _, b := range batches {
		left := b.Qty.Sub(used[b.ID])
		if left.IsPositive() && b.ExpiryDay <= horizonDay {
			out = append(out, Batch{ID: b.ID, Qty: left, ExpiryDay: b.ExpiryDay})
		}
	}
	return out
}
