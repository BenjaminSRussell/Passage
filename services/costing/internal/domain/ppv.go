package domain

import "github.com/shopspring/decimal"

// CheckPPVInputs rejects a quantity that is not positive or a unit price that
// is negative (CST-PPV-001).
func CheckPPVInputs(qty decimal.Decimal, units ...decimal.Decimal) error {
	if !qty.IsPositive() {
		return &Error{CodeInvalidPPV, "quantity must be positive"}
	}
	for _, u := range units {
		if u.IsNegative() {
			return &Error{CodeInvalidPPV, "unit prices must not be negative"}
		}
	}
	return nil
}

// ReceiptPPV is the purchase price variance at goods receipt of a STANDARD
// item: the GR/IR amount round(qty × PO price) less the inventory at standard
// round(qty × standard cost), each rounded half-even to the minor unit, so the
// inventory is exactly the standard. A positive result is a debit
// (unfavourable) to the PPV account and a negative result a credit (AC-20.4).
func ReceiptPPV(qty, poUnit, standardUnit decimal.Decimal, scale int32) decimal.Decimal {
	return qty.Mul(poUnit).RoundBank(scale).Sub(qty.Mul(standardUnit).RoundBank(scale))
}

// InvoicePPV is the purchase price variance at invoice: the payable
// round(qty × invoice price) less the GR/IR amount round(qty × PO price) it
// clears, with the same sign convention as ReceiptPPV.
func InvoicePPV(qty, invoiceUnit, poUnit decimal.Decimal, scale int32) decimal.Decimal {
	return qty.Mul(invoiceUnit).RoundBank(scale).Sub(qty.Mul(poUnit).RoundBank(scale))
}
