package domain

import "github.com/shopspring/decimal"

// CatchWeight is the per-unit weight specification of a catch-weight item.
type CatchWeight struct {
	Nominal decimal.Decimal
	Min     decimal.Decimal
	Max     decimal.Decimal
}

// ValidateCatchWeight implements BR-24.5: cwQty is a whole number of catch-weight
// units (≥ 1) and the weight must satisfy Min × cwQty ≤ weight ≤ Max × cwQty.
func ValidateCatchWeight(spec CatchWeight, cwQty int, weight decimal.Decimal) error {
	if cwQty < 1 {
		return errorf(CodeInvalid, "catch-weight quantity must be a whole number of at least 1")
	}
	if weight.IsNegative() {
		return errorf(CodeInvalid, "weight must not be negative")
	}
	n := decimal.NewFromInt(int64(cwQty))
	lo, hi := spec.Min.Mul(n), spec.Max.Mul(n)
	if weight.LessThan(lo) || weight.GreaterThan(hi) {
		return errorf(CodeCatchWeight, "catch weight %s is outside %s–%s for %d units",
			weight.StringFixed(QtyPrecision), lo.StringFixed(QtyPrecision), hi.StringFixed(QtyPrecision), cwQty)
	}
	return nil
}
