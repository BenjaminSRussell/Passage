// Package domain holds the print service's pure rules: ZPL rendering, printer
// validation, queue failover and the print job state. It performs no I/O.
package domain

// Error is a rule violation with a stable code from the message catalog
// (roadmap §6.32.18) or a service-local PRN code.
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

const (
	CodeInvalid          = "PRN-0001" // request or template data invalid (service-local)
	CodeUnknownTemplate  = "PRN-0002" // template code is not built in (service-local)
	CodeDuplicatePrinter = "PRN-0003" // printer code already registered (service-local)
	CodeUnreachable      = "MOB-0111" // printer unreachable after the attempts
	CodeNoPrinter        = "MOB-0112" // no printer routed to the queue
	CodeTerminal         = "PRN-0004" // job already completed (service-local)
	CodeRankTaken        = "PRN-0006" // queue already has a device at this failover rank (service-local)
	CodeMediaMismatch    = "PRN-0007" // label does not fit the device's media (service-local)
	CodePrinterNotFound  = "PRN-0010" // printer not found or already retired (service-local)
	CodeRuleNotFound     = "PRN-0011" // routing rule not found (service-local)
)
