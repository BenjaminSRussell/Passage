package domain

import (
	"errors"
	"fmt"
)

// ErrNesting rejects a nesting that breaks the pallet rules (FUL-0056): only
// CLOSED, free cartons go on a pallet, a pallet holds one level, and a carton
// on a pallet is not unpacked on its own.
var ErrNesting = errors.New("FUL-0056: container nesting is not allowed")

// NestChild is what the pallet rule needs to know about a container that is
// to go on a pallet: its status, whether it already sits on a pallet and
// whether it is a pallet itself.
type NestChild struct {
	SSCC     string
	Status   string
	OnPallet bool
	IsPallet bool
}

func nestingProblem(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrNesting, fmt.Sprintf(format, a...))
}

func nestable(from ShipmentStatus) error {
	if from != StatusPicked && from != StatusPacked {
		return fmt.Errorf("%w: containers of a %s shipment cannot be nested", ErrInvalidMove, from)
	}
	return nil
}

// NestContainers checks that children can go on one new pallet (nested pallet
// to carton, FR-28.3): the shipment is PICKED or PACKED, at least one child is
// given, and every child is a CLOSED container that is neither on a pallet nor
// a pallet itself. Pallets hold one level only.
func NestContainers(from ShipmentStatus, children []NestChild) error {
	if err := nestable(from); err != nil {
		return err
	}
	if len(children) == 0 {
		return nestingProblem("a pallet needs at least one container")
	}
	for _, c := range children {
		switch {
		case c.Status != "CLOSED":
			return nestingProblem("container %s is %s, only a CLOSED container goes on a pallet", c.SSCC, c.Status)
		case c.IsPallet:
			return nestingProblem("container %s is a pallet; pallets do not nest", c.SSCC)
		case c.OnPallet:
			return nestingProblem("container %s is already on a pallet", c.SSCC)
		}
	}
	return nil
}

// UnnestPallet checks that a pallet can be dissolved: the shipment is PICKED
// or PACKED and the container is a CLOSED pallet that holds containers.
func UnnestPallet(from ShipmentStatus, status string, children int) error {
	if err := nestable(from); err != nil {
		return err
	}
	if status != "CLOSED" {
		return fmt.Errorf("%w (container is %s)", ErrContainerNotUnpackable, status)
	}
	if children == 0 {
		return nestingProblem("the container is not a pallet")
	}
	return nil
}

// UnpackNested refuses to unpack a carton that sits on a pallet, or a pallet:
// the pallet is dissolved first, so a pallet never points at a void carton.
func UnpackNested(onPallet bool, children int) error {
	if onPallet {
		return nestingProblem("the container is on a pallet; dissolve the pallet first")
	}
	if children > 0 {
		return nestingProblem("the container is a pallet; dissolve it with unnest, then unpack its cartons")
	}
	return nil
}
