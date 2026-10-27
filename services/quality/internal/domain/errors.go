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

	// Slice 2: PRD message catalog (BR-33.11, BR-33.13, BR-33.14).
	CodeReportingHit = "QM-0121" // warning: a reporting-list substance is over its threshold
	CodeRestricted   = "QM-0122" // restricted-list hit: error under BLOCK, warning under WARN
	CodeCertRequired = "QM-0125" // required incoming certificate missing, NOK or not waived
	CodeLatePull     = "QM-0126" // stability pull recorded beyond the window

	// Slice 2: service codes beyond the catalog (see README).
	CodeListRace            = "QM-0160" // concurrent version of the same compliance list
	CodeStudyMissing        = "QM-0161"
	CodeCompositionTooLarge = "QM-0162" // BOM over 500 items or 20 levels
	CodeCompositionRace     = "QM-0163" // concurrent composition version of the same item
	CodeCompositionMissing  = "QM-0164"
	CodeCompositionCycle    = "QM-0165" // the declared BOM loops back on itself
	CodeSPCUnsupported      = "QM-0166" // the results cannot form the requested chart
	CodeStudyRace           = "QM-0167" // the same Idempotency-Key is creating a study concurrently
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
