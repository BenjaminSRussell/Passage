package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

const (
	// CodeCatchWeight is BR-24.5: the actual weight is outside min/max × cases.
	CodeCatchWeight = "SLS-0061"
	cwQtyPrecision  = 6
)

// CatchWeight is the per-unit weight specification of a catch-weight item.
type CatchWeight struct {
	Nominal decimal.Decimal `json:"nominal"`
	Min     decimal.Decimal `json:"min"`
	Max     decimal.Decimal `json:"max"`
}

// Set reports whether spec names a catch-weight item.
func (s CatchWeight) Set() bool {
	return !s.Nominal.IsZero() || !s.Min.IsZero() || !s.Max.IsZero()
}

// CatchWeightError is a BR-24.5 (or malformed spec) refusal.
type CatchWeightError struct {
	Code   string
	Detail string
}

func (e *CatchWeightError) Error() string { return e.Code + ": " + e.Detail }

func catchErrorf(code, format string, args ...any) error {
	return &CatchWeightError{Code: code, Detail: fmt.Sprintf(format, args...)}
}

// ValidateCatchWeight implements BR-24.5: cwQty is a whole number of catch-weight
// units (≥ 1) and the weight must satisfy Min × cwQty ≤ weight ≤ Max × cwQty.
func ValidateCatchWeight(spec CatchWeight, cwQty int, weight decimal.Decimal) error {
	if cwQty < 1 {
		return catchErrorf("COM-0001", "catch-weight quantity must be a whole number of at least 1")
	}
	if weight.IsNegative() {
		return catchErrorf("COM-0001", "weight must not be negative")
	}
	n := decimal.NewFromInt(int64(cwQty))
	lo, hi := spec.Min.Mul(n), spec.Max.Mul(n)
	if weight.LessThan(lo) || weight.GreaterThan(hi) {
		return catchErrorf(CodeCatchWeight, "catch weight %s is outside %s–%s for %d units",
			weight.StringFixed(cwQtyPrecision), lo.StringFixed(cwQtyPrecision), hi.StringFixed(cwQtyPrecision), cwQty)
	}
	return nil
}

// PricedQty is the weight a line is billed at: the recorded actual weight once
// known, otherwise cases × nominal for a catch-weight item, otherwise qty.
func PricedQty(qty decimal.Decimal, spec *CatchWeight, actual *decimal.Decimal) decimal.Decimal {
	if actual != nil {
		return *actual
	}
	if spec != nil {
		return qty.Mul(spec.Nominal)
	}
	return qty
}

// LineNetFor is LineNet of PricedQty (price per weight, FR-24.7).
func LineNetFor(qty, price, disc decimal.Decimal, spec *CatchWeight, actual *decimal.Decimal) (decimal.Decimal, error) {
	return LineNet(PricedQty(qty, spec, actual), price, disc)
}
