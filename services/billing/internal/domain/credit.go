package domain

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// CodeCreditExceeded is BIL-0005: the credit exceeds what the original invoice
// still allows, or the original cannot be credited at all (FR-29.5).
const CodeCreditExceeded = "BIL-0005"

// CanBeCredited reports whether an invoice can be the original of a credit
// note: it must be posted (or settled since) and must not be a credit note.
func CanBeCredited(status, invoiceType string) error {
	if invoiceType == TypeCreditNote {
		return &Error{Code: CodeCreditExceeded, Detail: "a credit note cannot itself be credited"}
	}
	switch status {
	case StatusPosted, StatusPartiallySettled, StatusSettled:
		return nil
	}
	return &Error{Code: CodeCreditExceeded, Detail: "only a posted invoice can be credited"}
}

// PostedLine is a line of the original invoice with its rounded net and tax.
type PostedLine struct {
	Line
	Net decimal.Decimal
	Tax decimal.Decimal
}

// Credited is what posted credit notes and active reservations already took
// from one original line; ReservedQty and ReservedTax are the reserved share.
type Credited struct {
	Qty         decimal.Decimal
	Net         decimal.Decimal
	Tax         decimal.Decimal
	ReservedQty decimal.Decimal
	ReservedTax decimal.Decimal
}

// CreditUsage is what posted credit notes and active credit reservations
// already took from an original invoice: per original line number, the header
// charges and the gross. Reserved is the gross share held by reservations.
type CreditUsage struct {
	Lines    map[int]Credited
	Charges  decimal.Decimal
	Gross    decimal.Decimal
	Reserved decimal.Decimal
}

func (u CreditUsage) line(no int) Credited {
	if c, ok := u.Lines[no]; ok {
		return c
	}
	return Credited{Qty: decimal.Zero, Net: decimal.Zero, Tax: decimal.Zero}
}

// RemainingGross is the gross the original can still be credited.
func (u CreditUsage) RemainingGross(originalGross decimal.Decimal) decimal.Decimal {
	return originalGross.Sub(u.Gross)
}

// CreditQty asks to credit Qty of original line LineNo.
type CreditQty struct {
	LineNo int
	Qty    decimal.Decimal
}

// CreditLine is one line of a credit note: a copy of the original line with
// the credited quantity, net and tax, and the original line it reverses.
type CreditLine struct {
	OriginalLineNo int
	Line           Line
	Net            decimal.Decimal
	Tax            decimal.Decimal
}

