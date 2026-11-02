package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Drop-ship statuses (FR-28.7).
const (
	DropShipOpen = "OPEN"
	// DropShipPartial: the vendor shipped part of the quantity and the rest is open.
	DropShipPartial   = "PARTIALLY_CONFIRMED"
	DropShipConfirmed = "CONFIRMED"
	DropShipCancelled = "CANCELLED"
)

// DropShipPosting returns the journal booked when a vendor confirms qty of a
// drop-ship line (BR-28.1, AC-28.5): Dr 5000 cost of goods sold / Cr 2110
// goods-received-not-invoiced at the purchase order cost, with no inventory
// transaction. Debits are positive, credits negative. confirmedBefore is the
// quantity earlier confirmations of the line booked. The amount is the rounded
// cost of everything confirmed so far less the rounded cost of what was
// confirmed before, so the parts of a vendor that ships in parts always add up
// to the cost of the confirmed total rounded once, like a stocked line (a
// whole confirmation is that single rounding). A zero amount posts nothing.
func DropShipPosting(orderLineID string, confirmedBefore, qty, unitCost decimal.Decimal, currency string) ([]JournalLine, error) {
	if !qty.IsPositive() {
		return nil, ErrNegativeQty
	}
	if confirmedBefore.IsNegative() {
		return nil, ErrNegativeQty
	}
	cost := money.Amount{Value: unitCost, Currency: currency}
	amt, err := cost.Mul(confirmedBefore.Add(qty)).Add(cost.Mul(confirmedBefore).Neg())
	if err != nil {
		return nil, err
	}
	if amt.IsZero() {
		return nil, nil
	}
	out := []JournalLine{
		{Account: AccountCOGS, Amount: amt, OrderLineID: orderLineID},
		{Account: AccountGRIRClearing, Amount: amt.Neg(), OrderLineID: orderLineID},
	}
	if err := checkBalanced(out, currency); err != nil {
		return nil, err
	}
	return out, nil
}

// DropShipOpenQty is the quantity the vendor has yet to ship or close: ordered
// less confirmed less closed.
func DropShipOpenQty(qty, confirmed, closed decimal.Decimal) decimal.Decimal {
	return qty.Sub(confirmed).Sub(closed)
}

// DropShipStatusAfter is the status once confirmed plus closed quantity is
// known: CONFIRMED when none is left open, PARTIALLY_CONFIRMED when some is.
func DropShipStatusAfter(qty, confirmed, closed decimal.Decimal) string {
	if DropShipOpenQty(qty, confirmed, closed).IsPositive() {
		return DropShipPartial
	}
	return DropShipConfirmed
}
