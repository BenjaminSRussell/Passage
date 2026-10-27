package domain

import "github.com/shopspring/decimal"

// Manual cost adjustments (FR-20.6). An ON_HAND adjustment revalues the stock
// of one product x site x warehouse x valuation type at a date, either to a new
// unit cost or by a signed amount; a RECEIPT adjustment changes the value of one
// receipt transaction by an amount, which flows to its issues on the next close.
const (
	AdjustOnHand  = "ON_HAND"
	AdjustReceipt = "RECEIPT"

	ModeUnitCost = "UNIT_COST"
	ModeAmount   = "AMOUNT"
)

// Adjustment refusal codes. CST-ADJ-001 is kept for the closed-period lock of
// BR-20.8, which needs the close job; CST-ADJ-006 (stale preview) and
// CST-ADJ-007 (not found) are raised by the store.
const (
	CodeAdjustNothing  = "CST-ADJ-002"
	CodeAdjustUnitCost = "CST-ADJ-003"
	CodeAdjustNoChange = "CST-ADJ-004"
	CodeAdjustNegative = "CST-ADJ-005"
)

// unitCostScale is the stored precision of a unit cost (numeric(19,6)).
const unitCostScale int32 = 6

// AdjustmentInput is the current quantity and value of what is adjusted, and
// the change asked for: UnitCost in ModeUnitCost, Amount in ModeAmount. Scale
// is the currency's minor unit, to which a revalued amount is rounded.
type AdjustmentInput struct {
	Mode     string
	Qty      decimal.Decimal
	Value    decimal.Decimal
	UnitCost decimal.Decimal
	Amount   decimal.Decimal
	Scale    int32
}

// AdjustmentPlan is the value change an adjustment posts. Delta is exactly
// NewValue - OldValue and is the amount of the MANUAL_ADJ cost transaction;
// the quantity never changes. The unit costs are set only when Qty > 0.
type AdjustmentPlan struct {
	Qty         decimal.Decimal
	OldValue    decimal.Decimal
	NewValue    decimal.Decimal
	Delta       decimal.Decimal
	HasUnitCost bool
	OldUnitCost decimal.Decimal
	NewUnitCost decimal.Decimal
}

// PlanAdjustment computes an adjustment. A new unit cost needs stock to
// revalue (CST-ADJ-003) and is applied as round-half-even(unit cost x qty) at
// the currency's minor unit (BR-20.4). An adjustment that changes nothing is
// refused (CST-ADJ-004), and so is one that leaves a negative value on positive
// stock (CST-ADJ-005). A value left on zero stock may be cleared by amount.
func PlanAdjustment(in AdjustmentInput) (AdjustmentPlan, error) {
	p := AdjustmentPlan{Qty: in.Qty, OldValue: in.Value}
	switch in.Mode {
	case ModeUnitCost:
		if !in.Qty.IsPositive() {
			return AdjustmentPlan{}, &Error{CodeAdjustUnitCost, "there is no positive stock to revalue at a unit cost; adjust by amount instead"}
		}
		p.NewValue = in.UnitCost.Mul(in.Qty).RoundBank(in.Scale)
	case ModeAmount:
		p.NewValue = in.Value.Add(in.Amount)
	default:
		return AdjustmentPlan{}, &Error{CodeAdjustNoChange, "the adjustment mode must be UNIT_COST or AMOUNT"}
	}
	p.Delta = p.NewValue.Sub(p.OldValue)
	if p.Delta.IsZero() {
		return AdjustmentPlan{}, &Error{CodeAdjustNoChange, "the adjustment does not change the value"}
	}
	if in.Qty.IsPositive() && p.NewValue.IsNegative() {
		return AdjustmentPlan{}, &Error{CodeAdjustNegative, "the adjustment would leave a negative value on positive stock"}
	}
	if in.Qty.IsPositive() {
		p.HasUnitCost = true
		p.OldUnitCost = in.Value.DivRound(in.Qty, unitCostScale)
		p.NewUnitCost = p.NewValue.DivRound(in.Qty, unitCostScale)
		if in.Mode == ModeUnitCost {
			p.NewUnitCost = in.UnitCost
		}
	}
	return p, nil
}
