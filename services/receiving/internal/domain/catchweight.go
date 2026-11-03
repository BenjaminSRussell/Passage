package domain

import "github.com/shopspring/decimal"

const (
	// CodeCatchWeight is BR-24.5: the actual weight is outside min/max × cases.
	CodeCatchWeight = "RCV-CW-001"
	qtyPrecision    = 6
)

// CatchWeight is the per-unit weight specification of a catch-weight item.
type CatchWeight struct {
	Nominal decimal.Decimal
	Min     decimal.Decimal
	Max     decimal.Decimal
}

// Set reports whether spec names a catch-weight item.
func (s CatchWeight) Set() bool {
	return !s.Nominal.IsZero() || !s.Min.IsZero() || !s.Max.IsZero()
}

// ValidateCatchWeight implements BR-24.5: cwQty is a whole number of catch-weight
// units (≥ 1) and the weight must satisfy Min × cwQty ≤ weight ≤ Max × cwQty.
func ValidateCatchWeight(spec CatchWeight, cwQty int, weight decimal.Decimal) error {
	if cwQty < 1 {
		return fail(CodeInvalidReceipt, "catch-weight quantity must be a whole number of at least 1")
	}
	if weight.IsNegative() {
		return fail(CodeInvalidReceipt, "weight must not be negative")
	}
	n := decimal.NewFromInt(int64(cwQty))
	lo, hi := spec.Min.Mul(n), spec.Max.Mul(n)
	if weight.LessThan(lo) || weight.GreaterThan(hi) {
		return fail(CodeCatchWeight, "catch weight %s is outside %s–%s for %d units",
			weight.StringFixed(qtyPrecision), lo.StringFixed(qtyPrecision), hi.StringFixed(qtyPrecision), cwQty)
	}
	return nil
}
