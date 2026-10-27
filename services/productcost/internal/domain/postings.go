package domain

import "github.com/shopspring/decimal"

// Account prefixes and clearing accounts for component-split postings
// (FR-39.15). The GL mapping lives in posting profiles.
const (
	AccountCOGSPrefix     = "COGS-"
	AccountPPVPrefix      = "PPV-"
	AccountPriceDiffClear = "PriceDifferenceClearing"
)

// sideLine books amount on the debit side when positive and on the credit side
// when negative, so reversals and favorable variances stay balanced.
func sideLine(account string, amount decimal.Decimal) JournalLine {
	if amount.IsNegative() {
		return JournalLine{Account: account, Credit: amount.Neg()}
	}
	return JournalLine{Account: account, Debit: amount}
}

// CostOfSalesLines books the cost of goods sold of an issue per cost component
// (FR-39.15): Dr COGS-<component> for each non-zero part and Cr Inventory for
// the total, rounded to scale decimals (PostingScale). Component parts sum to
// the total to the minor unit (BR-39.13), so the journal balances. The total
// is returned for the caller's reconciliation.
func CostOfSalesLines(qty decimal.Decimal, unit Split, scale int32) ([]JournalLine, decimal.Decimal) {
	parts, total := IssueCOGS(qty, unit, scale)
	lines := make([]JournalLine, 0, len(parts)+1)
	for _, c := range parts.Components() {
		if parts[c].IsZero() {
			continue
		}
		lines = append(lines, sideLine(AccountCOGSPrefix+c, parts[c]))
	}
	lines = append(lines, sideLine(AccountInventory, total.Neg()))
	return lines, total
}

// PriceDifferenceLines books a purchase price variance per cost component of the
// purchased item's standard (FR-39.7, FR-39.15): Dr PPV-<component> for an
// unfavorable part, Cr for a favorable one, with the total offset to the
// price-difference clearing account. PPV assigns its rounding residual to the
// largest component, so the parts sum to the total.
func PriceDifferenceLines(qty, receiptUnit decimal.Decimal, standard Split, scale int32) ([]JournalLine, decimal.Decimal) {
	total, parts := PPV(qty, receiptUnit, standard, scale)
	lines := make([]JournalLine, 0, len(parts)+1)
	for _, c := range parts.Components() {
		if parts[c].IsZero() {
			continue
		}
		lines = append(lines, sideLine(AccountPPVPrefix+c, parts[c]))
	}
	if !total.IsZero() {
		lines = append(lines, sideLine(AccountPriceDiffClear, total.Neg()))
	}
	return lines, total
}
