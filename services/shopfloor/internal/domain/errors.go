// Package domain holds the shop floor rules as pure functions: punch pairing and
// rounding (BR-25.1..25.3), pay lines (FR-25.7, BR-25.8) and OEE (BR-25.5).
// It performs no I/O.
package domain

// Error is a rule violation carrying a catalog code (SF-nnnn, see README).
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }
