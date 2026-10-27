package domain

import "github.com/shopspring/decimal"

// ReconStatus is the classification of one reconciliation item (FR-40.6).
type ReconStatus string

const (
	ReconMatched  ReconStatus = "Matched"
	ReconTiming   ReconStatus = "Timing"
	ReconMismatch ReconStatus = "Mismatch"
)

// DefaultFXTolerancePct is the default `ic.fxTolerancePct` (0.5 % of the amount).
var DefaultFXTolerancePct = decimal.New(5, -1)

// MaxFXTolerancePct is the upper bound of `ic.fxTolerancePct` (0–5 %).
var MaxFXTolerancePct = decimal.NewFromInt(5)

// CentTolerance is the absolute tolerance for a Matched item (0.01).
var CentTolerance = decimal.New(1, -2)

// ReconInput is one IC pair's balances for an entity pair, period and
// currency. AmountA is the due-from side booked in entity A in the period and
// AmountB the due-to side booked in B. NextPeriodA and NextPeriodB are what
// each side booked for the same pair in the next period (zero when nothing):
// a counterpart there makes a difference a Timing item (BR-40.5). Anything
// booked later than the next period is not a counterpart, so the item stays a
// Mismatch.
type ReconInput struct {
	AmountA     decimal.Decimal
	AmountB     decimal.Decimal
	NextPeriodA decimal.Decimal
	NextPeriodB decimal.Decimal
	// FXApplies marks items whose two sides were converted from different
	// accounting currencies (BR-40.2); only these may use the FX tolerance.
	FXApplies bool
	// FXTolerancePct is the tolerance as a percentage of the larger side.
	FXTolerancePct decimal.Decimal
}

// ReconResult is the classification and the in-period difference A − B.
type ReconResult struct {
	Status           ReconStatus
	Difference       decimal.Decimal
	ToleranceApplied bool
}

// ValidateFXTolerance checks `ic.fxTolerancePct` is within 0–5 %.
func ValidateFXTolerance(pct decimal.Decimal) error {
	if pct.IsNegative() || pct.GreaterThan(MaxFXTolerancePct) {
		return invalid("FX tolerance must be between 0 and 5 percent")
	}
	return nil
}

// Classify applies FR-40.6 and BR-40.5. An item is Matched when the
// in-period difference is within 0.01 or, for FX items, within the
// percentage tolerance; Timing when it is not, but the next period's
// counterpart bookings close it within the same tolerance; otherwise
// Mismatch.
func Classify(in ReconInput) ReconResult {
	res := ReconResult{Difference: in.AmountA.Sub(in.AmountB)}
	if ok, fx := in.within(in.AmountA, in.AmountB); ok {
		res.Status, res.ToleranceApplied = ReconMatched, fx
		return res
	}
	if !in.NextPeriodA.IsZero() || !in.NextPeriodB.IsZero() {
		if ok, fx := in.within(in.AmountA.Add(in.NextPeriodA), in.AmountB.Add(in.NextPeriodB)); ok {
			res.Status, res.ToleranceApplied = ReconTiming, fx
			return res
		}
	}
	res.Status = ReconMismatch
	return res
}

// within reports whether a and b agree within a cent, or within the FX
// tolerance for FX items; fx is true when only the FX tolerance made them agree.
func (in ReconInput) within(a, b decimal.Decimal) (ok, fx bool) {
	abs := a.Sub(b).Abs()
	if abs.LessThanOrEqual(CentTolerance) {
		return true, false
	}
	if !in.FXApplies {
		return false, false
	}
	base := decimal.Max(a.Abs(), b.Abs())
	tolerance := base.Mul(in.FXTolerancePct).Div(decimal.NewFromInt(100))
	if abs.LessThanOrEqual(tolerance) {
		return true, true
	}
	return false, false
}
