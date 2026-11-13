package domain

import "github.com/shopspring/decimal"

// PotencyResult is the consumption of an active-ingredient line and the
// adjusted compensating (filler) line.
type PotencyResult struct {
	Qty             decimal.Decimal
	CompensatingQty decimal.Decimal
}

// ConsumePotency implements BR-24.2. The active-ingredient quantity is
// planned × standardPotency / batchPotency; the compensating line is reduced by
// the same delta so the batch size is kept. A negative compensating quantity is
// rejected (PMF-0002, alias POTENCY_COMPENSATION_NEGATIVE).
func ConsumePotency(planned, standardPotency, batchPotency, compensatingPlanned decimal.Decimal) (PotencyResult, error) {
	if !standardPotency.IsPositive() || !batchPotency.IsPositive() {
		return PotencyResult{}, errorf(CodeInvalid, "potency must be positive")
	}
	if planned.IsNegative() || compensatingPlanned.IsNegative() {
		return PotencyResult{}, errorf(CodeInvalid, "planned quantities must not be negative")
	}
	qty := planned.Mul(standardPotency).DivRound(batchPotency, QtyPrecision)
	comp := compensatingPlanned.Sub(qty.Sub(planned))
	if comp.IsNegative() {
		return PotencyResult{}, errorf(CodeCompNegative, "compensating line would become negative (%s)", comp.Round(QtyPrecision).StringFixed(QtyPrecision))
	}
	return PotencyResult{Qty: qty, CompensatingQty: comp.Round(QtyPrecision)}, nil
}

// PotencyInRange reports whether a batch potency lies within [min, max] (FR-24.3:
// batches outside the range are not pickable for the line).
func PotencyInRange(actual, minimum, maximum decimal.Decimal) bool {
	return !actual.LessThan(minimum) && !actual.GreaterThan(maximum)
}

// PotencyAdjustment says that an active-ingredient line of a batch order is charged from an ingredient batch of a
// potency other than the formula's standard, and which line compensates (BR-24.2). BatchRef names the ingredient
// batch for the operator.
type PotencyAdjustment struct {
	LineNumber       int             `json:"lineNumber"`
	StandardPotency  decimal.Decimal `json:"standardPotency"`
	BatchPotency     decimal.Decimal `json:"batchPotency"`
	CompensatingLine int             `json:"compensatingLine"`
	BatchRef         string          `json:"batchRef,omitempty"`
}

// PotencyLine is what ApplyPotency needs of an order line.
type PotencyLine struct {
	LineNumber int
	Type       LineType
	Qty        decimal.Decimal
}

// ApplyPotency returns the potency-adjusted quantity of every line an adjustment touches: each active line is
// planned × standard / batch potency, and its compensating line gives up the difference, so the batch size is kept.
// Several active lines may share one compensating line; each takes from what the previous left. Both lines must be
// lines of the order: the adjusted one an active ingredient, the compensating one a plain ingredient (the filler).
// A line is adjusted once. A compensating line driven below zero is
// PMF-0002.
func ApplyPotency(lines []PotencyLine, adjustments []PotencyAdjustment) (map[int]decimal.Decimal, error) {
	qty := map[int]decimal.Decimal{}
	typ := map[int]LineType{}
	for _, l := range lines {
		qty[l.LineNumber] = l.Qty
		typ[l.LineNumber] = l.Type
	}
	out := map[int]decimal.Decimal{}
	seen, compensating := map[int]bool{}, map[int]bool{}
	for i, a := range adjustments {
		switch {
		case typ[a.LineNumber] != LineActiveIngredient:
			return nil, errorf(CodeInvalid, "adjustment %d: line %d is not an active-ingredient line of the order", i, a.LineNumber)
		case typ[a.CompensatingLine] != LineIngredient:
			return nil, errorf(CodeInvalid, "adjustment %d: the compensating line %d must be an ingredient (filler) line of the order", i, a.CompensatingLine)
		case seen[a.LineNumber]:
			return nil, errorf(CodeInvalid, "adjustment %d: line %d is adjusted twice", i, a.LineNumber)
		case compensating[a.LineNumber] || seen[a.CompensatingLine]:
			return nil, errorf(CodeInvalid, "adjustment %d: a line is either adjusted or compensating, not both", i)
		}
		seen[a.LineNumber], compensating[a.CompensatingLine] = true, true
		comp := qty[a.CompensatingLine]
		if v, ok := out[a.CompensatingLine]; ok {
			comp = v
		}
		res, err := ConsumePotency(qty[a.LineNumber], a.StandardPotency, a.BatchPotency, comp)
		if err != nil {
			return nil, err
		}
		out[a.LineNumber] = res.Qty
		out[a.CompensatingLine] = res.CompensatingQty
	}
	return out, nil
}
