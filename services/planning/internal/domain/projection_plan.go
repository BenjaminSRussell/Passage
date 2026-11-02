package domain

import "github.com/shopspring/decimal"

// NetDay is one stored day of an item's net requirements view (FR-22.12):
// the movement of the day and the projected available balance after it. Day is
// the offset from the plan version's as-of date.
type NetDay struct {
	Day       int
	Gross     decimal.Decimal
	Scheduled decimal.Decimal
	Planned   decimal.Decimal
	PAB       decimal.Decimal
}

// ProjectPlanned builds the supply schedule and inventory projection (FR-22.21,
// FR-22.22) of one item from its net requirements rows in a stored plan
// version, so the grid shows what the run planned and not a caller's guess.
// Gross requirements are the demand, scheduled receipts the open supply and
// planned receipts the run's planned supply. The opening on-hand is read back
// from the first row: PAB = opening + scheduled + planned - gross, which is
// the identity the run stores (one row per item and day), so it holds when the
// first day itself has receipts or demand. rows must be in day order and may be
// empty (an item the plan did not plan has no movement and projects to
// nothing); the first row may lie past the horizon, where it only gives the
// opening on-hand.
func ProjectPlanned(rows []NetDay, in ProjectionInput) (opening decimal.Decimal, p Projection, err error) {
	if len(rows) > 0 {
		first := rows[0]
		opening = first.PAB.Sub(first.Scheduled).Sub(first.Planned).Add(first.Gross)
	}
	if opening.IsNegative() {
		opening = decimal.Zero
	}
	in.OpeningOnHand = opening
	for _, r := range rows {
		in.Demand = append(in.Demand, Flow{Day: r.Day, Qty: r.Gross})
		in.Supply = append(in.Supply, Flow{Day: r.Day, Qty: r.Scheduled})
		in.Planned = append(in.Planned, Flow{Day: r.Day, Qty: r.Planned})
	}
	p, err = Project(in)
	return opening, p, err
}
