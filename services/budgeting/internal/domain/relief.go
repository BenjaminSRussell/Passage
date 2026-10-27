package domain

import "github.com/shopspring/decimal"

// Relief is the amount an invoice relieves from a reservation (FR-38.5):
// min(matched share, remaining reservation), never negative. The matched share
// is invoice quantity times the PO unit price, so price variances stay out of
// relief and are checked as a new amount (BR-38.4).
func Relief(reserved, relieved, matchedShare decimal.Decimal) decimal.Decimal {
	remaining := reserved.Sub(relieved)
	if remaining.Sign() <= 0 || matchedShare.Sign() <= 0 {
		return decimal.Zero
	}
	if matchedShare.LessThan(remaining) {
		return matchedShare
	}
	return remaining
}

// Excess is the part of an invoice amount above its relief; it runs a new check.
func Excess(invoiceAmount, relief decimal.Decimal) decimal.Decimal {
	e := invoiceAmount.Sub(relief)
	if e.Sign() < 0 {
		return decimal.Zero
	}
	return e
}

// Encumbrance statuses (PRD 7.38.4).
const (
	StatusOpen              = "Open"
	StatusPartiallyRelieved = "PartiallyRelieved"
	StatusRelieved          = "Relieved"
	StatusReleased          = "Released"
)

// Release is the amount a PO line cancellation or a PO close gives back to
// available funds (FR-38.5): the open remainder of the reservation, never
// negative. What invoices already relieved stays relieved.
func Release(reserved, relieved, released decimal.Decimal) decimal.Decimal {
	open := reserved.Sub(relieved).Sub(released)
	if open.Sign() <= 0 {
		return decimal.Zero
	}
	return open
}

// EncumbranceStatus derives the status from the totals. A release is final:
// once a residual is released the line is closed and stays Released. Without
// one, nothing relieved is Open, everything relieved is Relieved, otherwise
// partial.
func EncumbranceStatus(reserved, relieved, released decimal.Decimal) string {
	switch {
	case released.Sign() > 0:
		return StatusReleased
	case relieved.Sign() <= 0 && reserved.Sign() > 0:
		return StatusOpen
	case relieved.GreaterThanOrEqual(reserved):
		return StatusRelieved
	default:
		return StatusPartiallyRelieved
	}
}
