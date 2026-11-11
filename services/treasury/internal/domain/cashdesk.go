package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Cash desk rule codes (FR-36.16, see README).
const (
	CodeDeskOverdrawn   = "TRS-0540" // a payment would take the desk balance below zero
	CodeDeskOverLimit   = "TRS-0541" // a receipt would take the desk balance above its maximum
	CodeDeskDifference  = "TRS-0542" // the count differs from the book balance and no reason was given
	CodeDeskDayOpen     = "TRS-0543" // the desk already has an open day
	CodeDeskDayClosed   = "TRS-0544" // the day is closed or not open: no entries
	CodeDeskDateOrder   = "TRS-0545" // a day cannot be opened on or before the last closed day
	CodeDeskNotFound    = "TRS-0546"
	CodeDeskInactive    = "TRS-0547"
	CodeDeskDayNotFound = "TRS-0548"
)

// DeskBalance is the book balance of a day: the opening balance plus receipts less payments.
func DeskBalance(opening decimal.Decimal, entries []DeskEntry) decimal.Decimal {
	b := opening
	for _, e := range entries {
		if e.Kind == DeskReceipt {
			b = b.Add(e.Amount)
		} else {
			b = b.Sub(e.Amount)
		}
	}
	return b
}

// Desk entry kinds.
const (
	DeskReceipt = "receipt"
	DeskPayment = "payment"
)

// DeskEntry is one line of the cash journal: Amount is positive, Kind gives the direction.
type DeskEntry struct {
	Kind   string
	Amount decimal.Decimal
}

// CheckDeskEntry applies the balance rules to a new entry on a desk whose book balance is balance: a payment may not
// overdraw the desk (BR-36.12), a receipt may not take it above max.
func CheckDeskEntry(balance decimal.Decimal, e DeskEntry, max decimal.Decimal) error {
	switch e.Kind {
	case DeskPayment:
		if balance.Sub(e.Amount).IsNegative() {
			return &Error{Code: CodeDeskOverdrawn, Detail: fmt.Sprintf("the payment of %s would overdraw the desk, which holds %s", e.Amount, balance)}
		}
	case DeskReceipt:
		if balance.Add(e.Amount).GreaterThan(max) {
			return &Error{Code: CodeDeskOverLimit, Detail: fmt.Sprintf("the receipt of %s would take the desk above its maximum of %s (it holds %s); pay cash to the bank first", e.Amount, max, balance)}
		}
	default:
		return &Error{Code: "COM-0001", Detail: "kind must be receipt or payment"}
	}
	return nil
}

// CloseDifference is counted less book. A difference needs a reason, so a shortage or surplus is never closed silently.
func CloseDifference(book, counted decimal.Decimal, reason string) (decimal.Decimal, error) {
	diff := counted.Sub(book)
	if !diff.IsZero() && reason == "" {
		return diff, &Error{Code: CodeDeskDifference, Detail: fmt.Sprintf("the count of %s differs from the book balance of %s by %s; give the reason", counted, book, diff)}
	}
	return diff, nil
}

// Cash desk codes added with posting, transfers and the second signature.
const (
	CodeDeskUnposted      = "TRS-0552" // entries or a difference are not yet in the ledger: the day cannot close
	CodeDeskSameSigner    = "TRS-0553" // the second signature must come from another person
	CodeDeskNotClosing    = "TRS-0554" // the day is not waiting for a second signature
	CodeDeskHasOpenDay    = "TRS-0555" // a desk with an open day cannot be deactivated
	CodeDeskCountMismatch = "TRS-0556" // the counted denominations do not add up to the counted balance
)

// Denomination is one line of a cash count: a note or coin value and how many of them were counted.
type Denomination struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

const (
	maxDenominations     = 40
	maxDenominationCount = 1_000_000
)

// DenominationTotal is the sum of value x count over the lines. A value must be positive with at most places decimals,
// a value may appear once, and a count is 0 to one million.
func DenominationTotal(lines []Denomination, places int32) (decimal.Decimal, error) {
	if len(lines) > maxDenominations {
		return decimal.Zero, &Error{Code: "COM-0001", Detail: fmt.Sprintf("a count has at most %d denominations", maxDenominations)}
	}
	total := decimal.Zero
	seen := map[string]bool{}
	for _, l := range lines {
		v, err := decimal.NewFromString(l.Value)
		if err != nil || !v.IsPositive() || !v.Equal(v.Round(places)) {
			return decimal.Zero, &Error{Code: "COM-0001", Detail: fmt.Sprintf("denomination %q must be a positive amount with at most %d decimals", l.Value, places)}
		}
		if seen[v.String()] {
			return decimal.Zero, &Error{Code: "COM-0001", Detail: fmt.Sprintf("denomination %s appears twice", v)}
		}
		seen[v.String()] = true
		if l.Count < 0 || l.Count > maxDenominationCount {
			return decimal.Zero, &Error{Code: "COM-0001", Detail: "a denomination count must be 0 to 1000000"}
		}
		total = total.Add(v.Mul(decimal.NewFromInt(int64(l.Count))))
	}
	return total, nil
}

// SecondSigner checks that the person approving a close is not the person who counted.
func SecondSigner(closedBy, approver string) error {
	if closedBy == approver {
		return &Error{Code: CodeDeskSameSigner, Detail: "the second signature must come from a different person than the one who counted the cash"}
	}
	return nil
}
