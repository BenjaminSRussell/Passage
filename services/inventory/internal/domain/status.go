package domain

import (
	"errors"
	"fmt"
)

// Direction is the sign side of an inventory transaction.
type Direction string

const (
	IssueDirection   Direction = "ISSUE"
	ReceiptDirection Direction = "RECEIPT"
)

// Status is an AX-style issue or receipt status of a transaction.
type Status string

const (
	StatusOnOrder          Status = "ON_ORDER"
	StatusReservedOrdered  Status = "RESERVED_ORDERED"
	StatusReservedPhysical Status = "RESERVED_PHYSICAL"
	StatusPicked           Status = "PICKED"
	StatusDeducted         Status = "DEDUCTED"
	StatusSold             Status = "SOLD"
	StatusOrdered          Status = "ORDERED"
	StatusArrived          Status = "ARRIVED"
	StatusRegistered       Status = "REGISTERED"
	StatusReceived         Status = "RECEIVED"
	StatusPurchased        Status = "PURCHASED"
)

// statusSequence lists each direction's statuses in forward order (BR-16.1).
var statusSequence = map[Direction][]Status{
	IssueDirection:   {StatusOnOrder, StatusReservedOrdered, StatusReservedPhysical, StatusPicked, StatusDeducted, StatusSold},
	ReceiptDirection: {StatusOrdered, StatusArrived, StatusRegistered, StatusReceived, StatusPurchased},
}

// ErrBackwardStep is returned when a forward-only step would move backwards or
// change direction. Backward moves exist only as named commands (unpick,
// unregister, cancel reservation), which are outside this slice.
var ErrBackwardStep = errors.New("inventory: status step is not forward")

// ErrUnknownStatus is returned for a status outside the model.
var ErrUnknownStatus = errors.New("inventory: unknown status")

// Lookup returns the direction and zero-based rank of s.
func Lookup(s Status) (Direction, int, bool) {
	for dir, seq := range statusSequence {
		for i, v := range seq {
			if v == s {
				return dir, i, true
			}
		}
	}
	return "", 0, false
}

// CanStep reports whether a transaction may move forward from one status to
// another: same direction and a strictly later rank. Skips are allowed.
func CanStep(from, to Status) bool {
	fd, fr, ok1 := Lookup(from)
	td, tr, ok2 := Lookup(to)
	return ok1 && ok2 && fd == td && tr > fr
}

// Step validates a forward step, returning ErrBackwardStep or ErrUnknownStatus.
func Step(from, to Status) error {
	if _, _, ok := Lookup(from); !ok {
		return fmt.Errorf("%w: %q", ErrUnknownStatus, from)
	}
	if _, _, ok := Lookup(to); !ok {
		return fmt.Errorf("%w: %q", ErrUnknownStatus, to)
	}
	if !CanStep(from, to) {
		return fmt.Errorf("%w: %s to %s", ErrBackwardStep, from, to)
	}
	return nil
}

// IsFinancial reports whether s is a financial status (SOLD, PURCHASED). Only
// financial statuses change posted quantity and posted value.
func IsFinancial(s Status) bool { return s == StatusSold || s == StatusPurchased }

// DirectionFor returns the direction a status belongs to.
func DirectionFor(s Status) (Direction, error) {
	d, _, ok := Lookup(s)
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownStatus, s)
	}
	return d, nil
}
