package domain

import (
	"fmt"
	"slices"
	"time"
)

// pegPlan orders the chains of a run so every parent order is placed before
// the orders pegged to it, keeping the order plan sequence otherwise: each step
// takes the first chain whose parents inside the run are all placed. Pegs that
// would close a loop (the service refuses them with SCHED-0121) are ignored
// for the chains left, which are then taken in plan order.
func pegPlan(chains [][]Operation, pegs []Peg) [][]Operation {
	if len(pegs) == 0 {
		return chains
	}
	inRun := map[string]bool{}
	for _, c := range chains {
		inRun[c[0].OrderID] = true
	}
	parents := map[string][]string{}
	for _, p := range pegs {
		if inRun[p.ParentOrderID] && inRun[p.DependentOrderID] && p.ParentOrderID != p.DependentOrderID {
			parents[p.DependentOrderID] = append(parents[p.DependentOrderID], p.ParentOrderID)
		}
	}
	if len(parents) == 0 {
		return chains
	}
	done := map[string]bool{}
	out := make([][]Operation, 0, len(chains))
	left := slices.Clone(chains)
	for len(left) > 0 {
		pick := 0
		for i, c := range left {
			ready := true
			for _, par := range parents[c[0].OrderID] {
				if !done[par] {
					ready = false
					break
				}
			}
			if ready {
				pick = i
				break
			}
		}
		done[left[pick][0].OrderID] = true
		out = append(out, left[pick])
		left = slices.Delete(left, pick, pick+1)
	}
	return out
}

// pegFloor is the earliest the first operation of an order may start under its
// pegs: the latest end among its parent orders, taken from this run's
// placements, the order's frozen jobs and ParentEnds for parents outside the
// run. A parent in unplaced (not placed in this run, or holding an unplaced
// operation outside it) is returned as blocked: the dependent cannot be
// placed either. A parent outside the run with no jobs sets no floor.
func pegFloor(orderID string, pegs []Peg, ends map[string]time.Time, parentEnds map[string]time.Time, unplaced map[string]bool) (floor time.Time, blocked string) {
	for _, p := range pegs {
		if p.DependentOrderID != orderID || p.ParentOrderID == orderID {
			continue
		}
		if unplaced[p.ParentOrderID] {
			return time.Time{}, p.ParentOrderID
		}
		end, ok := ends[p.ParentOrderID]
		if !ok {
			end, ok = parentEnds[p.ParentOrderID]
		}
		if ok && end.After(floor) {
			floor = end
		}
	}
	return floor, ""
}

// pegBlockedReason is the unplaced reason of an order behind an unplaced parent.
func pegBlockedReason(parent string) string {
	return fmt.Sprintf("pegged parent order %s is unplaced", parent)
}
