package domain

import (
	"strings"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

// Invoice submissions by received line (#511, BR-20.11). A vendor submits an invoice naming the received lines it bills; accounts
// payable reviews it and keys the invoice. The submission holds the quantities until it is decided.
const (
	CodeSubmissionInvalid   = "SUP-INV-001"
	CodeSubmissionLine      = "SUP-INV-002"
	CodeSubmissionOverQty   = "SUP-INV-003"
	CodeSubmissionDuplicate = "SUP-INV-004"
	CodeSubmissionState     = "SUP-INV-005"

	SubmissionSubmitted = "SUBMITTED"
	SubmissionWithdrawn = "WITHDRAWN"
	SubmissionRejected  = "REJECTED"
	SubmissionPosted    = "POSTED"

	MaxSubmissionLines = 200
)

// SubmissionLine is one received line a vendor bills.
type SubmissionLine struct {
	ReceiptID     string          `json:"receiptId"`
	ReceiptLineNo int             `json:"receiptLineNo"`
	Qty           decimal.Decimal `json:"qty"`
	UnitPrice     decimal.Decimal `json:"unitPrice"`
	Amount        decimal.Decimal `json:"amount"`
}

// CheckSubmission validates the shape of a submission and returns the problems by field. Whether the lines are the vendor's and
// still uninvoiced is the store's check, made under lock.
func CheckSubmission(number string, lines []SubmissionLine, note string) map[string]string {
	errs := map[string]string{}
	if n := utf8.RuneCountInString(strings.TrimSpace(number)); n < 1 || n > 64 {
		errs["invoiceNumber"] = "is required, at most 64 characters"
	}
	if utf8.RuneCountInString(note) > 1000 {
		errs["note"] = "must be at most 1000 characters"
	}
	if len(lines) < 1 || len(lines) > MaxSubmissionLines {
		errs["lines"] = "must hold 1 to 200 lines"
		return errs
	}
	seen := map[[2]any]bool{}
	for _, l := range lines {
		key := [2]any{l.ReceiptID, l.ReceiptLineNo}
		switch {
		case seen[key]:
			errs["lines"] = "names a received line twice"
		case l.ReceiptLineNo < 1:
			errs["lines"] = "a line needs a receipt line number"
		case !l.Qty.IsPositive():
			errs["lines"] = "a line needs a positive quantity"
		case l.UnitPrice.IsNegative():
			errs["lines"] = "a line needs a unit price of zero or more"
		}
		seen[key] = true
	}
	return errs
}
