package domain

import (
	"errors"

	"github.com/shopspring/decimal"
)

// ErrOverSettlement is CO-0618: a settlement may not exceed the cost collected
// on the internal order (BR-38.9).
var ErrOverSettlement = errors.New("settlement exceeds collected cost")

// CheckSettlement validates settling amount from an order that has collected
// cost on its books for the period. Prior settlements already reduce collected,
// because each one credits the order.
func CheckSettlement(amount, collected decimal.Decimal) error {
	if amount.Sign() <= 0 {
		return errors.New("settlement amount must be positive")
	}
	if amount.GreaterThan(collected) {
		return ErrOverSettlement
	}
	return nil
}
