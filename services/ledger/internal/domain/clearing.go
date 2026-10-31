package domain

import (
	"regexp"

	"github.com/shopspring/decimal"
)

// Clearing limits and codes (FR-11.19, BR-11.12, BR-11.13).
const (
	MinClearingItems = 2
	MaxClearingItems = 10000
	// MaxResidualDepth is the longest residual_of_item_id chain (BR-11.13).
	MaxResidualDepth = 20
)

var toleranceCodeRe = regexp.MustCompile(`^[A-Z0-9_]{1,10}$`)

// ValidToleranceCode reports whether code is a tolerance group code.
func ValidToleranceCode(code string) bool { return toleranceCodeRe.MatchString(code) }

// Tolerance is the write-off limit of a tolerance group (BR-11.12): Debit caps a
// debit residual (debits exceed credits), Credit caps a credit residual, and
// Percent, when above zero, also caps the residual at that share of the
// selection's absolute total Σ|amount|.
type Tolerance struct {
	Debit   decimal.Decimal
	Credit  decimal.Decimal
	Percent decimal.Decimal
}

// ClearingTotals returns the debit sum, the credit sum and their difference
// (debit minus credit) of signed item amounts (debit positive).
func ClearingTotals(amounts []decimal.Decimal) (debit, credit, net decimal.Decimal) {
	debit, credit = decimal.Zero, decimal.Zero
	for _, a := range amounts {
		if a.IsNegative() {
			credit = credit.Add(a.Neg())
		} else {
			debit = debit.Add(a)
		}
	}
	return debit, credit, debit.Sub(credit)
}

// WithinTolerance reports whether a residual of net may be written off: it must
// not exceed the limit of its side and, when a percentage limit is set, not
// exceed that share of Σ|amount| (debit plus credit). A zero net is always within.
func WithinTolerance(net, debit, credit decimal.Decimal, t Tolerance) bool {
	r := net.Abs()
	if r.IsZero() {
		return true
	}
	limit := t.Debit
	if net.IsNegative() {
		limit = t.Credit
	}
	if r.GreaterThan(limit) {
		return false
	}
	if t.Percent.IsPositive() && r.GreaterThan(debit.Add(credit).Mul(t.Percent).Div(decimal.NewFromInt(100))) {
		return false
	}
	return true
}

// ClearingOutcome says how a selection with residual net clears.
const (
	OutcomeBalanced   = "balanced"   // net is zero
	OutcomeDifference = "difference" // residual within tolerance, written to the difference account
	OutcomeResidual   = "residual"   // residual outside tolerance, carried as a new open item
	OutcomeUnbalanced = "unbalanced" // residual outside tolerance and no residual item requested
)

// ClearingOutcomeOf decides the outcome. tolerance is nil when no group is named.
func ClearingOutcomeOf(net, debit, credit decimal.Decimal, tolerance *Tolerance, residualItem bool) string {
	switch {
	case net.IsZero():
		return OutcomeBalanced
	case tolerance != nil && WithinTolerance(net, debit, credit, *tolerance):
		return OutcomeDifference
	case residualItem:
		return OutcomeResidual
	default:
		return OutcomeUnbalanced
	}
}
