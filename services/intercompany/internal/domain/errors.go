// Package domain holds the pure intercompany rules: trading partners, the
// accounting matrix and journal derivation, the mirror-document saga, the
// reconciliation classifier and IC charge pricing. It performs no I/O.
package domain

import "fmt"

// Error is a rule violation with a stable code from the wave-40 catalog.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }

// Codes raised by the domain. Codes not in the PRD catalog are listed in NOT-IMPLEMENTED.md.
const (
	CodeInvalid       = "COM-0001"
	CodeNoPolicy      = "IC-0801"
	CodeNoMatrix      = "IC-0805"
	CodeTransferPrice = "IC-0821"
	CodeUnbalanced    = "IC-0830"
)

func invalid(detail string) error { return &Error{Code: CodeInvalid, Detail: detail} }
