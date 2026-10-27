// Package domain holds the transportation rules as pure functions: rating,
// container numbers, freight invoice matching, landed-cost allocation and the
// tender cascade. It performs no I/O.
package domain

// Error is a rule violation carrying a message-catalog code (TMS-nnnn).
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Message-catalog codes (PRD §6.34.18). TMS-0100 and TMS-0111 to TMS-0114 are
// slice-local: the catalog leaves 0111 to 0120 unassigned and has no not-found
// code. The README lists them.
const (
	CodeNotFound           = "TMS-0100"
	CodeBillInvoiced       = "TMS-0111"
	CodeInvoiceMismatch    = "TMS-0112"
	CodeTenderNotPending   = "TMS-0113"
	CodeTenderOpen         = "TMS-0114"
	CodeNoValidRateCard    = "TMS-0101"
	CodeRateCardOverlap    = "TMS-0102"
	CodeTenderExpired      = "TMS-0105"
	CodeTenderFailed       = "TMS-0106"
	CodeDuplicateInvoice   = "TMS-0108"
	CodeUnexpectedInvoice  = "TMS-0109"
	CodeAllocationResidual = "TMS-0110"
	CodeContainerCheck     = "TMS-0124"
)
