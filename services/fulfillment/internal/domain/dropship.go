package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Drop-ship statuses (FR-28.7).
const (
	DropShipOpen      = "OPEN"
	DropShipConfirmed = "CONFIRMED"
	DropShipCancelled = "CANCELLED"
)

// DropShipPosting returns the journal booked when a vendor confirms a
// drop-ship line (BR-28.1, AC-28.5): Dr 5000 cost of goods sold / Cr 2110
// goods-received-not-invoiced at the purchase order cost, with no inventory
// transaction. Debits are positive, credits negative. A zero cost posts
// nothing. The amount rounds once, like a stocked line.
func DropShipPosting(orderLineID string, qty, unitCost decimal.Decimal, currency string) ([]JournalLine, error) {
	if !qty.IsPositive() {
		return nil, ErrNegativeQty
	}
	amt := money.Amount{Value: unitCost, Currency: currency}.Mul(qty)
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
