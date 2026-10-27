package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// CodeSameBatch is the same-batch shortfall: no single batch can supply the line
// (FR-24.19). The HTTP layer answers 409 (the PRD's SAME_BATCH_UNAVAILABLE).
const CodeSameBatch = "PMF-0008"

// CodeRequirementNotMet is the PMF-0009 warning: a batch does not meet the
// customer's requirements and is left out of the proposal.
const CodeRequirementNotMet = "PMF-0009"

// DispositionAvailable is the only disposition a reservation proposal may use
// (FR-24.5, AC-24.10).
const DispositionAvailable = "available"

// Scope levels of a CustomerBatchRequirement. The most specific level wins
// (BR-24.13): customer over customer group over item default.
const (
	ScopeItemDefault = 1
	ScopeGroup       = 2
	ScopeCustomer    = 3
)

// MaxSellableDays bounds sellable days (PRD 4.24.13: 0–3,650).
const MaxSellableDays = 3650

// Requirement is one CustomerBatchRequirement row that applies to the line
// (FR-24.19). A row sets any of: sellable days (minimum remaining shelf life at
// shipment), a numeric range for one attribute, and the same-batch flag. Unset
// fields are nil and leave the decision to a less specific row.
type Requirement struct {
	Level        int
	SellableDays *int
	Attribute    string
	Min          *decimal.Decimal
	Max          *decimal.Decimal
	SameBatch    *bool
}

// Range is a numeric attribute range; a nil bound is open.
type Range struct {
	Min *decimal.Decimal
	Max *decimal.Decimal
}

// Resolved is the requirement that applies to one customer line after
// precedence (BR-24.13).
type Resolved struct {
	SellableDays int
	Ranges       map[string]Range
	SameBatch    bool
}

// Resolve applies BR-24.13 field by field: for sellable days, the same-batch
// flag and each attribute range, the row with the highest scope level that sets
// it wins; between rows of the same level the first wins.
func Resolve(rows []Requirement) (Resolved, error) {
	out := Resolved{Ranges: map[string]Range{}}
	daysLevel, sameLevel := 0, 0
	rangeLevel := map[string]int{}
	for i, r := range rows {
		if r.Level < ScopeItemDefault || r.Level > ScopeCustomer {
			return Resolved{}, errorf(CodeInvalid, "requirement %d: level must be 1 (item), 2 (customer group) or 3 (customer)", i)
		}
		if r.SellableDays != nil {
			if *r.SellableDays < 0 || *r.SellableDays > MaxSellableDays {
				return Resolved{}, errorf(CodeInvalid, "requirement %d: sellable days must be 0 to %d", i, MaxSellableDays)
			}
			if r.Level > daysLevel {
				out.SellableDays, daysLevel = *r.SellableDays, r.Level
			}
		}
		if r.SameBatch != nil && r.Level > sameLevel {
			out.SameBatch, sameLevel = *r.SameBatch, r.Level
		}
		if r.Attribute == "" {
			if r.Min != nil || r.Max != nil {
				return Resolved{}, errorf(CodeInvalid, "requirement %d: a range needs an attribute", i)
			}
			continue
		}
		if r.Min == nil && r.Max == nil {
			return Resolved{}, errorf(CodeInvalid, "requirement %d: attribute %s needs a min or a max", i, r.Attribute)
		}
		if r.Min != nil && r.Max != nil && r.Min.GreaterThan(*r.Max) {
			return Resolved{}, errorf(CodeInvalid, "requirement %d: min is above max", i)
		}
		if r.Level > rangeLevel[r.Attribute] {
			out.Ranges[r.Attribute], rangeLevel[r.Attribute] = Range{Min: r.Min, Max: r.Max}, r.Level
		}
	}
	return out, nil
}

// SellableOn implements BR-24.13: a batch qualifies for a shipment on day d if
// expiry − d is at least sellableDays whole days. Both dates are taken at UTC
// midnight so that times of day do not matter.
func SellableOn(expiry, shipDate time.Time, sellableDays int) bool {
	return wholeDays(shipDate, expiry) >= sellableDays
}

// wholeDays returns the number of whole calendar days from a to b (b − a).
func wholeDays(a, b time.Time) int {
	return int(civil(b).Sub(civil(a)) / (24 * time.Hour))
}

// Check reports why a batch does not meet the resolved requirement for a
// shipment on shipDate (FR-24.19, BR-24.13), or "" when it qualifies. The
// batch must be available, not expired on shipDate, keep at least the sellable
// days of shelf life (which needs a recorded expiry when sellable days are
// set), and carry a value inside every attribute range.
func Check(b Batch, req Resolved, shipDate time.Time) string {
	if b.Disposition != DispositionAvailable {
		return fmt.Sprintf("disposition is %s", b.Disposition)
	}
	exp, hasExpiry := b.Values["expiry"]
	hasExpiry = hasExpiry && exp.Date != nil
	switch {
	case hasExpiry && !SellableOn(*exp.Date, shipDate, req.SellableDays):
		if req.SellableDays == 0 {
			return "expired before the ship date"
		}
		return fmt.Sprintf("%d days of shelf life left at shipment, %d required", wholeDays(shipDate, *exp.Date), req.SellableDays)
	case !hasExpiry && req.SellableDays > 0:
		return "no expiry recorded"
	}
	names := make([]string, 0, len(req.Ranges))
	for n := range req.Ranges {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		rg := req.Ranges[n]
		v, ok := b.Values[n]
		if !ok || v.Num == nil {
			return fmt.Sprintf("no numeric value for %s", n)
		}
		if (rg.Min != nil && v.Num.LessThan(*rg.Min)) || (rg.Max != nil && v.Num.GreaterThan(*rg.Max)) {
			return fmt.Sprintf("%s %s is outside the required range", n, v.Num.String())
		}
	}
	return ""
}

// SelectSameBatch implements the same-batch rule (FR-24.19): one batch must
// supply the whole need. Among the batches large enough it picks the one that
// expires first (FEFO; batches without expiry last, then input order).
// Otherwise it returns PMF-0008 naming the largest available quantity.
func SelectSameBatch(candidates []Batch, need decimal.Decimal) (Batch, error) {
	if !need.IsPositive() {
		return Batch{}, errorf(CodeInvalid, "the line quantity must be positive")
	}
	largest := decimal.Zero
	found := -1
	for i, b := range candidates {
		if b.Qty.GreaterThan(largest) {
			largest = b.Qty
		}
		if b.Qty.LessThan(need) {
			continue
		}
		if found < 0 || expiresBefore(b, candidates[found]) {
			found = i
		}
	}
	if found < 0 {
		return Batch{}, errorf(CodeSameBatch, "no single batch can supply %s (largest %s)", need, largest)
	}
	return candidates[found], nil
}

// expiresBefore reports whether a expires strictly before b; a batch without
// an expiry never expires before one with.
func expiresBefore(a, b Batch) bool {
	ea, oka := a.Values["expiry"]
	eb, okb := b.Values["expiry"]
	switch {
	case !oka || ea.Date == nil:
		return false
	case !okb || eb.Date == nil:
		return true
	}
	return ea.Date.Before(*eb.Date)
}
