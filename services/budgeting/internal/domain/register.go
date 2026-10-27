package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// EntryType is a budget register entry type (FR-38.1). This slice accepts the
// budget-moving types; pre-encumbrances and encumbrances arrive from checks
// and reservations.
type EntryType string

const (
	EntryOriginal     EntryType = "Original"
	EntryRevision     EntryType = "Revision"
	EntryTransfer     EntryType = "Transfer"
	EntryCarryForward EntryType = "CarryForward"
)

// ValidEntryType reports whether t is accepted by the register.
func ValidEntryType(t EntryType) bool {
	switch t {
	case EntryOriginal, EntryRevision, EntryTransfer, EntryCarryForward:
		return true
	}
	return false
}

// RegisterLine is one amount added to a control combination in a time span.
type RegisterLine struct {
	DimensionKey string          `json:"dimensionKey"`
	SpanKey      string          `json:"spanKey"`
	Amount       decimal.Decimal `json:"amount"`
}

// ValidateEntry enforces the entry invariants: at least one line, and a
// transfer whose lines net to zero (CO-0604).
func ValidateEntry(t EntryType, lines []RegisterLine) error {
	if !ValidEntryType(t) {
		return &Error{Code: "COM-0001", Detail: fmt.Sprintf("entry type %q is not accepted", t)}
	}
	if len(lines) == 0 {
		return &Error{Code: "COM-0001", Detail: "an entry needs at least one line"}
	}
	if t != EntryTransfer {
		return nil
	}
	net := decimal.Zero
	for _, l := range lines {
		net = net.Add(l.Amount)
	}
	if !net.IsZero() {
		return &Error{Code: CodeTransferUnbalanced, Detail: fmt.Sprintf("transfer lines net to %s", net.StringFixed(4))}
	}
	return nil
}
