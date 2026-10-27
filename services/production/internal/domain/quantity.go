package domain

import "github.com/shopspring/decimal"

// QtyScale is the internal precision of quantities (BR-21.2).
const QtyScale = 6

var (
	hundred = decimal.NewFromInt(100)
	one     = decimal.NewFromInt(1)
)

// CeilQty rounds up to QtyScale decimal places. Quantities are rounded once,
// at the point of use, never at explosion (BR-21.2).
func CeilQty(d decimal.Decimal) decimal.Decimal {
	return d.Shift(QtyScale).Ceil().Shift(-QtyScale)
}

// RequiredQty implements BR-21.2: qtyPer × orderQty / seriesSize × (1 + scrap/100),
// rounded up to QtyScale places, or up to a whole unit when roundUp is set.
func RequiredQty(qtyPer, orderQty, seriesSize, scrapPct decimal.Decimal, roundUp bool) (decimal.Decimal, error) {
	if !seriesSize.IsPositive() {
		return decimal.Zero, &Error{Code: CodeInvalidInput, Detail: "series size must be positive"}
	}
	q := qtyPer.Mul(orderQty).Div(seriesSize).Mul(one.Add(scrapPct.Div(hundred)))
	if roundUp {
		return q.Ceil(), nil
	}
	return CeilQty(q), nil
}

// StartQuantities implements BR-21.8. scrapPcts is in route order. The quantity
// to start at operation n is orderQty / Π(1 − scrap_k) over k ≥ n, rounded up
// at each operation, so the chain is computed backward from the last operation.
func StartQuantities(orderQty decimal.Decimal, scrapPcts []decimal.Decimal) ([]decimal.Decimal, error) {
	out := make([]decimal.Decimal, len(scrapPcts))
	q := orderQty
	for n := len(scrapPcts) - 1; n >= 0; n-- {
		s := scrapPcts[n]
		if s.IsNegative() || !s.LessThan(hundred) {
			return nil, &Error{Code: CodeInvalidInput, Detail: "scrap must be from 0 up to but excluding 100"}
		}
		q = CeilQty(q.Div(one.Sub(s.Div(hundred))))
		out[n] = q
	}
	return out, nil
}
