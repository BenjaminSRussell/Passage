// Package domain holds the pure payroll engine: rule sets, tax functions,
// gross-to-net statement calculation, deduction arrears, retro deltas and the
// pay-run lifecycle rules. It performs no I/O.
package domain

import "fmt"

// Stable error codes returned by the domain (see README error table).
const (
	CodeNoRule        = "PAY-RULE-001"
	CodeRuleSoD       = "PAY-RULE-002"
	CodeRunSoD        = "PAY-RUN-004"
	CodeRunTransition = "PAY-RUN-005"
	CodeInvalidInput  = "PAY-RUN-006"
)

// Error is a domain rule violation with a stable code.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }
