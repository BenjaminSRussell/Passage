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
// item: (PO unit price − standard unit cost) × qty, rounded half-even to the
// minor unit. A positive result is a debit (unfavourable) to the PPV account
// and a negative result a credit (AC-20.4).
func ReceiptPPV(qty, poUnit, standardUnit decimal.Decimal, scale int32) decimal.Decimal {
	return qty.Mul(poUnit.Sub(standardUnit)).RoundBank(scale)
}

// InvoicePPV is the purchase price variance at invoice: (invoice unit price −
// PO unit price) × qty, with the same sign convention as ReceiptPPV.
func InvoicePPV(qty, invoiceUnit, poUnit decimal.Decimal, scale int32) decimal.Decimal {
	return qty.Mul(invoiceUnit.Sub(poUnit)).RoundBank(scale)
}
