// Package domain holds the pure lean-manufacturing rules of wave 24: the kanban
// state machine, kanban quantities for fixed and event rules, flow validation
// and flow-period settlement. It performs no I/O.
package domain

import "fmt"

// Error is a rule violation carrying a stable code (see the service README).
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Codes. COM-0001 is malformed input; LEAN-* codes come from the PRD message
// catalog (4.24.18).
const (
	CodeInvalid     = "COM-0001"
	CodeMove        = "LEAN-0001"
	CodeCapacity    = "LEAN-0002"
	CodeOpenKanbans = "LEAN-0003"
	CodeNoOutput    = "LEAN-0004"
)

// MaxKanbansPerGeneration bounds one generation so a single request cannot
// allocate an unbounded slice.
const MaxKanbansPerGeneration = 10000

func errorf(code, format string, args ...any) error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}
