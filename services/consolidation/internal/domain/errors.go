// Package domain holds the pure consolidation rules: IAS 21 translation and
// CTA, the E1–E5 elimination entries with NCI, and ownership validation. It
// performs no I/O; runs, versions and the consolidated dataset are later slices.
package domain

import "fmt"

// Error is a rule violation with a stable code from the wave-40 catalog.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }

// Codes raised by the domain. CodeBargainPurchase is not in the PRD catalog and is listed in NOT-IMPLEMENTED.md.
const (
	CodeInvalid         = "COM-0001"
	CodeUnbalancedRule  = "CNS-0903"
	CodeOwnership       = "CNS-0904"
	CodeBargainPurchase = "CNS-0920"
)

func invalid(detail string) error { return &Error{Code: CodeInvalid, Detail: detail} }
