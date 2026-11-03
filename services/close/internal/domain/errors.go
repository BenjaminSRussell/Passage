package domain

import "time"

type PeriodRegistration struct {
	LegalEntityID  string
	LedgerID       string
	FiscalPeriodID string
	Start          time.Time
	End            time.Time
	Actor          string
}

const (
	CodeInvalidTransition   = "CLOSE-0001"
	CodeBusy                = "CLOSE-0002"
	CodeBlockedByDependency = "CLOSE-0003"
	CodeValidation          = "CLOSE-0004"
	CodeNoPeriod            = "CLOSE-0005"
	CodeAmbiguousPeriod     = "CLOSE-0006"
	CodeNoRun               = "CLOSE-0007"
	CodeLedgerRejected      = "CLOSE-0008"
	CodeLedgerData          = "CLOSE-0009"
	CodeRegroupingExists    = "CLOSE-0010"
	CodePeriodClosed        = "LEDGER-0013"
	CodePeriodOnHold        = "LEDGER-0014"
	CodeNoConvergence       = "ALLOC-0001"
	CodeZeroBasis           = "ALLOC-0002"
	CodeIdempotencyReuse    = "COM-0409"
)

type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

func fail(code, detail string) *Error { return &Error{Code: code, Detail: detail} }
