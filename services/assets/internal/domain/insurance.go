package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Insurance codes from the PRD message catalog (7.37.18): AST-0531 warns that
// the valuation year has no index value and the latest year was used; AST-0532
// warns that an asset is under-insured.
const (
	CodeIndexMissing = "AST-0531"
	CodeUnderInsured = "AST-0532"
)

// CheckCoverage validates a policy period: coverage must end after it starts.
func CheckCoverage(from, to time.Time) error {
	if !to.After(from) {
		return fail(CodeInvalidInsurance, "coverage end must be after coverage start")
	}
	return nil
}

// UnderInsuredRatio is the share of indexed replacement value below which an
// asset is listed as under-insured (FR-37.15).
var UnderInsuredRatio = decimal.RequireFromString("0.9")

// IndexedValue returns base × indexNow ÷ indexBase rounded half-up to the
// currency minor unit (BR-37.10).
func IndexedValue(base, indexNow, indexBase decimal.Decimal, currency string) (decimal.Decimal, error) {
	if indexBase.LessThanOrEqual(decimal.Zero) || indexNow.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero, fail(CodeInvalidInsurance, "index values must be positive")
	}
	return base.Mul(indexNow).Div(indexBase).Round(money.MinorUnits(currency)), nil
}

// IndexValue is the index used for a year and whether it fell back to the
// latest available year (AST-0531 warning).
type IndexValue struct {
	Value    decimal.Decimal
	Year     int
	Fallback bool
}

// IndexFor picks the index for year. A missing year uses the latest loaded year
// and sets Fallback. ok is false when the series is empty.
func IndexFor(series map[int]decimal.Decimal, year int) (IndexValue, bool) {
	if v, ok := series[year]; ok {
		return IndexValue{Value: v, Year: year}, true
	}
	if len(series) == 0 {
		return IndexValue{}, false
	}
	years := make([]int, 0, len(series))
	for y := range series {
		years = append(years, y)
	}
	sort.Ints(years)
	latest := years[len(years)-1]
	return IndexValue{Value: series[latest], Year: latest, Fallback: true}, true
}

// UnderInsured reports whether insured is below 90% of the indexed replacement value.
func UnderInsured(insured, indexed decimal.Decimal, currency string) bool {
	threshold := indexed.Mul(UnderInsuredRatio).Round(money.MinorUnits(currency))
	return insured.LessThan(threshold)
}
