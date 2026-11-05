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

// CodeGRIRAllocation refuses an invoice whose GR/IR cannot be allocated from its receipt.
const CodeGRIRAllocation = "CST-GRIR-001"

// InvoiceGRIR is the GR/IR one invoice clears against its receipt, allocated by
// remainder: round(cumulative invoiced qty × PO price) less what earlier
// invoices cleared, and, on the invoice that completes the receipt, the
// receipt's stored GR/IR less that. Split invoices therefore clear exactly the
// receipt's GR/IR (3 received at 0.335 clear 1.00; invoiced 1 + 2 they clear
// 0.34 + 0.66, where rounding each document would clear 0.34 + 0.67). An invoice
// beyond the received quantity, or at a PO price the receipt's GR/IR was not
// cleared at, is refused rather than posted.
func InvoiceGRIR(receiptQty, receiptGRIR, priorQty, priorGRIR, qty, poUnit decimal.Decimal, scale int32) (decimal.Decimal, error) {
	if !receiptQty.Mul(poUnit).RoundBank(scale).Equal(receiptGRIR) {
		return decimal.Zero, &Error{CodeGRIRAllocation, "poUnitPrice is not the PO price the receipt cleared GR/IR at"}
	}
	cum := priorQty.Add(qty)
	switch cum.Cmp(receiptQty) {
	case 1:
		return decimal.Zero, &Error{CodeGRIRAllocation, "the invoices against the receipt would exceed its received quantity " + receiptQty.String()}
	case 0:
		return receiptGRIR.Sub(priorGRIR), nil
	}
	return cum.Mul(poUnit).RoundBank(scale).Sub(priorGRIR), nil
}