// PlanCredit builds the lines and totals of a credit note against an original
// invoice (FR-29.5). With no requested lines the whole remainder is credited:
// every line's remaining quantity and the remaining header charges. Otherwise
// only the requested quantities are credited, plus charges when given.
//
// Tax is never re-quoted: a credit reverses the original's tax. A partial
// quantity takes the line net at that quantity and the original tax pro rata,
// both rounded half-even to the minor unit; the last remaining quantity of a
// line takes exactly what is left, so crediting a line in parts sums to the
// original line to the cent. Anything beyond the remainder is BIL-0005.
func PlanCredit(currency string, original []PostedLine, originalTotals Totals, used CreditUsage, req []CreditQty, charges *decimal.Decimal) ([]CreditLine, Totals, error) {
	exp := money.MinorUnits(currency)
	byNo := make(map[int]PostedLine, len(original))
	for _, l := range original {
		byNo[l.No] = l
	}
	remainingCharges := originalTotals.Charges.Sub(used.Charges)
	if len(req) == 0 {
		if charges != nil {
			return nil, Totals{}, &Error{Code: "COM-0001", Detail: "a charge amount can only be given with the lines to credit"}
		}
		for _, l := range original {
			if left := l.Qty.Sub(used.line(l.No).Qty); left.IsPositive() {
				req = append(req, CreditQty{LineNo: l.No, Qty: left})
			}
		}
		if remainingCharges.IsPositive() {
			charges = &remainingCharges
		}
		if len(req) == 0 && charges == nil {
			return nil, Totals{}, &Error{Code: CodeCreditExceeded, Detail: "the invoice is already fully credited"}
		}
	}
	seen := map[int]bool{}
	out := make([]CreditLine, 0, len(req))
	totals := Totals{Net: decimal.Zero, Charges: decimal.Zero, Tax: decimal.Zero}
	for _, r := range req {
		orig, ok := byNo[r.LineNo]
		switch {
		case !ok:
			return nil, Totals{}, &Error{Code: "COM-0001", Detail: fmt.Sprintf("line %d is not a line of the original invoice", r.LineNo)}
		case seen[r.LineNo]:
			return nil, Totals{}, &Error{Code: "COM-0001", Detail: fmt.Sprintf("line %d is listed twice", r.LineNo)}
		case !r.Qty.IsPositive():
			return nil, Totals{}, &Error{Code: "COM-0001", Detail: fmt.Sprintf("line %d: the credited quantity must be positive", r.LineNo)}
		}
		seen[r.LineNo] = true
		done := used.line(r.LineNo)
		leftQty, leftNet, leftTax := orig.Qty.Sub(done.Qty), orig.Net.Sub(done.Net), orig.Tax.Sub(done.Tax)
		if r.Qty.GreaterThan(leftQty) {
			return nil, Totals{}, &Error{Code: CodeCreditExceeded,
				Detail: fmt.Sprintf("Credit exceeds remaining creditable amount: line %d has %s left to credit", r.LineNo, leftQty.String())}
		}
		net, tax := leftNet, leftTax
		if r.Qty.LessThan(leftQty) {
			partial := orig.Line
			partial.Qty = r.Qty
			net = decimal.Min(partial.Net(currency), leftNet)
			tax = decimal.Min(orig.Tax.Mul(r.Qty).Div(orig.Qty).RoundBank(exp), leftTax)
		}
		line := orig.Line
		line.No = len(out) + 1
		line.Qty = r.Qty
		out = append(out, CreditLine{OriginalLineNo: r.LineNo, Line: line, Net: net, Tax: tax})
		totals.Net = totals.Net.Add(net)
		totals.Tax = totals.Tax.Add(tax)
	}
	if charges != nil {
		if charges.IsNegative() || charges.RoundBank(exp).GreaterThan(remainingCharges) {
			return nil, Totals{}, &Error{Code: CodeCreditExceeded,
				Detail: "Credit exceeds remaining creditable amount: charges have " + remainingCharges.StringFixed(exp) + " left to credit"}
		}
		totals.Charges = charges.RoundBank(exp)
	}
	totals.Gross = totals.Net.Add(totals.Charges).Add(totals.Tax)
	if !totals.Gross.IsPositive() {
		return nil, Totals{}, &Error{Code: "COM-0001", Detail: "a credit note must credit a positive amount"}
	}
	if err := VerifyCredit(currency, original, originalTotals, used, out, totals); err != nil {
		return nil, Totals{}, err
	}
	return out, totals, nil
}

// VerifyCredit re-checks a credit note against the original and what posted
// credit notes already took: every credited quantity, net and tax must fit its
// original line, the charges the original charges, and the gross the
// remaining creditable amount (BIL-0005). It runs again at post, under the
// original's row lock, because another credit note may have posted since.
func VerifyCredit(currency string, original []PostedLine, originalTotals Totals, used CreditUsage, lines []CreditLine, totals Totals) error {
	exp := money.MinorUnits(currency)
	byNo := make(map[int]PostedLine, len(original))
	for _, l := range original {
		byNo[l.No] = l
	}
	for _, c := range lines {
		orig, ok := byNo[c.OriginalLineNo]
		if !ok {
			return &Error{Code: CodeCreditExceeded, Detail: fmt.Sprintf("credit line %d references no line of the original invoice", c.Line.No)}
		}
		done := used.line(c.OriginalLineNo)
		if c.Line.Qty.GreaterThan(orig.Qty.Sub(done.Qty)) || c.Net.GreaterThan(orig.Net.Sub(done.Net)) || c.Tax.GreaterThan(orig.Tax.Sub(done.Tax)) {
			return &Error{Code: CodeCreditExceeded,
				Detail: fmt.Sprintf("Credit exceeds remaining creditable amount: line %d has %s left to credit", c.OriginalLineNo, orig.Net.Sub(done.Net).Add(orig.Tax.Sub(done.Tax)).StringFixed(exp))}
		}
	}
	if totals.Charges.GreaterThan(originalTotals.Charges.Sub(used.Charges)) {
		return &Error{Code: CodeCreditExceeded,
			Detail: "Credit exceeds remaining creditable amount: charges have " + originalTotals.Charges.Sub(used.Charges).StringFixed(exp) + " left to credit"}
	}
	if remaining := used.RemainingGross(originalTotals.Gross); totals.Gross.GreaterThan(remaining) {
		return &Error{Code: CodeCreditExceeded, Detail: "Credit exceeds remaining creditable amount " + remaining.StringFixed(exp)}
	}
	return nil
}
