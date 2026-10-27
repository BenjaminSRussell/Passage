package domain

import (
	"errors"

	"github.com/shopspring/decimal"
)

// Forecast reduction principles (FR-22.8, BR-22.3).
const (
	ReductionNone                = "none"
	ReductionPercent             = "percent"
	ReductionTransactions        = "transactions"
	ReductionTransactionsDynamic = "transactions-dynamic"
)

// ErrInvalidReduction is returned for an unknown principle or a percentage outside 0..100.
var ErrInvalidReduction = errors.New("planning: invalid forecast reduction")

var hundred = decimal.NewFromInt(100)

// ForecastCell is one forecast period of one item at one site. Cells of a
// series are passed in period order.
type ForecastCell struct {
	Qty      decimal.Decimal
	SalesQty decimal.Decimal
}

// ReducePeriods returns the reduced forecast per period of one series.
//
//   - none: the forecast is kept as entered.
//   - percent: each period keeps (100 - percent)% of its forecast, rounded to
//     6 decimals; sales are ignored.
//   - transactions: reduced = max(0, forecast - sales in the period) (BR-22.3).
//   - transactions-dynamic: sales in excess of a period's forecast carry into the
//     next period, so they consume later forecast too.
//
// Reduced quantities are never negative and never exceed the forecast.
func ReducePeriods(principle string, percent decimal.Decimal, cells []ForecastCell) ([]decimal.Decimal, error) {
	out := make([]decimal.Decimal, len(cells))
	switch principle {
	case ReductionNone:
		for i, c := range cells {
			out[i] = c.Qty
		}
	case ReductionPercent:
		if percent.IsNegative() || percent.GreaterThan(hundred) {
			return nil, ErrInvalidReduction
		}
		keep := hundred.Sub(percent).Div(hundred)
		for i, c := range cells {
			// Quantities carry 6 decimals (BR-22.10).
			out[i] = c.Qty.Mul(keep).Round(6)
		}
	case ReductionTransactions:
		for i, c := range cells {
			out[i] = decimal.Max(c.Qty.Sub(c.SalesQty), decimal.Zero)
		}
	case ReductionTransactionsDynamic:
		carry := decimal.Zero
		for i, c := range cells {
			need := c.SalesQty.Add(carry)
			out[i] = decimal.Max(c.Qty.Sub(need), decimal.Zero)
			carry = decimal.Max(need.Sub(c.Qty), decimal.Zero)
		}
	default:
		return nil, ErrInvalidReduction
	}
	return out, nil
}
