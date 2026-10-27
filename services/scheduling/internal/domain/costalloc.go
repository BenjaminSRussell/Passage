package domain

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Allocation bases for a campaign's cleanout and leading-setup cost (FR-23.16).
const (
	BasisQuantity   = "quantity"
	BasisEqual      = "equal"
	BasisRunMinutes = "run-minutes"
)

// CostShare is one campaign order with the weight it earns in the allocation.
type CostShare struct {
	OrderID string
	Weight  decimal.Decimal
}

// Allocated is the amount assigned to one order.
type Allocated struct {
	OrderID string       `json:"orderId"`
	Amount  money.Amount `json:"-"`
}

// AllocationWeight maps an order's facts to its weight for a basis: the
// reported-as-finished quantity for quantity, one for equal, and the run
// minutes for run-minutes.
func AllocationWeight(basis string, rafQty decimal.Decimal, runMinutes int) (decimal.Decimal, error) {
	if rafQty.IsNegative() || runMinutes < 0 {
		return decimal.Zero, errors.New("allocation facts must not be negative")
	}
	switch basis {
	case BasisQuantity:
		return rafQty, nil
	case BasisEqual:
		return decimal.NewFromInt(1), nil
	case BasisRunMinutes:
		return decimal.NewFromInt(int64(runMinutes)), nil
	}
	return decimal.Zero, fmt.Errorf("unknown allocation basis %q", basis)
}

// AllocateCost splits cost across shares in proportion to weight (FR-23.16,
// BR-23.14): amount_i = round(cost x weight_i / total) to the currency's minor
// unit, half away from zero, and the residual goes to the largest share (ties:
// lowest order id), so the amounts always sum exactly to the cost.
//
// Rounding every share up can overshoot by up to half a minor unit per order.
// When that would leave the largest share negative (only possible when the
// cost is a few minor units spread over many orders), the shares are floored
// instead so that no order receives a credit; the residual rule is unchanged.
func AllocateCost(cost money.Amount, shares []CostShare) ([]Allocated, error) {
	if len(cost.Currency) != 3 {
		return nil, errors.New("cost needs a currency")
	}
	if cost.Value.IsNegative() {
		return nil, errors.New("cost to allocate must not be negative")
	}
	if len(shares) == 0 {
		return nil, errors.New("no orders to allocate the cost to")
	}
	cost = cost.Round()
	total := decimal.Zero
	largest := 0
	for i, s := range shares {
		if s.Weight.IsNegative() {
			return nil, fmt.Errorf("order %s has a negative weight", s.OrderID)
		}
		total = total.Add(s.Weight)
		if s.Weight.GreaterThan(shares[largest].Weight) ||
			(s.Weight.Equal(shares[largest].Weight) && s.OrderID < shares[largest].OrderID) {
			largest = i
		}
	}
	if total.IsZero() {
		return nil, errors.New("allocation weights sum to zero")
	}
	exp := money.MinorUnits(cost.Currency)
	out := split(cost, shares, largest, func(x decimal.Decimal) decimal.Decimal { return x.DivRound(total, exp) })
	if out[largest].Amount.Value.IsNegative() {
		out = split(cost, shares, largest, func(x decimal.Decimal) decimal.Decimal {
			q, _ := x.QuoRem(total, exp)
			return q
		})
	}
	return out, nil
}

// split applies share to cost x weight for every order and gives the residual
// to the largest share.
func split(cost money.Amount, shares []CostShare, largest int, share func(decimal.Decimal) decimal.Decimal) []Allocated {
	out := make([]Allocated, len(shares))
	assigned := decimal.Zero
	for i, s := range shares {
		amt := share(cost.Value.Mul(s.Weight))
		out[i] = Allocated{OrderID: s.OrderID, Amount: money.Amount{Value: amt, Currency: cost.Currency}}
		assigned = assigned.Add(amt)
	}
	out[largest].Amount.Value = out[largest].Amount.Value.Add(cost.Value.Sub(assigned))
	return out
}
