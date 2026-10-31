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

// ceilDiv rounds num ÷ den up to places decimals exactly. A decimal Div rounds
// at 16 places first, so a quotient like 200 ÷ 3 comes back a hair above the
// true value and a later multiply and round-up gains a whole unit at the last
// place (80.000001 instead of 80); the integer remainder has no such error.
func ceilDiv(num, den decimal.Decimal, places int32) decimal.Decimal {
	q, r := num.Shift(places).QuoRem(den, 0)
	if !r.IsZero() && num.Sign() == den.Sign() {
		q = q.Add(one)
	}
	return q.Shift(-places)
}

// CeilQtyDiv is CeilQty(num ÷ den) computed without intermediate rounding.
func CeilQtyDiv(num, den decimal.Decimal) decimal.Decimal { return ceilDiv(num, den, QtyScale) }

// RequiredQty implements BR-21.2: qtyPer × orderQty / seriesSize × (1 + scrap/100),
// rounded up to QtyScale places, or up to a whole unit when roundUp is set. The
// division by the series size comes last and is exact, so the round-up never
// lifts a whole requirement (2 × 100 / 3 × 1.2 is 80, not 80.000001).
func RequiredQty(qtyPer, orderQty, seriesSize, scrapPct decimal.Decimal, roundUp bool) (decimal.Decimal, error) {
	if !seriesSize.IsPositive() {
		return decimal.Zero, &Error{Code: CodeInvalidInput, Detail: "series size must be positive"}
	}
	num := qtyPer.Mul(orderQty).Mul(hundred.Add(scrapPct))
	den := seriesSize.Mul(hundred)
	if roundUp {
		return ceilDiv(num, den, 0), nil
	}
	return ceilDiv(num, den, QtyScale), nil
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
