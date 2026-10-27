// Package domain holds the pure process-manufacturing rules of wave 24:
// formula scaling, potency consumption, co-product cost allocation, catch
// weight validation and shelf-life derivation. It performs no I/O.
package domain

import "fmt"

// Error is a rule violation carrying a stable code (see the service README).
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Codes returned by the domain. COM-0001 is a malformed input; PMF-* codes come
// from the wave-24 message catalog (PRD 4.24.18).
const (
	CodeInvalid      = "COM-0001"
	CodePercentSum   = "PMF-0001"
	CodeCompNegative = "PMF-0002"
	CodeCatchWeight  = "PMF-0004"
	CodeAllocation   = "PMF-0021"
)

func errorf(code, format string, args ...any) error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}
