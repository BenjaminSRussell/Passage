package domain

import (
	"sort"

	"github.com/shopspring/decimal"
)

// ArrearsPolicy decides what happens to a deduction that net pay cannot cover.
type ArrearsPolicy string

const (
	PolicyArrears   ArrearsPolicy = "ARREARS"
	PolicyNoArrears ArrearsPolicy = "NO_ARREARS"
	PolicyPartial   ArrearsPolicy = "PARTIAL"
)

// Claim is a post-tax deduction competing for the net pay of one period.
type Claim struct {
	Code     string
	Priority int // lower is taken first
	Due      decimal.Decimal
	Policy   ArrearsPolicy
}

// Allocation is what one claim takes and what it leaves in the arrears ledger.
type Allocation struct {
	Code    string
	Taken   decimal.Decimal
	Arrears decimal.Decimal
}

// AllocateDeductions takes claims in ascending priority from the available net
// pay (BR-43.10). A claim that fits is taken in full. A claim that does not fit
// is skipped (NO_ARREARS), added whole to arrears (ARREARS) or taken up to the
// remaining net with the rest added to arrears (PARTIAL).
func AllocateDeductions(available decimal.Decimal, claims []Claim) []Allocation {
	ordered := make([]Claim, len(claims))
	copy(ordered, claims)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Priority < ordered[j].Priority })

	remaining := decimal.Max(decimal.Zero, available)
	out := make([]Allocation, 0, len(ordered))
	for _, c := range ordered {
		a := Allocation{Code: c.Code}
		switch {
		case !c.Due.GreaterThan(decimal.Zero):
			// nothing due
		case c.Due.LessThanOrEqual(remaining):
			a.Taken = c.Due
		case c.Policy == PolicyPartial:
			a.Taken = remaining
			a.Arrears = c.Due.Sub(remaining)
		case c.Policy == PolicyArrears:
			a.Arrears = c.Due
		}
		remaining = remaining.Sub(a.Taken)
		out = append(out, a)
	}
	return out
}

// RecoveryDue is the arrears recovered this period: the balance, capped at
// limitPct (a fraction, 1 = 100%) of the normal per-period deduction.
func RecoveryDue(normal, balance, limitPct decimal.Decimal) decimal.Decimal {
	ceiling := decimal.Max(decimal.Zero, normal.Mul(limitPct))
	return decimal.Max(decimal.Zero, decimal.Min(balance, ceiling))
}
