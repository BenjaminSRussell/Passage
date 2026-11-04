package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

const (
	// CodeCatchWeight is BR-24.5: the actual weight is outside min/max × cases.
	CodeCatchWeight = "SLS-0061"
	// CodeCatchWeightInput is a malformed catch-weight spec, case count or actualWeight report.
	CodeCatchWeightInput = "SLS-0063"
	cwQtyPrecision       = 6
)

var cwColumnLimit = decimal.New(1, 14)

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

// FitsWeightColumn reports whether d has at most six decimals and fits numeric(20,6).
func FitsWeightColumn(d decimal.Decimal) bool {
	return d.Round(cwQtyPrecision).Equal(d) && d.Abs().LessThan(cwColumnLimit)
}

// Validate requires 0 < min ≤ nominal ≤ max with every value storable.
func (s CatchWeight) Validate() error {
	switch {
	case !s.Nominal.IsPositive() || !s.Min.IsPositive() || !s.Max.IsPositive():
		return catchErrorf(CodeCatchWeightInput, "catch-weight nominal, min and max must be greater than zero")
	case s.Min.GreaterThan(s.Max) || s.Nominal.LessThan(s.Min) || s.Nominal.GreaterThan(s.Max):
		return catchErrorf(CodeCatchWeightInput, "catch-weight spec must satisfy min <= nominal <= max")
	case !FitsWeightColumn(s.Nominal) || !FitsWeightColumn(s.Min) || !FitsWeightColumn(s.Max):
		return catchErrorf(CodeCatchWeightInput, "catch-weight values allow at most 6 decimals and 14 integer digits")
	}
	return nil
}

// WholeCases reports whether qty is a whole number of at least 1.
func WholeCases(qty decimal.Decimal) bool {
	return qty.IsInteger() && !qty.LessThan(decimal.NewFromInt(1))
}

// ValidateCatchWeight implements BR-24.5: cases is a whole number of catch-weight
// units (≥ 1) and the weight must satisfy Min × cases ≤ weight ≤ Max × cases.
func ValidateCatchWeight(spec CatchWeight, cases decimal.Decimal, weight decimal.Decimal) error {
	if !WholeCases(cases) {
		return catchErrorf(CodeCatchWeightInput, "catch-weight quantity must be a whole number of at least 1")
	}
	if !weight.IsPositive() || !FitsWeightColumn(weight) {
		return catchErrorf(CodeCatchWeightInput, "actual weight must be greater than zero with at most 6 decimals and 14 integer digits")
	}
	lo, hi := spec.Min.Mul(cases), spec.Max.Mul(cases)
	if weight.LessThan(lo) || weight.GreaterThan(hi) {
		return catchErrorf(CodeCatchWeight, "catch weight %s is outside %s–%s for %s units",
			weight.StringFixed(cwQtyPrecision), lo.StringFixed(cwQtyPrecision), hi.StringFixed(cwQtyPrecision), cases)
	}
	return nil
}

// PricedQty is the weight a line is billed at. A catch-weight line is
// cases × nominal until a delivery is recorded; after that it is the recorded
// weight of the delivered cases plus the undelivered cases at nominal.
func PricedQty(qty, delivered decimal.Decimal, spec *CatchWeight, actual *decimal.Decimal) decimal.Decimal {
	if spec == nil {
		return qty
	}
	if actual == nil {
		return qty.Mul(spec.Nominal)
	}
	return actual.Add(decimal.Max(qty.Sub(delivered), decimal.Zero).Mul(spec.Nominal))
}

// LineNetFor is LineNet of PricedQty (price per weight, FR-24.7).
func LineNetFor(qty, delivered, price, disc decimal.Decimal, spec *CatchWeight, actual *decimal.Decimal) (decimal.Decimal, error) {
	return LineNet(PricedQty(qty, delivered, spec, actual), price, disc)
}
