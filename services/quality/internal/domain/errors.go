// Package domain holds the quality rules as pure functions and value types.
// Nothing here performs I/O.
package domain

import "fmt"

// Service error codes (see README). Codes from the PRD catalog keep their QM-01xx
// numbers; codes the slice needs beyond it are listed in the README.
const (
	CodeValidation        = "COM-0001"
	CodeNoAssociation     = "QM-0100"
	CodeInstrumentOverdue = "QM-0102"
	CodeQtySplit          = "QM-0104"
	CodeRequiredNoResult  = "QM-0105"
	CodeClosed            = "QM-0109"
	CodeResultDuplicate   = "QM-0111"
	CodeInstrumentUnknown = "QM-0113"
	CodeTableNotLoaded    = "QM-0114"
	CodeStatus            = "QM-0115"
	CodeSourceLineDupe    = "QM-0116"
	CodeTestGroupMissing  = "QM-0140"
	CodeOrderMissing      = "QM-0141"
	CodeInstrumentMissing = "QM-0142"
	CodeInstrumentDupe    = "QM-0151"
)

// Error is a rule violation with a stable code. Reason is an optional machine
// token (for example INSTRUMENT_OVERDUE) carried in the problem's errors map.
type Error struct {
	Code   string
	Detail string
	Reason string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Detail) }

func invalid(detail string) *Error { return &Error{Code: CodeValidation, Detail: detail} }

func fail(code, detail string) *Error { return &Error{Code: code, Detail: detail} }
