// Package domain holds the pure production rules: the order state machine,
// quantity rounding, BOM explosion, cost estimation and settlement. It does no I/O.
package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Status is the production order status (BR-21.1).
type Status string

const (
	Created            Status = "Created"
	Estimated          Status = "Estimated"
	Scheduled          Status = "Scheduled"
	Released           Status = "Released"
	Started            Status = "Started"
	ReportedAsFinished Status = "ReportedAsFinished"
	Ended              Status = "Ended"
	Cancelled          Status = "Cancelled"
)

// Command is a status transition requested through the `:command` custom methods.
type Command string

const (
	CmdEstimate       Command = "estimate"
	CmdSchedule       Command = "schedule"
	CmdRelease        Command = "release"
	CmdStart          Command = "start"
	CmdReportFinished Command = "report-finished"
	CmdEnd            Command = "end"
	CmdCancel         Command = "cancel"
	CmdResetStatus    Command = "reset-status"
)

// Stable error codes raised by the domain.
const (
	CodeIllegalTransition = "PRODUCTION_ILLEGAL_TRANSITION"
	CodeInvalidInput      = "PRODUCTION_INVALID_INPUT"
	CodeZeroOutput        = "PRODUCTION_ZERO_OUTPUT_NOT_PERMITTED"
	CodeNotRAFed          = "PRODUCTION_END_REQUIRES_RAF"
	CodeRAFExceeds        = "PRODUCTION_RAF_EXCEEDS_REMAINING"
	CodeNotEditable       = "PRODUCTION_FIELD_NOT_EDITABLE"
)

// lifecycle is the forward order of the statuses a reset can move between.
var lifecycle = []Status{Created, Estimated, Scheduled, Released, Started, ReportedAsFinished}

func rank(s Status) int {
	for i, x := range lifecycle {
		if x == s {
			return i
		}
	}
	return -1
}

// Valid reports whether s is one of the eight statuses.
func (s Status) Valid() bool { return rank(s) >= 0 || s == Ended || s == Cancelled }

// Error is a rule violation carrying a stable code.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Transition returns the status reached by applying cmd to from (BR-21.1).
// Guards that need order facts (quantities, duties) are checked by CheckEnd and
// CheckReportFinished. `:estimate` on an Estimated, Scheduled or Released order
// re-estimates and keeps the status (FR-21.3: re-estimate is allowed until Started).
func Transition(from Status, cmd Command) (Status, error) {
	to, ok := next(from, cmd)
	if !ok {
		return "", &Error{Code: CodeIllegalTransition, Detail: fmt.Sprintf("transition %s via %s is not allowed", from, cmd)}
	}
	return to, nil
}

func next(from Status, cmd Command) (Status, bool) {
	switch cmd {
	case CmdEstimate:
		switch from {
		case Created:
			return Estimated, true
		case Estimated, Scheduled, Released:
			return from, true
		}
		return "", false
	case CmdSchedule:
		return Scheduled, from == Estimated
	case CmdRelease:
		return Released, from == Scheduled
	case CmdStart:
		return Started, from == Released
	case CmdReportFinished:
		// A further report on a ReportedAsFinished order adds quantity (AX parity).
		return ReportedAsFinished, from == Started || from == ReportedAsFinished
	case CmdEnd:
		return Ended, from == ReportedAsFinished || from == Started
	case CmdCancel:
		return Cancelled, from == Created || from == Estimated || from == Scheduled || from == Released
	case CmdResetStatus:
		return previous(from)
	}
	return "", false
}

// previous is the status a reset returns to: the one before, for every
// status that a reset may leave (not Ended and not Cancelled).
func previous(from Status) (Status, bool) {
	switch from {
	case Estimated:
		return Created, true
	case Scheduled:
		return Estimated, true
	case Released:
		return Scheduled, true
	case Started:
		return Released, true
	case ReportedAsFinished:
		return Started, true
	}
	return "", false
}

// ResetTo returns to when a `:reset-status` may move an order from `from` back to
// it (FR-21.9): any status of the forward lifecycle earlier than `from`. Ended
// and Cancelled orders cannot be reset.
func ResetTo(from, to Status) (Status, error) {
	f, t := rank(from), rank(to)
	if f <= 0 || t < 0 || t >= f {
		return "", &Error{Code: CodeIllegalTransition, Detail: fmt.Sprintf("reset from %s to %s is not allowed", from, to)}
	}
	return to, nil
}

// CheckReportFinished applies the report-as-finished guards: the good quantity
// must be positive (BR-21.1, qty_raf > 0), the error quantity not negative, and
// everything reported so far within quantity × (1 + tolerance/100) (FR-21.7, PROD-0008).
func CheckReportFinished(orderQty, rafSoFar, errorSoFar, good, errorQty, tolerancePct decimal.Decimal) error {
	if !good.IsPositive() || errorQty.IsNegative() {
		return &Error{Code: CodeInvalidInput, Detail: "report as finished needs a positive good quantity and a non-negative error quantity"}
	}
	limit := orderQty.Mul(one.Add(tolerancePct.Div(hundred)))
	reported := rafSoFar.Add(errorSoFar).Add(good).Add(errorQty)
	if reported.GreaterThan(limit) {
		remaining := decimal.Max(orderQty.Sub(rafSoFar).Sub(errorSoFar), decimal.Zero)
		return &Error{Code: CodeRAFExceeds, Detail: fmt.Sprintf("RAF quantity %s exceeds remaining %s plus tolerance %s%%", good.Add(errorQty), remaining, tolerancePct)}
	}
	return nil
}

// CheckEdit applies the header edit rules of PATCH (PRD 4.21.5): the quantity
// can change only before the estimate is scheduled (Created or Estimated, the
// order is re-exploded and re-estimated), and the delivery date until the order
// is Released. Started and later orders keep their header.
func CheckEdit(status Status, quantity, deliveryDate bool) error {
	if quantity && status != Created && status != Estimated {
		return &Error{Code: CodeNotEditable, Detail: fmt.Sprintf("the quantity of a %s order cannot change; reset it to Estimated first", status)}
	}
	if deliveryDate && (rank(status) < 0 || rank(status) > rank(Released)) {
		return &Error{Code: CodeNotEditable, Detail: fmt.Sprintf("the delivery date of a %s order cannot change", status)}
	}
	return nil
}

// CheckEnd applies the End guards of BR-21.1. From Started the order may end
// only with no output and the zero-output duty; from ReportedAsFinished the
// output is already booked, so no further check applies here.
func CheckEnd(from Status, qtyRAF decimal.Decimal, zeroOutputDuty bool) error {
	if from != Started {
		return nil
	}
	if !qtyRAF.IsZero() {
		return &Error{Code: CodeNotRAFed, Detail: "a started order with output must be reported as finished before it ends"}
	}
	if !zeroOutputDuty {
		return &Error{Code: CodeZeroOutput, Detail: `ending with zero output requires the duty "End production order with zero output"`}
	}
	return nil
}
