package domain

import (
	"errors"
	"slices"
	"time"

	"github.com/shopspring/decimal"
)

// Quota source types (FR-22.17).
const (
	SourceVendor     = "vendor"
	SourceProduction = "production"
	SourceTransfer   = "transfer"
)

// Quota errors. PLAN-0009 is raised when no source can take the requirement.
var (
	ErrInvalidQuota   = errors.New("planning: invalid quota source or quantity")
	ErrNoUsableSource = errors.New("planning: quota arrangement has no usable source")
)

// QuotaSource is one source of a quota arrangement with its running allocation.
// Blocked sources (for example an expired trade agreement) take no quantity
// unless the arrangement has nothing else (BR-22.12).
type QuotaSource struct {
	ID        string
	Type      string
	Quota     decimal.Decimal // share weight; a source with quota 0 is a fallback only
	BaseQty   decimal.Decimal
	Allocated decimal.Decimal
	Priority  int
	Blocked   bool
}

// QuotaAssignment is the quantity given to one source and the rating it reaches.
type QuotaAssignment struct {
	SourceID    string
	Qty         decimal.Decimal
	RatingAfter decimal.Decimal
}

// Rating is (allocated + base) / quota (BR-22.12). It is only defined for quota > 0.
func Rating(s QuotaSource) decimal.Decimal {
	return s.Allocated.Add(s.BaseQty).Div(s.Quota)
}

// Allocate assigns qty to quota sources.
//
// Without split, the whole requirement goes to the source with the lowest
// rating (ties: lower priority number, then source id). With split, the
// requirement is divided so that the ratings of the receiving sources are
// equal after allocation (water filling); a share below minSplit is dropped and
// the rest recomputed. Sources with quota 0 are used, whole, only when no
// unblocked source with quota > 0 exists.
//
// The returned assignments sum to qty and are sorted by source id.
func Allocate(sources []QuotaSource, qty decimal.Decimal, split bool, minSplit decimal.Decimal) ([]QuotaAssignment, error) {
	if !qty.IsPositive() || minSplit.IsNegative() {
		return nil, ErrInvalidQuota
	}
	for _, s := range sources {
		if s.Quota.IsNegative() || s.BaseQty.IsNegative() || s.Allocated.IsNegative() {
			return nil, ErrInvalidQuota
		}
	}
	eligible := filterSources(sources, func(s QuotaSource) bool { return !s.Blocked && s.Quota.IsPositive() })
	if len(eligible) == 0 {
		return allocateFallback(sources, qty)
	}
	if !split {
		return allocateWhole(eligible, qty), nil
	}
	return allocateSplit(eligible, qty, minSplit)
}

func allocateWhole(eligible []QuotaSource, qty decimal.Decimal) []QuotaAssignment {
	best := eligible[0]
	for _, s := range eligible[1:] {
		if r, b := Rating(s), Rating(best); r.LessThan(b) || (r.Equal(b) && s.Priority < best.Priority) {
			best = s
		}
	}
	return []QuotaAssignment{{SourceID: best.ID, Qty: qty, RatingAfter: ratingWith(best, qty)}}
}

func allocateFallback(sources []QuotaSource, qty decimal.Decimal) ([]QuotaAssignment, error) {
	zero := filterSources(sources, func(s QuotaSource) bool { return !s.Blocked && s.Quota.IsZero() })
	if len(zero) == 0 {
		return nil, ErrNoUsableSource
	}
	slices.SortFunc(zero, func(a, b QuotaSource) int {
		if a.Priority != b.Priority {
			return a.Priority - b.Priority
		}
		return compareStrings(a.ID, b.ID)
	})
	return []QuotaAssignment{{SourceID: zero[0].ID, Qty: qty, RatingAfter: decimal.Zero}}, nil
}

