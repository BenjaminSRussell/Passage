package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Error codes raised by the statement and matching rules (see README).
const (
	CodeStatementFormat    = "TRS-0493" // unparseable or structurally invalid statement file
	CodeStatementBalance   = "TRS-0494" // opening + Σ lines != closing
	CodeStatementCurrency  = "TRS-0408" // statement currency differs from the account currency
	CodeStatementDuplicate = "TRS-0405" // identical file already imported (returned as replay)
	CodeStatementConflict  = "TRS-0406" // same statement id, different closing balance
	CodeStatementGap       = "TRS-0407" // warning: electronic sequence skips numbers
	CodeIBANInvalid        = "TRS-0401"
)

// Error is a domain rule violation with a stable code. Parse errors say where
// the file failed: Line is the physical line of a BAI2 or MT940 file, Entry the
// position of the camt.053 Ntry element (XML has no useful line).
type Error struct {
	Code   string
	Detail string
	Line   int
	Entry  int
}

func (e *Error) Error() string {
	switch {
	case e.Line > 0:
		return fmt.Sprintf("%s (line %d): %s", e.Code, e.Line, e.Detail)
	case e.Entry > 0:
		return fmt.Sprintf("%s (entry %d): %s", e.Code, e.Entry, e.Detail)
	}
	return e.Code + ": " + e.Detail
}

// Line is one statement entry (BankStatementLine in the data model).
type Line struct {
	LineNo           int
	BookingDate      string
	ValueDate        string
	Amount           decimal.Decimal // signed: credits positive, debits negative
	Currency         string
	BTCCode          string // domain/family/subfamily
	EndToEndRef      string
	Reference        string // first unstructured remittance text
	BankReference    string // bank or servicer reference (MT940 :61: after //); never a payer reference
	CounterpartyName string
	RawFragment      string // source XML slice kept for audit
}

// MaxEntries is the FR-36.2 cap on entries per statement file.
const MaxEntries = 200000

// Statement is one parsed bank statement with its balances.
type Statement struct {
	Format        string
	StatementID   string
	Sequence      int    // electronic sequence number, 0 when the format has none
	Account       string // IBAN, or the bank's own account identifier when the file has none (BAI2, MT940 :25:, camt Othr/Id)
	Currency      string
	OpeningDate   string
	OpeningAmount decimal.Decimal
	ClosingDate   string
	ClosingAmount decimal.Decimal
	Lines         []Line
}

// CheckBalances enforces the statement invariant: opening + Σ lines = closing.
func CheckBalances(s Statement) error {
	sum := decimal.Zero
	for _, l := range s.Lines {
		sum = sum.Add(l.Amount)
	}
	if !s.OpeningAmount.Add(sum).Equal(s.ClosingAmount) {
		return &Error{Code: CodeStatementBalance, Detail: fmt.Sprintf("opening %s plus lines %s does not equal closing %s",
			s.OpeningAmount.StringFixed(2), sum.StringFixed(2), s.ClosingAmount.StringFixed(2))}
	}
	return nil
}

// Duplicate classifies a re-upload against an existing statement with the same
// statement id: identical content is a replay (TRS-0405), a different closing
// balance is a conflict (TRS-0406, BR-36.8).
func Duplicate(existingClosing, incomingClosing decimal.Decimal) error {
	if existingClosing.Equal(incomingClosing) {
		return nil
	}
	return &Error{Code: CodeStatementConflict, Detail: "statement exists with a different closing balance"}
}

// SequenceGap returns the TRS-0407 warning when an incoming electronic
// sequence number does not follow the highest earlier one of the account
// (FR-36.2, statement.sequenceGapPolicy = Warn). previous is nil for the first
// numbered statement, which never warns.
func SequenceGap(previous *int, incoming int) *Error {
	if previous == nil || incoming <= 0 || incoming == *previous+1 {
		return nil
	}
	return &Error{Code: CodeStatementGap, Detail: fmt.Sprintf("statement sequence gap: expected %d, received %d", *previous+1, incoming)}
}
