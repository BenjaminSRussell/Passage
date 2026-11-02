package domain

import (
	"cmp"
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

// ReservationPolicy selects batches for a policy-based reservation (FR-16.7).
type ReservationPolicy string

const (
	PolicyManual ReservationPolicy = "MANUAL"
	PolicyFIFO   ReservationPolicy = "FIFO"
	PolicyLIFO   ReservationPolicy = "LIFO"
	PolicyFEFO   ReservationPolicy = "FEFO"
)

// BatchStock is the available quantity of one batch at one dimension set.
// A nil Expiry means the batch never expires and sorts last under FEFO. Key
// identifies the dimension set the stock sits in; it is carried through to the
// allocation and is the final tie-break, so selection is deterministic.
type BatchStock struct {
	BatchID     string
	Key         string
	Expiry      *time.Time
	ReceiptDate time.Time
	Available   decimal.Decimal
	Status      string // AVAILABLE, BLOCKED or QUARANTINE
	// Segment is the stock's segment dimension value, "" when unsegmented
	// (FR-16.20). PlanDemandPolicy matches it against the demand.
	Segment string
	// Dims is the stock's dimension set; the candidate inquiry shows it.
	Dims Dimensions
}

// Allocation is the quantity taken from one batch at one dimension set.
type Allocation struct {
	BatchID string
	Key     string
	Qty     decimal.Decimal
}

// Selection is the result of a policy reservation: allocations in pick order and
// any shortfall the candidates could not cover (returned, not an error).
type Selection struct {
	Allocations []Allocation
	Shortfall   decimal.Decimal
}

// SelectBatches allocates qty across eligible batches in policy order.
//
// FEFO orders by expiry ascending, then receipt date ascending, then batch id
// ascending (BR-16.7), and excludes batches that are not AVAILABLE or that expire
// before requiredDate plus marginDays. FIFO orders by receipt date; LIFO by
// receipt date descending. MANUAL is not a policy and is rejected by the caller.
func SelectBatches(policy ReservationPolicy, batches []BatchStock, qty decimal.Decimal, requiredDate time.Time, marginDays int) Selection {
	eligible := make([]BatchStock, 0, len(batches))
	cutoff := requiredDate.AddDate(0, 0, marginDays)
	for _, b := range batches {
		if b.Status != StockAvailable || b.Available.Sign() <= 0 {
			continue
		}
		if policy == PolicyFEFO && b.Expiry != nil && b.Expiry.Before(cutoff) {
			continue
		}
		eligible = append(eligible, b)
	}
	sortByPolicy(policy, eligible)
	var sel Selection
	remaining := qty
	for _, b := range eligible {
		if remaining.Sign() <= 0 {
			break
		}
		take := decimal.Min(b.Available, remaining)
		sel.Allocations = append(sel.Allocations, Allocation{BatchID: b.BatchID, Key: b.Key, Qty: take})
		remaining = remaining.Sub(take)
	}
	if remaining.Sign() > 0 {
		sel.Shortfall = remaining
	}
	return sel
}

// compareExpiry sorts non-nil expiries first in ascending order and nil last.
func compareExpiry(a, b *time.Time) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return 1
	case b == nil:
		return -1
	default:
		return a.Compare(*b)
	}
}

// sortByPolicy puts batches in the pick order of policy.
func sortByPolicy(policy ReservationPolicy, list []BatchStock) {
	switch policy {
	case PolicyFEFO:
		slices.SortStableFunc(list, func(a, b BatchStock) int {
			if c := compareExpiry(a.Expiry, b.Expiry); c != 0 {
				return c
			}
			return cmp.Or(a.ReceiptDate.Compare(b.ReceiptDate), cmp.Compare(a.BatchID, b.BatchID), cmp.Compare(a.Key, b.Key))
		})
	case PolicyLIFO:
		slices.SortStableFunc(list, func(a, b BatchStock) int {
			return cmp.Or(b.ReceiptDate.Compare(a.ReceiptDate), cmp.Compare(a.BatchID, b.BatchID), cmp.Compare(a.Key, b.Key))
		})
	default:
		slices.SortStableFunc(list, func(a, b BatchStock) int {
			return cmp.Or(a.ReceiptDate.Compare(b.ReceiptDate), cmp.Compare(a.BatchID, b.BatchID), cmp.Compare(a.Key, b.Key))
		})
	}
}
