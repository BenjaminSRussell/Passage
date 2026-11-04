package domain

import (
	"strings"

	"github.com/shopspring/decimal"
)

const (
	// CodeCatchWeight is BR-24.5: the actual weight is outside min/max × cases.
	CodeCatchWeight = "RCV-CW-001"
	// CodeCatchWeightUnit refuses a catch-weight receipt on a line that is not priced per weight unit.
	CodeCatchWeightUnit = "RCV-CW-002"
	qtyPrecision        = 6
)

var weightUnits = []string{"KG", "G", "LB", "OZ", "T"}

var columnLimit = decimal.New(1, 14)

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

// IsWeightUnit reports whether unit is a unit a catch-weight line can be priced and received in.
func IsWeightUnit(unit string) bool {
	for _, u := range weightUnits {
		if strings.EqualFold(u, unit) {
			return true
		}
	}
	return false
}

// FitsColumn reports whether d has at most six decimals and fits numeric(20,6).
func FitsColumn(d decimal.Decimal) bool {
	return d.Round(qtyPrecision).Equal(d) && d.Abs().LessThan(columnLimit)
}

// Validate requires 0 < min ≤ nominal ≤ max with every value storable.
func (s CatchWeight) Validate() error {
	switch {
	case !s.Nominal.IsPositive() || !s.Min.IsPositive() || !s.Max.IsPositive():
		return fail(CodeInvalidReceipt, "catch-weight nominal, min and max must be greater than zero")
	case s.Min.GreaterThan(s.Max) || s.Nominal.LessThan(s.Min) || s.Nominal.GreaterThan(s.Max):
		return fail(CodeInvalidReceipt, "catch-weight spec must satisfy min <= nominal <= max")
	case !FitsColumn(s.Nominal) || !FitsColumn(s.Min) || !FitsColumn(s.Max):
		return fail(CodeInvalidReceipt, "catch-weight values allow at most 6 decimals and 14 integer digits")
	}
	return nil
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
