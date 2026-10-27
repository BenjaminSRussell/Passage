package domain

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// ShipmentStatus is the BR-28.6 state of a shipment.
type ShipmentStatus string

// Shipment states.
const (
	StatusOpen      ShipmentStatus = "OPEN"
	StatusReleased  ShipmentStatus = "RELEASED"
	StatusPicking   ShipmentStatus = "PICKING"
	StatusPicked    ShipmentStatus = "PICKED"
	StatusPacked    ShipmentStatus = "PACKED"
	StatusShipped   ShipmentStatus = "SHIPPED"
	StatusDelivered ShipmentStatus = "DELIVERED"
	StatusCancelled ShipmentStatus = "CANCELLED"
)

// Errors returned by the pick and pack rules. Codes are the FUL catalog entries.
var (
	ErrOverPick        = errors.New("FUL-0004: over-pick")
	ErrOverPack        = errors.New("FUL-0023: packed quantity exceeds picked quantity")
	ErrNegativeQty     = errors.New("FUL-0030: quantity must not be negative")
	ErrInvalidMove     = errors.New("FUL-0021: shipment transition is not allowed")
	ErrStatusNotPacked = errors.New("FUL-0001: shipment must be PACKED with no active hold to post")
)

var transitions = map[ShipmentStatus][]ShipmentStatus{
	StatusOpen:      {StatusReleased, StatusCancelled},
	StatusReleased:  {StatusPicking, StatusCancelled},
	StatusPicking:   {StatusPicked, StatusCancelled},
	StatusPicked:    {StatusPacked, StatusCancelled},
	StatusPacked:    {StatusShipped, StatusCancelled},
	StatusShipped:   {StatusDelivered, StatusPacked},
	StatusDelivered: nil,
	StatusCancelled: nil,
}

// CanTransition reports whether BR-28.6 allows from -> to.
func CanTransition(from, to ShipmentStatus) bool {
	for _, next := range transitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// Advance moves a shipment to next, or stays put when it is already there.
func Advance(from, next ShipmentStatus) (ShipmentStatus, error) {
	if from == next {
		return from, nil
	}
	if !CanTransition(from, next) {
		return from, fmt.Errorf("%w: %s to %s", ErrInvalidMove, from, next)
	}
	return next, nil
}

// LineQty carries the quantities of one shipment line through pick and pack.
type LineQty struct {
	Planned   decimal.Decimal
	Picked    decimal.Decimal
	Packed    decimal.Decimal
	Shipped   decimal.Decimal
	Confirmed bool
}

// ConfirmPick records the picked quantity for a line (FR-28.2, BR-28.7). An
// over-pick is rejected. A short pick leaves the difference as backorder,
// returned so the caller can release the unpicked reservation.
func ConfirmPick(l LineQty, picked decimal.Decimal) (LineQty, decimal.Decimal, error) {
	if picked.IsNegative() {
		return l, decimal.Zero, ErrNegativeQty
	}
	if picked.GreaterThan(l.Planned) {
		return l, decimal.Zero, fmt.Errorf("%w: %s exceeds planned %s", ErrOverPick, picked, l.Planned)
	}
	l.Picked = picked
	l.Confirmed = true
	return l, l.Planned.Sub(picked), nil
}

// CanPack checks that packing qty more keeps packed <= picked (BR-28.1 invariant).
func CanPack(l LineQty, qty decimal.Decimal) error {
	if !qty.IsPositive() {
		return ErrNegativeQty
	}
	if l.Packed.Add(qty).GreaterThan(l.Picked) {
		return fmt.Errorf("%w: packed %s + %s > picked %s", ErrOverPack, l.Packed, qty, l.Picked)
	}
	return nil
}

// AllConfirmed reports whether every line has had its pick confirmed.
func AllConfirmed(lines []LineQty) bool {
	for _, l := range lines {
		if !l.Confirmed {
			return false
		}
	}
	return len(lines) > 0
}

// AllPacked reports whether every picked unit is packed.
func AllPacked(lines []LineQty) bool {
	for _, l := range lines {
		if !l.Confirmed || !l.Packed.Equal(l.Picked) {
			return false
		}
	}
	return len(lines) > 0
}
