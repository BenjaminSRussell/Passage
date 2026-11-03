package domain

import "fmt"

// Error is a rule violation carrying a stable WMS code from the message
// catalog (PRD 6.31.18). The API maps it to a problem response.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Codes returned by the domain. Every other code in the catalog is raised by a
// later slice (waves, confirmations, quality, hazmat).
const (
	CodeNoLocation        = "WMS-0101"
	CodeCapacity          = "WMS-0102"
	CodeMixedItems        = "WMS-0103"
	CodeNestDepth         = "WMS-0104"
	CodeIllegalTransition = "WMS-0110"
	// CodeHoldBlocked is wave 33 AC2: a pick of quantity that is still held
	// (QUALITY_HOLD). Distinct from CodeQualityHold (WMS-0113), the cross-dock
	// warning that supply is not ACCEPTED.
	CodeHoldBlocked = "QUALITY_HOLD"
)

func errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}
