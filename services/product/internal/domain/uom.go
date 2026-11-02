package domain

import "github.com/shopspring/decimal"

const uomDivisionPrecision int32 = 24

// ConvertQuantity applies product-specific conversions first, then class conversions (FR-8.7, AC-8.8).
func ConvertQuantity(product, class []Conversion, from, to string, qty decimal.Decimal, precision int32) (decimal.Decimal, error) {
	if from == to {
		return roundHalfEven(qty, precision), nil
	}
	if precision < 0 {
		precision = 6
	}
	if out, ok, err := convertOnce(product, from, to, qty, precision); err != nil {
		return decimal.Zero, err
	} else if ok {
		return out, nil
	}
	if out, ok, err := convertOnce(class, from, to, qty, precision); err != nil {
		return decimal.Zero, err
	} else if ok {
		return out, nil
	}
	// Two-hop via a shared class unit (e.g. BOX → EA → CASE).
	if out, ok, err := convertTwoHop(append(append([]Conversion{}, product...), class...), from, to, qty, precision); err != nil {
		return decimal.Zero, err
	} else if ok {
		return out, nil
	}
	return decimal.Zero, errf(CodeUOMUnderflow, "no conversion path from "+from+" to "+to)
}

func convertOnce(convs []Conversion, from, to string, qty decimal.Decimal, precision int32) (decimal.Decimal, bool, error) {
	for _, c := range convs {
		if c.Factor.IsZero() {
			continue
		}
		if c.From == from && c.To == to {
			return finish(qty.Mul(c.Factor).Add(c.Offset), qty, precision)
		}
		if c.From == to && c.To == from {
			return finish(qty.Sub(c.Offset).DivRound(c.Factor, uomDivisionPrecision), qty, precision)
		}
	}
	return decimal.Zero, false, nil
}

func convertTwoHop(convs []Conversion, from, to string, qty decimal.Decimal, precision int32) (decimal.Decimal, bool, error) {
	type edge struct {
		next  string
		apply func(decimal.Decimal) decimal.Decimal
		class string
	}
	neighbours := func(unit string) []edge {
		var out []edge
		for _, c := range convs {
			if c.Factor.IsZero() {
				continue
			}
			if c.From == unit {
				cc := c
				out = append(out, edge{next: cc.To, class: cc.Class, apply: func(q decimal.Decimal) decimal.Decimal {
					return q.Mul(cc.Factor).Add(cc.Offset)
				}})
			}
			if c.To == unit {
				cc := c
				out = append(out, edge{next: cc.From, class: cc.Class, apply: func(q decimal.Decimal) decimal.Decimal {
					return q.Sub(cc.Offset).DivRound(cc.Factor, uomDivisionPrecision)
				}})
			}
		}
		return out
	}
	for _, first := range neighbours(from) {
		if first.next == to {
			return finish(first.apply(qty), qty, precision)
		}
		for _, second := range neighbours(first.next) {
			if second.next == to && (first.class == "" || second.class == "" || first.class == second.class) {
				return finish(second.apply(first.apply(qty)), qty, precision)
			}
		}
	}
	return decimal.Zero, false, nil
}

func finish(out, in decimal.Decimal, precision int32) (decimal.Decimal, bool, error) {
	rounded := roundHalfEven(out, precision)
	if !in.IsZero() && rounded.IsZero() {
		return decimal.Zero, false, errf(CodeUOMUnderflow, "conversion rounds "+in.String()+" to zero")
	}
	return rounded, true, nil
}

func roundHalfEven(q decimal.Decimal, precision int32) decimal.Decimal {
	return q.RoundBank(precision)
}
