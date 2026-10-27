// Package domain holds the pure billing rules of wave 29: invoice totals with
// per-currency rounding (FR-29.3, BR-29.3), installment splits (BR-29.11),
// daily proration (BR-29.8) and the invoice state rules (BR-29.7). It performs
// no I/O.
package domain

import (
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Invoice statuses (BR-29.7).
const (
	StatusProposal         = "PROPOSAL"
	StatusPosted           = "POSTED"
	StatusPartiallySettled = "PARTIALLY_SETTLED"
	StatusSettled          = "SETTLED"
	StatusReversed         = "REVERSED"
)

// Invoice types (FR-29.1 to FR-29.5).
const (
	TypeShipment     = "SHIPMENT"
	TypeConsolidated = "CONSOLIDATED"
	TypeFreeText     = "FREE_TEXT"
	TypeSubscription = "SUBSCRIPTION"
	TypeMilestone    = "MILESTONE"
	TypeCreditNote   = "CREDIT_NOTE"
)

// TaxRecheckTolerance is the amount the tax may move between proposal and post
// before the post is refused (FR-29.6, parameter tax_recheck_tolerance, 0.01).
func TaxRecheckTolerance() decimal.Decimal { return decimal.New(1, -2) }

// Error is a rule violation with a stable Passage code (BIL-xxxx).
type Error struct {
	Code   string
	Detail string
}

func (e *Error) Error() string { return e.Code + ": " + e.Detail }

// Line is one invoice line before tax. Discount is a percentage (0 to 100);
// TaxCode is the tax code the tax engine quotes the line with (FR-29.4).
type Line struct {
	No             int
	Description    string
	Qty            decimal.Decimal
	UnitPrice      decimal.Decimal
	DiscountPct    decimal.Decimal
	RevenueAccount string
	TaxCode        string
}

// MaxDescription bounds a line description in runes.
const MaxDescription = 200

// Validate checks a line against the proposal rules (BIL-0004 for a missing account).
func (l Line) Validate() error {
	switch {
	case l.Description == "" || len([]rune(l.Description)) > MaxDescription:
		return &Error{Code: "COM-0001", Detail: "line description is required and at most 200 characters"}
	case !l.Qty.IsPositive():
		return &Error{Code: "COM-0001", Detail: "line quantity must be positive"}
	case l.UnitPrice.IsNegative():
		return &Error{Code: "COM-0001", Detail: "line unit price must not be negative"}
	case l.DiscountPct.IsNegative() || l.DiscountPct.GreaterThan(decimal.NewFromInt(100)):
		return &Error{Code: "COM-0001", Detail: "line discount must be between 0 and 100 percent"}
	case l.RevenueAccount == "":
		return &Error{Code: "BIL-0004", Detail: "line has no revenue account"}
	case l.TaxCode == "":
		return &Error{Code: "COM-0001", Detail: "line tax code is required"}
	}
	return nil
}

// Net returns line_net = qty × unit price × (1 − discount/100), rounded half-even
// to the currency's minor unit (BR-29.3). Rounding happens once per line.
func (l Line) Net(currency string) decimal.Decimal {
	factor := decimal.NewFromInt(100).Sub(l.DiscountPct).Div(decimal.NewFromInt(100))
	return l.Qty.Mul(l.UnitPrice).Mul(factor).RoundBank(money.MinorUnits(currency))
}

// Totals is the header amount set: gross = net + charges + tax.
type Totals struct {
	Net     decimal.Decimal
	Charges decimal.Decimal
	Tax     decimal.Decimal
	Gross   decimal.Decimal
}

// Compute returns the line nets and the invoice totals. tax holds one tax amount
// per line from the tax engine (LINE rounding, BR-29.3); charges are header
// charges. Every amount is rounded to the currency's minor unit.
func Compute(currency string, lines []Line, tax []decimal.Decimal, charges decimal.Decimal) ([]decimal.Decimal, Totals, error) {
	if len(tax) != len(lines) {
		return nil, Totals{}, &Error{Code: "BIL-0001", Detail: "tax engine returned a different number of lines"}
	}
	exp := money.MinorUnits(currency)
	nets := make([]decimal.Decimal, len(lines))
	t := Totals{Net: decimal.Zero, Charges: charges.RoundBank(exp), Tax: decimal.Zero}
	for i, l := range lines {
		nets[i] = l.Net(currency)
		t.Net = t.Net.Add(nets[i])
		t.Tax = t.Tax.Add(tax[i].RoundBank(exp))
	}
	t.Gross = t.Net.Add(t.Charges).Add(t.Tax)
	return nets, t, nil
}

// TaxMoved reports whether a re-quoted tax total differs from the proposal's by
// more than TaxRecheckTolerance (FR-29.6, BIL-0002).
func TaxMoved(proposed, requoted decimal.Decimal) bool {
	return proposed.Sub(requoted).Abs().GreaterThan(TaxRecheckTolerance())
}

// CanReverse applies BR-29.7: a POSTED invoice reverses only on its posting date
// and only while it has no settlements; otherwise a credit note is required.
func CanReverse(status string, postingDate, today time.Time, settlements int) error {
	if status != StatusPosted {
		return &Error{Code: "BIL-0008", Detail: "only a posted invoice can be reversed"}
	}
	if settlements > 0 || !sameDay(postingDate, today) {
		return &Error{Code: "BIL-0008", Detail: "same-day reversal not allowed: issue a credit note"}
	}
	return nil
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
