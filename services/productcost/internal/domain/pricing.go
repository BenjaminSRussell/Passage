package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Account names used in journal previews. The GL mapping lives in posting profiles.
const (
	AccountInventory     = "Inventory"
	AccountCostChangeVar = "CostChangeVariance"
)

// CodeMixingRatioTotal is raised when mixing ratios do not total 100.00 (BR-39.12).
const CodeMixingRatioTotal = "CST-0741"

const mixingRatioTotalWanted = 100

// PPV returns the purchase price variance of a receipt (FR-39.7):
// (receipt unit cost − standard unit cost) × quantity, rounded to scale
// decimals (PostingScale). The total is split over the standard's components
// in proportion to their share of the standard; the rounding residual goes to
// the largest component (FR-39.15).
func PPV(qty, receiptUnit decimal.Decimal, standard Split, scale int32) (decimal.Decimal, Split) {
	stdUnit := standard.Total()
	total := receiptUnit.Sub(stdUnit).Mul(qty).Round(scale)
	out := Split{}
	if stdUnit.IsZero() {
		out.Add(CompDM, total)
		return total, out
	}
	largest := ""
	sum := decimal.Zero
	for _, c := range standard.Components() {
		part := total.Mul(standard[c]).Div(stdUnit).Round(scale)
		out.Add(c, part)
		sum = sum.Add(part)
		if largest == "" || standard[c].GreaterThan(standard[largest]) {
			largest = c
		}
	}
	out.Add(largest, total.Sub(sum))
	return total, out
}

// Alternative is one procurement source of a mixed-cost item (FR-39.14).
// Percent is the mixing ratio in percent; Split is the source's unit standard.
type Alternative struct {
	Source  string          `json:"source"`
	Percent decimal.Decimal `json:"percent"`
	Split   Split           `json:"split"`
}

// MixedCost returns the ratio-weighted split of the alternatives (FR-39.14).
// Ratios must total exactly 100.00 (BR-39.12); a zero ratio keeps the
// alternative for reporting but leaves it out of the roll.
func MixedCost(alts []Alternative) (Split, error) {
	total := decimal.Zero
	for _, a := range alts {
		if a.Percent.IsNegative() {
			return nil, fmt.Errorf("%s: negative ratio for %s", CodeMixingRatioTotal, a.Source)
		}
		total = total.Add(a.Percent)
	}
	if !total.Equal(decimal.NewFromInt(mixingRatioTotalWanted)) {
		return nil, fmt.Errorf("%s: mixing ratios total %s", CodeMixingRatioTotal, total.StringFixed(2))
	}
	out := Split{}
	for _, a := range alts {
		if a.Percent.IsZero() {
			continue
		}
		w := a.Percent.Div(decimal.NewFromInt(100))
		for c, v := range a.Split {
			out.Add(c, v.Mul(w))
		}
	}
	return out.Rounded(UnitScale), nil
}

// IssueCOGS splits the cost of goods sold of an issue by cost component
// (FR-39.15). Each component is rounded to scale decimals (PostingScale) and
// the total is the issue quantity times the unit cost; the rounding residual
// goes to the material component DM (BR-39.13).
func IssueCOGS(qty decimal.Decimal, unit Split, scale int32) (Split, decimal.Decimal) {
	total := qty.Mul(unit.Total()).Round(scale)
	out := Split{}
	sum := decimal.Zero
	for _, c := range unit.Components() {
		part := qty.Mul(unit[c]).Round(scale)
		out.Add(c, part)
		sum = sum.Add(part)
	}
	if residual := total.Sub(sum); !residual.IsZero() {
		out.Add(CompDM, residual)
	}
	return out, total
}

// RevalLine is one on-hand position revalued by an activation (FR-39.6).
type RevalLine struct {
	ItemID string          `json:"itemId"`
	SiteID string          `json:"siteId"`
	Qty    decimal.Decimal `json:"qty"`
	OldStd decimal.Decimal `json:"oldStd"`
	NewStd decimal.Decimal `json:"newStd"`
}

// RevalPreviewLine is a revalued position with its amount.
type RevalPreviewLine struct {
	RevalLine
	Amount decimal.Decimal `json:"amount"`
}

// JournalLine is one side of a journal preview.
type JournalLine struct {
	Account string          `json:"account"`
	Debit   decimal.Decimal `json:"debit"`
	Credit  decimal.Decimal `json:"credit"`
}

// RevalPreview is the activation preview: per-line revaluation, the total and
// the journal that would post it (CST-0712).
type RevalPreview struct {
	Currency string             `json:"currency,omitempty"`
	Lines    []RevalPreviewLine `json:"lines"`
	Total    decimal.Decimal    `json:"total"`
	Journal  []JournalLine      `json:"journal"`
}

// Revalue computes Σ qty × (new − old) per line, each rounded to the minor
// unit of currency (BR-39.1), and the balanced journal: a gain debits
// Inventory and credits the cost change variance, a loss does the reverse
// (AC-39.4).
func Revalue(lines []RevalLine, currency string) RevalPreview {
	scale := PostingScale(currency)
	p := RevalPreview{Currency: currency, Lines: make([]RevalPreviewLine, 0, len(lines)), Total: decimal.Zero}
	for _, ln := range lines {
		amt := ln.Qty.Mul(ln.NewStd.Sub(ln.OldStd)).Round(scale)
		p.Lines = append(p.Lines, RevalPreviewLine{RevalLine: ln, Amount: amt})
		p.Total = p.Total.Add(amt)
	}
	p.Journal = journal(p.Total)
	return p
}

// ConversionLine is one position at the conversion date (FR-39.12): quantity,
// standard unit cost and the value carried at the actual costing method.
type ConversionLine struct {
	ItemID      string          `json:"itemId"`
	SiteID      string          `json:"siteId"`
	Qty         decimal.Decimal `json:"qty"`
	StdUnit     decimal.Decimal `json:"stdUnit"`
	ActualValue decimal.Decimal `json:"actualValue"`
}

// ConversionAdjustment returns, per line, qty × standard − actual value rounded
// to the minor unit of currency (BR-39.1), their total and the journal that
// books it (FR-39.12, AC-39.10). The journal equals the sum of the rows.
func ConversionAdjustment(lines []ConversionLine, currency string) ([]decimal.Decimal, decimal.Decimal, []JournalLine) {
	scale := PostingScale(currency)
	amounts := make([]decimal.Decimal, len(lines))
	total := decimal.Zero
	for i, ln := range lines {
		amounts[i] = ln.Qty.Mul(ln.StdUnit).Sub(ln.ActualValue).Round(scale)
		total = total.Add(amounts[i])
	}
	return amounts, total, journal(total)
}

// journal books a revaluation: a positive amount debits Inventory.
func journal(amount decimal.Decimal) []JournalLine {
	if amount.IsZero() {
		return []JournalLine{}
	}
	if amount.IsNegative() {
		return []JournalLine{
			{Account: AccountCostChangeVar, Debit: amount.Neg()},
			{Account: AccountInventory, Credit: amount.Neg()},
		}
	}
	return []JournalLine{
		{Account: AccountInventory, Debit: amount},
		{Account: AccountCostChangeVar, Credit: amount},
	}
}
