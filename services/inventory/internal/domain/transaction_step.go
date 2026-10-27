package domain

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

// StepCommand names the command that registers or moves an inventory
// transaction (FR-16.2). CREATE registers a transaction for an external origin
// at its first status; STEP moves it forward. The backward moves BR-16.1
// allows are each reachable through one named command and no other.
type StepCommand string

const (
	CommandCreate            StepCommand = "CREATE"
	CommandStep              StepCommand = "STEP"
	CommandUnpick            StepCommand = "UNPICK"
	CommandUnregister        StepCommand = "UNREGISTER"
	CommandCancelReservation StepCommand = "CANCEL_RESERVATION"
)

// Error codes of status steps (service codes; the 3.16.18 catalog has none for
// the status model). The README lists them with the other error codes.
const (
	CodeStatusStep          = "INV-STP-001" // the move is not allowed by the status model or the command
	CodeStatusOrigin        = "INV-STP-002" // the origin already has a transaction with other values
	CodeTransactionNotFound = "INV-TXN-001"
)

// ErrUnknownCommand is returned for a command outside the step model.
var ErrUnknownCommand = errors.New("inventory: unknown status command")

// ErrNotNamedMove is returned when a named command is applied to a status it
// does not move, or a backward move is attempted under a command that does not
// own it.
var ErrNotNamedMove = errors.New("inventory: status move is not allowed by this command")

// namedBackward maps each named backward command to the moves it performs,
// from status to status (AX Unpick, Unregister, cancel reservation).
var namedBackward = map[StepCommand]map[Status]Status{
	CommandUnpick:            {StatusPicked: StatusReservedPhysical},
	CommandUnregister:        {StatusRegistered: StatusArrived},
	CommandCancelReservation: {StatusReservedPhysical: StatusOnOrder, StatusReservedOrdered: StatusOnOrder},
}

// NamedCommands lists the named backward commands.
func NamedCommands() []StepCommand {
	return []StepCommand{CommandUnpick, CommandUnregister, CommandCancelReservation}
}

// NamedMove returns the status a named backward command moves a transaction to
// from status from; ok is false when the command does not apply to from.
func NamedMove(cmd StepCommand, from Status) (to Status, ok bool) {
	to, ok = namedBackward[cmd][from]
	return to, ok
}

// ValidateStep checks one status move under a command. STEP accepts forward
// moves within a direction (Step); a named command accepts exactly its own
// moves.
func ValidateStep(cmd StepCommand, from, to Status) error {
	if _, _, ok := Lookup(from); !ok {
		return fmt.Errorf("%w: %q", ErrUnknownStatus, from)
	}
	if _, _, ok := Lookup(to); !ok {
		return fmt.Errorf("%w: %q", ErrUnknownStatus, to)
	}
	switch cmd {
	case CommandStep:
		return Step(from, to)
	case CommandUnpick, CommandUnregister, CommandCancelReservation:
		if want, ok := NamedMove(cmd, from); !ok || want != to {
			return fmt.Errorf("%w: %s does not move %s to %s", ErrNotNamedMove, cmd, from, to)
		}
		return nil
	}
	return fmt.Errorf("%w: %q", ErrUnknownCommand, cmd)
}

// ValidateCreate checks the first status of a transaction registered for an
// external origin: a known status that matches the sign of qty (receipts are
// positive, issues negative) and is not financial. Financial postings carry a
// value and are reached with STEP, so the value is fixed when the status is.
func ValidateCreate(qty decimal.Decimal, s Status) error {
	dir, _, ok := Lookup(s)
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownStatus, s)
	}
	switch {
	case qty.IsZero():
		return errors.New("inventory: quantity must not be zero")
	case qty.Sign() > 0 && dir != ReceiptDirection:
		return fmt.Errorf("%w: a receipt (positive quantity) starts at a receipt status, not %s", ErrBackwardStep, s)
	case qty.Sign() < 0 && dir != IssueDirection:
		return fmt.Errorf("%w: an issue (negative quantity) starts at an issue status, not %s", ErrBackwardStep, s)
	case IsFinancial(s):
		return fmt.Errorf("%w: a transaction is registered at a physical or expected status; step it to %s", ErrBackwardStep, s)
	}
	return nil
}

// AllStatuses lists every status of the model, issue statuses first.
func AllStatuses() []Status {
	var out []Status
	for _, dir := range []Direction{IssueDirection, ReceiptDirection} {
		out = append(out, statusSequence[dir]...)
	}
	return out
}

// BucketQty returns the projection quantity a status contributes to: the
// posted quantity for a financial status, otherwise the status's bucket.
func (o OnHand) BucketQty(s Status) decimal.Decimal {
	if _, _, ok := Lookup(s); !ok {
		return decimal.Zero
	}
	if IsFinancial(s) {
		return o.PostedQty
	}
	return *o.bucket(s)
}
