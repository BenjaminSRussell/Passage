package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// ShortPickAction is the warehouse setting that says what happens to the open
// remainder of a shipment once a packing slip has shipped part of it (FR-28.8).
type ShortPickAction string

// Short pick actions.
const (
	// ActionKeepOpen leaves the shipment PARTIALLY_SHIPPED; its remainder is
	// re-picked on the same shipment. It is not the default.
	ActionKeepOpen ShortPickAction = "KEEP_OPEN"
	// ActionCancelRemainder closes the remainder (the shipment becomes SHIPPED).
	ActionCancelRemainder ShortPickAction = "CANCEL_REMAINDER"
	// ActionBackorder (REMAINDER_BACKORDER, the code of the PRD parameter)
	// closes the remainder and moves it to a new shipment of the same order and
	// warehouse.
	ActionBackorder ShortPickAction = "REMAINDER_BACKORDER"
)

// DefaultShortPickAction applies to a warehouse with no saved setting: the
// PRD parameter default (section 5.28.13), REMAINDER_BACKORDER.
const DefaultShortPickAction = ActionBackorder

// ParseShortPickAction validates a short pick action code.
func ParseShortPickAction(code string) (ShortPickAction, error) {
	switch a := ShortPickAction(code); a {
	case ActionKeepOpen, ActionCancelRemainder, ActionBackorder:
		return a, nil
	}
	return "", fmt.Errorf("short pick action must be KEEP_OPEN, CANCEL_REMAINDER or REMAINDER_BACKORDER")
}

// BackorderLine is one line of a backorder shipment: the quantity a line gave
// up when the remainder closed, in the order of the shipment's lines.
type BackorderLine struct {
	LineIndex int
	Qty       decimal.Decimal
}

// BackorderLines lists the lines that owe a remainder, from the closed
// quantities CloseRemainder returned.
func BackorderLines(closed []ClosedQty) []BackorderLine {
	out := []BackorderLine{}
	for i, c := range closed {
		if c.Closed.IsPositive() {
			out = append(out, BackorderLine{LineIndex: i, Qty: c.Closed})
		}
	}
	return out
}
