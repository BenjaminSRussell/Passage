package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Change decisions for a linked sales order (BR-40.3).
const (
	ChangeUpdateSilently = "UpdateSilently"
	ChangeReApprove      = "ReApprove"
)

// CodeChangeTolerance is the warning raised when a change exceeds the pairing's tolerance.
const CodeChangeTolerance = "IC-0804"

// OrderChange is a change to a linked sales order as the mirror purchase order sees it.
type OrderChange struct {
	OldQty        decimal.Decimal
	NewQty        decimal.Decimal
	OldPrice      decimal.Decimal
	NewPrice      decimal.Decimal
	DateShiftDays int
}

// ChangeDecision says whether the mirror PO updates silently or goes back to
// approval (wave 17 workflow). Reasons lists every breach and is empty when
// the change is silent.
type ChangeDecision struct {
	Action  string
	Code    string
	Reasons []string
}

// DecideOrderChange applies the pairing's tolerance to a change (BR-40.3):
// quantity must not change, the unit price may move by the tolerance
// percentage of the old price and the date by the tolerance days, both
// inclusive. Any breach sends the mirror PO into re-approval with IC-0804.
func DecideOrderChange(t Tolerance, ch OrderChange) ChangeDecision {
	reasons := []string{}
	if !ch.OldQty.Equal(ch.NewQty) {
		reasons = append(reasons, fmt.Sprintf("quantity changed from %s to %s; quantity changes always need re-approval", ch.OldQty, ch.NewQty))
	}
	switch {
	case ch.OldPrice.IsZero() && !ch.NewPrice.IsZero():
		reasons = append(reasons, fmt.Sprintf("price changed from 0 to %s", ch.NewPrice))
	case !ch.OldPrice.IsZero():
		move := ch.OldPrice.Sub(ch.NewPrice).Abs().Div(ch.OldPrice).Mul(decimal.NewFromInt(100))
		if move.GreaterThan(t.PricePct) {
			reasons = append(reasons, fmt.Sprintf("price moved %s%% against a tolerance of %s%%", move.StringFixed(2), t.PricePct))
		}
	}
	shift := ch.DateShiftDays
	if shift < 0 {
		shift = -shift
	}
	if shift > t.DateDays {
		reasons = append(reasons, fmt.Sprintf("date moved %d days against a tolerance of %d days", shift, t.DateDays))
	}
	if len(reasons) == 0 {
		return ChangeDecision{Action: ChangeUpdateSilently, Reasons: reasons}
	}
	return ChangeDecision{Action: ChangeReApprove, Code: CodeChangeTolerance, Reasons: reasons}
}
