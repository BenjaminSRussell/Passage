package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// CodeTruckLevel is INV-TRK-001: a truck stock level that cannot be stored (field service, BR-45.7).
const CodeTruckLevel = "INV-TRK-001"

// TruckLevel is the stock a truck should hold of one product: below Min it is replenished up
// to Max. Quantities are in Unit, the product's inventory unit.
type TruckLevel struct {
	ProductID string
	Unit      string
	Min       decimal.Decimal
	Max       decimal.Decimal
}

// Validate checks a level: Min is not negative, Max is positive and not below Min, and both
// have at most scale decimals.
func (l TruckLevel) Validate(scale int32) error {
	switch {
	case l.Min.IsNegative() || !l.Max.IsPositive() || l.Max.LessThan(l.Min):
		return fmt.Errorf("min must be 0 or more and max positive and not below min")
	case l.Min.Exponent() < -scale || l.Max.Exponent() < -scale:
		return fmt.Errorf("quantities have at most %d decimals", scale)
	}
	return nil
}

// ReplenishQty is what to move to a truck holding onHand with inbound already on its way: zero
// unless the two together are below Min, then enough to reach Max.
func (l TruckLevel) ReplenishQty(onHand, inbound decimal.Decimal) decimal.Decimal {
	have := onHand.Add(inbound)
	if !have.LessThan(l.Min) {
		return decimal.Zero
	}
	if q := l.Max.Sub(have); q.IsPositive() {
		return q
	}
	return decimal.Zero
}
