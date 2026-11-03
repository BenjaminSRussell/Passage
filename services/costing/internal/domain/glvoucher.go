package domain

import "github.com/shopspring/decimal"

type VoucherLine struct {
	Account string          `json:"account"`
	Credit  bool            `json:"credit"`
	Amount  decimal.Decimal `json:"amount"`
}

type PostingAccounts struct {
	Inventory, GRIR, AP, PPV, Adjustment string
}

func appendLine(lines []VoucherLine, account string, credit bool, amount decimal.Decimal) []VoucherLine {
	if amount.IsZero() {
		return lines
	}
	return append(lines, VoucherLine{Account: account, Credit: credit, Amount: amount})
}

func ppvLine(lines []VoucherLine, a PostingAccounts, ppv decimal.Decimal) []VoucherLine {
	return appendLine(lines, a.PPV, ppv.IsNegative(), ppv.Abs())
}

// ReceiptVoucher books a goods receipt of a STANDARD item: inventory at standard (the clearing amount less
// the variance) and the variance against GR/IR at the PO price. A positive ppv is a debit.
func ReceiptVoucher(a PostingAccounts, grir, ppv decimal.Decimal) []VoucherLine {
	lines := appendLine(nil, a.Inventory, false, grir.Sub(ppv))
	lines = ppvLine(lines, a, ppv)
	return appendLine(lines, a.GRIR, true, grir)
}

// InvoiceVoucher clears GR/IR against accounts payable, with the invoice-to-PO variance on the PPV account.
func InvoiceVoucher(a PostingAccounts, grir, ppv decimal.Decimal) []VoucherLine {
	lines := appendLine(nil, a.GRIR, false, grir)
	lines = ppvLine(lines, a, ppv)
	return appendLine(lines, a.AP, true, grir.Add(ppv))
}

// CloseVoucher books the net settlement adjustment of a close: a positive total raises the inventory.
func CloseVoucher(a PostingAccounts, total decimal.Decimal) []VoucherLine {
	lines := appendLine(nil, a.Inventory, total.IsNegative(), total.Abs())
	return appendLine(lines, a.Adjustment, total.IsPositive(), total.Abs())
}
