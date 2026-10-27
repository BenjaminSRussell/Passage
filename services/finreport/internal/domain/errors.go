// Package domain holds the pure reporting rules: period expressions, the
// expression language of report rows and columns, definitions, the statement
// engine and the cash flow reconciliation. It performs no I/O.
package domain

import "fmt"

// Message catalog codes (PRD §2.15.18) used by the domain and returned by the API.
const (
	CodeNotPublished   = "REPORT-0001"
	CodeExpression     = "REPORT-0002"
	CodePeriodNotFound = "REPORT-0003"
	CodeUnclassified   = "REPORT-0009"
	CodeCashFlow       = "REPORT-0008"
	CodePYMissing      = "REPORT-0010"
	CodeImportDup      = "REPORT-0011"
	CodeImportSchema   = "REPORT-0012"
	CodeTooLarge       = "REPORT-0026"
)

// Error is a domain rule violation carrying a catalog code.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Detail: fmt.Sprintf(format, args...)}
}