// allocateSplit water-fills qty over the eligible sources.
func allocateSplit(eligible []QuotaSource, qty, minSplit decimal.Decimal) ([]QuotaAssignment, error) {
	active := slices.Clone(eligible)
	var amounts []decimal.Decimal
	for {
		if len(active) == 0 {
			return nil, ErrNoUsableSource
		}
		sumQ, sumA := decimal.Zero, decimal.Zero
		for _, s := range active {
			sumQ = sumQ.Add(s.Quota)
			sumA = sumA.Add(s.Allocated).Add(s.BaseQty)
		}
		level := qty.Add(sumA).Div(sumQ)

		// A source already above the water level takes nothing and is removed.
		if above := indexWhere(active, func(s QuotaSource) bool {
			return level.Mul(s.Quota).LessThan(s.Allocated.Add(s.BaseQty))
		}); above >= 0 {
			active = slices.Delete(active, above, above+1)
			continue
		}
		amounts = amounts[:0]
		for _, s := range active {
			amounts = append(amounts, level.Mul(s.Quota).Sub(s.Allocated).Sub(s.BaseQty))
		}
		// A share below the minimum split is not worth a line: drop the smallest
		// such and retry. The last source always stays, so a requirement smaller
		// than the minimum split still goes, whole, to one source.
		if small := indexSmallestBelow(amounts, minSplit); small >= 0 && len(active) > 1 {
			active = slices.Delete(active, small, small+1)
			continue
		}
		break
	}
	return roundedSplit(active, amounts, qty), nil
}

// roundedSplit rounds shares to six places and gives the rounding remainder to
// the largest share, so the assignments sum exactly to qty.
func roundedSplit(active []QuotaSource, amounts []decimal.Decimal, qty decimal.Decimal) []QuotaAssignment {
	sum := decimal.Zero
	largest := 0
	for i := range amounts {
		amounts[i] = amounts[i].Round(6)
		sum = sum.Add(amounts[i])
		if amounts[i].GreaterThan(amounts[largest]) || (amounts[i].Equal(amounts[largest]) && active[i].ID < active[largest].ID) {
			largest = i
		}
	}
	amounts[largest] = amounts[largest].Add(qty.Sub(sum))

	out := make([]QuotaAssignment, 0, len(active))
	for i, s := range active {
		if !amounts[i].IsPositive() {
			continue
		}
		out = append(out, QuotaAssignment{SourceID: s.ID, Qty: amounts[i], RatingAfter: ratingWith(s, amounts[i])})
	}
	slices.SortFunc(out, func(a, b QuotaAssignment) int { return compareStrings(a.SourceID, b.SourceID) })
	return out
}

func ratingWith(s QuotaSource, qty decimal.Decimal) decimal.Decimal {
	return s.Allocated.Add(s.BaseQty).Add(qty).Div(s.Quota)
}

// ArrangementsOverlap reports whether two validity periods share a day. Both
// ends are inclusive. Arrangements for one item and site must not overlap (4.22.4 invariant).
func ArrangementsOverlap(aFrom, aTo, bFrom, bTo time.Time) bool {
	return !aFrom.After(bTo) && !bFrom.After(aTo)
}

func filterSources(in []QuotaSource, keep func(QuotaSource) bool) []QuotaSource {
	out := make([]QuotaSource, 0, len(in))
	for _, s := range in {
		if keep(s) {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b QuotaSource) int { return compareStrings(a.ID, b.ID) })
	return out
}

func indexWhere(in []QuotaSource, pred func(QuotaSource) bool) int {
	for i, s := range in {
		if pred(s) {
			return i
		}
	}
	return -1
}

// indexSmallestBelow returns the index of the smallest positive amount below limit, or -1.
func indexSmallestBelow(amounts []decimal.Decimal, limit decimal.Decimal) int {
	best := -1
	for i, a := range amounts {
		if a.IsPositive() && a.LessThan(limit) && (best < 0 || a.LessThan(amounts[best])) {
			best = i
		}
	}
	return best
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
