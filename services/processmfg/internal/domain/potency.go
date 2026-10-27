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
