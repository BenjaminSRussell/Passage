package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Negative-stock error codes (message catalog 3.16.18).
const (
	CodeNegativePhysical  = "INV-NEG-001"
	CodeNegativeFinancial = "INV-NEG-002"
	// CodeQualityHold is an issue or transfer that would move held quantity
	// (wave 33 AC2 / AC-33.3).
	CodeQualityHold = "QUALITY_HOLD"
)

// OnHand is the projection row for one product and dimension set. Bucket
// fields hold magnitudes; posted quantity and value are signed.
type OnHand struct {
	PostedQty        decimal.Decimal
	PostedValue      decimal.Decimal
	Received         decimal.Decimal
	Registered       decimal.Decimal
	Arrived          decimal.Decimal
	Ordered          decimal.Decimal
	Deducted         decimal.Decimal
	Picked           decimal.Decimal
	ReservedPhysical decimal.Decimal
	HeldPhysical     decimal.Decimal
	ReservedOrdered  decimal.Decimal
	OnOrder          decimal.Decimal
}

// PhysicalInvent is posted + received + registered - deducted - picked (FR-16.3).
func (o OnHand) PhysicalInvent() decimal.Decimal {
	return o.PostedQty.Add(o.Received).Add(o.Registered).Sub(o.Deducted).Sub(o.Picked)
}

// AvailPhysical is physical invent minus physically reserved and held quantity.
func (o OnHand) AvailPhysical() decimal.Decimal {
	return o.PhysicalInvent().Sub(o.ReservedPhysical).Sub(o.HeldPhysical)
}

// AvailOrdered is avail physical + ordered + arrived - on order - reserved ordered.
func (o OnHand) AvailOrdered() decimal.Decimal {
	return o.AvailPhysical().Add(o.Ordered).Add(o.Arrived).Sub(o.OnOrder).Sub(o.ReservedOrdered)
}

// Post applies one transaction quantity at status s, multiplied by sign (+1 to
// add the status contribution, -1 to remove it). qty and value are signed:
// issues carry negative qty. Financial statuses move posted qty and value;
// other statuses move the magnitude of qty into their bucket.
func (o *OnHand) Post(s Status, qty, value decimal.Decimal, sign int64) error {
	if _, _, ok := Lookup(s); !ok {
		return fmt.Errorf("%w: %q", ErrUnknownStatus, s)
	}
	d := decimal.NewFromInt(sign)
	if IsFinancial(s) {
		o.PostedQty = o.PostedQty.Add(qty.Mul(d))
		o.PostedValue = o.PostedValue.Add(value.Mul(d))
		return nil
	}
	b := o.bucket(s)
	*b = b.Add(qty.Abs().Mul(d))
	return nil
}

func (o *OnHand) bucket(s Status) *decimal.Decimal {
	switch s {
	case StatusOnOrder:
		return &o.OnOrder
	case StatusReservedOrdered:
		return &o.ReservedOrdered
	case StatusReservedPhysical:
		return &o.ReservedPhysical
	case StatusPicked:
		return &o.Picked
	case StatusDeducted:
		return &o.Deducted
	case StatusOrdered:
		return &o.Ordered
	case StatusArrived:
		return &o.Arrived
	case StatusRegistered:
		return &o.Registered
	default: // StatusReceived; Post has validated the status
		return &o.Received
	}
}

// NegativePolicy says whether physical or financial negative stock is allowed
// for an item model group. The default forbids both (FR-16.4).
type NegativePolicy struct {
	PhysicalAllowed  bool
	FinancialAllowed bool
}

// NegativeError reports an issue that would drive available stock below zero.
type NegativeError struct {
	Code      string
	Shortfall decimal.Decimal
}

func (e *NegativeError) Error() string {
	return fmt.Sprintf("%s: shortfall %s", e.Code, e.Shortfall.String())
}

// CheckNegative returns a *NegativeError when the projection o (after a
// posting) is negative where the policy forbids it and the posting lowered it
// from before. A receipt into a row that is already negative (stock issued
// while the policy allowed it) is therefore never rejected. Callers run it
// after applying all deltas to the row, under the row lock (FR-16.4, BR-16.4).
func (o OnHand) CheckNegative(before OnHand, p NegativePolicy) error {
	if pi := o.PhysicalInvent(); o.HeldPhysical.Sign() > 0 && pi.LessThan(o.HeldPhysical) && pi.LessThan(before.PhysicalInvent()) {
		return &NegativeError{Code: CodeQualityHold, Shortfall: o.HeldPhysical.Sub(pi)}
	}
	if ap := o.AvailPhysical(); !p.PhysicalAllowed && ap.Sign() < 0 && ap.LessThan(before.AvailPhysical()) {
		return &NegativeError{Code: CodeNegativePhysical, Shortfall: ap.Neg()}
	}
	if ao := o.AvailOrdered(); !p.FinancialAllowed && ao.Sign() < 0 && ao.LessThan(before.AvailOrdered()) {
		return &NegativeError{Code: CodeNegativeFinancial, Shortfall: ao.Neg()}
	}
	return nil
}

// Contribution is one transaction's current state, used to rebuild a projection
// from scratch and compare it with the live row.
type Contribution struct {
	Status Status
	Qty    decimal.Decimal
	Value  decimal.Decimal
}

// Rebuild computes the projection implied by a set of transaction states.
func Rebuild(entries []Contribution) (OnHand, error) {
	var o OnHand
	for _, c := range entries {
		if err := o.Post(c.Status, c.Qty, c.Value, 1); err != nil {
			return OnHand{}, err
		}
	}
	return o, nil
}

// Diff names the bucket fields where a stored projection row differs from a
// rebuilt one (FR-16.3 rebuild-and-compare). An empty result means equal.
func Diff(stored, rebuilt OnHand) []string {
	pairs := []struct {
		name string
		a, b decimal.Decimal
	}{
		{"posted_qty", stored.PostedQty, rebuilt.PostedQty},
		{"posted_value", stored.PostedValue, rebuilt.PostedValue},
		{"received", stored.Received, rebuilt.Received},
		{"registered", stored.Registered, rebuilt.Registered},
		{"arrived", stored.Arrived, rebuilt.Arrived},
		{"ordered", stored.Ordered, rebuilt.Ordered},
		{"deducted", stored.Deducted, rebuilt.Deducted},
		{"picked", stored.Picked, rebuilt.Picked},
		{"reserved_physical", stored.ReservedPhysical, rebuilt.ReservedPhysical},
		{"held_physical", stored.HeldPhysical, rebuilt.HeldPhysical},
		{"reserved_ordered", stored.ReservedOrdered, rebuilt.ReservedOrdered},
		{"on_order", stored.OnOrder, rebuilt.OnOrder},
	}
	var out []string
	for _, p := range pairs {
		if !p.a.Equal(p.b) {
			out = append(out, p.name)
		}
	}
	return out
}
