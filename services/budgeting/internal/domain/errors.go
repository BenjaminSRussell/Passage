// Package domain holds the budget control rules as pure functions: no I/O, no
// database, no clock. The store applies their results under row locks.
package domain

import "fmt"

// Error is a domain rule violation carrying a stable message code (CO-06xx).
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }

// CodeTransferUnbalanced is CO-0604: transfer lines must net to zero.
const CodeTransferUnbalanced = "CO-0604"
