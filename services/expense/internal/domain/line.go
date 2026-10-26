// Package domain holds the pure expense rules: policy evaluation, cash-advance
// settlement, travel-requisition variance and card-to-receipt matching. It
// performs no I/O.
package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// Who paid a line. Employee-paid lines reimburse the worker through AP; company
// card lines post against the card issuer vendor (FR-43.14).
const (
	PaidByEmployee    = "EMPLOYEE"
	PaidByCompanyCard = "COMPANY_CARD"
)

// Line is one expense line, amounts in the accounting currency.
type Line struct {
	ID            string
	Category      string
	TxnDate       time.Time
	Amount        decimal.Decimal
	Merchant      string
	Attendees     int
	HasReceipt    bool
	Justification string
	PaidBy        string
}

// Totals satisfies the report invariant: total = reimbursable + company-paid (8.43.4).
type Totals struct {
	Total        decimal.Decimal
	Reimbursable decimal.Decimal
	CompanyPaid  decimal.Decimal
}

// SumLines totals a report's lines. A line with no PaidBy is employee-paid.
func SumLines(lines []Line) Totals {
	t := Totals{Total: decimal.Zero, Reimbursable: decimal.Zero, CompanyPaid: decimal.Zero}
	for _, l := range lines {
		t.Total = t.Total.Add(l.Amount)
		if l.PaidBy == PaidByCompanyCard {
			t.CompanyPaid = t.CompanyPaid.Add(l.Amount)
		} else {
			t.Reimbursable = t.Reimbursable.Add(l.Amount)
		}
	}
	return t
}
