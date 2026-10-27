package domain

import "github.com/shopspring/decimal"

// JournalLine is the slice of mart.fin_journal_line that the monetary metrics
// read. Amount is amount_reporting in the ledger's reporting currency (BR-46.1).
type JournalLine struct {
	AccountCategory string
	Status          string
	Amount          decimal.Decimal
}

// Revenue is -SUM(amount_reporting) over posted REVENUE lines: credits show
// positive. Reversals net to zero in the period they post (BR-46.3).
func Revenue(lines []JournalLine) decimal.Decimal {
	return sumPosted(lines, "REVENUE").Neg()
}

// COGS is SUM(amount_reporting) over posted COGS lines.
func COGS(lines []JournalLine) decimal.Decimal {
	return sumPosted(lines, "COGS")
}

// GrossMargin is revenue minus cost of goods sold.
func GrossMargin(revenue, cogs decimal.Decimal) decimal.Decimal {
	return revenue.Sub(cogs)
}

// GrossMarginPct is gross margin over revenue. It returns false, never infinity,
// when revenue is zero (BR-46.4 makes the ratio NULL).
func GrossMarginPct(revenue, cogs decimal.Decimal) (decimal.Decimal, bool) {
	if revenue.IsZero() {
		return decimal.Zero, false
	}
	return GrossMargin(revenue, cogs).Div(revenue), true
}

func sumPosted(lines []JournalLine, category string) decimal.Decimal {
	sum := decimal.Zero
	for _, l := range lines {
		if l.AccountCategory == category && l.Status == "POSTED" {
			sum = sum.Add(l.Amount)
		}
	}
	return sum
}
