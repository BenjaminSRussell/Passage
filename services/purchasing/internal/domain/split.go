package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Limits of splitting a requisition line (FR-17.3 "the ability to split lines").
const (
	// MaxSplitParts bounds the new lines one split request can cut from a line.
	MaxSplitParts = 20
	// QtyScale is the number of decimals a quantity keeps (numeric(20,6)).
	QtyScale = 6
)

// SplitPiece is one new line cut from a requisition line: its quantity and its share of the line amount.
type SplitPiece struct {
	Qty    decimal.Decimal
	Amount decimal.Decimal
}

// SplitResult is what remains on the original line and the new lines cut from it.
type SplitResult struct {
	Remaining SplitPiece
	Pieces    []SplitPiece
}

// SplitRequisitionLine cuts parts out of a line of qty and amount. The original line keeps what is left, so the
// quantities add up to qty and the amounts to amount exactly: each new line's amount is the difference of the
// rounded cumulative share of amount, rounded half to even to the currency's minor units (minor), so rounding
// never creates or loses a minor unit and no share is negative. Every part must be above zero with at most
// QtyScale decimals, and the parts must leave a quantity above zero on the original line.
func SplitRequisitionLine(qty, amount decimal.Decimal, parts []decimal.Decimal, minor int32) (SplitResult, error) {
	if len(parts) == 0 {
		return SplitResult{}, fmt.Errorf("a split needs at least one quantity")
	}
	if len(parts) > MaxSplitParts {
		return SplitResult{}, fmt.Errorf("a split makes at most %d new lines", MaxSplitParts)
	}
	cum, prev := decimal.Zero, decimal.Zero
	out := SplitResult{Pieces: make([]SplitPiece, 0, len(parts))}
	for i, p := range parts {
		if !p.IsPositive() {
			return SplitResult{}, fmt.Errorf("quantities[%d] must be greater than zero", i)
		}
		if p.Exponent() < -QtyScale {
			return SplitResult{}, fmt.Errorf("quantities[%d] has more than %d decimals", i, QtyScale)
		}
		cum = cum.Add(p)
		if cum.Cmp(qty) >= 0 {
			return SplitResult{}, fmt.Errorf("the new quantities must leave a quantity above zero on the original line (%s)", qty)
		}
		share := amount.Mul(cum).Div(qty).RoundBank(minor)
		out.Pieces = append(out.Pieces, SplitPiece{Qty: p, Amount: share.Sub(prev)})
		prev = share
	}
	out.Remaining = SplitPiece{Qty: qty.Sub(cum), Amount: amount.Sub(prev)}
	return out, nil
}
