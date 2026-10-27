package domain

import (
	"sort"
	"strings"

	"github.com/shopspring/decimal"
)

// Subsequent price change (FR-17.22, BR-17.18). A reprice run moves the open
// PO lines priced from a trade agreement (price_source TRADE_AGREEMENT:<id>) to
// the trade agreement price that wins today for the PO's pricing date. Lines
// priced by hand are never moved, lines drawn against a purchase agreement keep
// their release amount (PRC-PO-010), and lines with an invoice stay at the old
// price and are handed to payables for a subsequent debit or credit instead.
const (
	PriceSourceManual      = "MANUAL"
	PriceSourceTradePrefix = "TRADE_AGREEMENT:"
)

// Reasons a reprice run leaves a line alone.
const (
	SkipInvoiced       = "INVOICED"
	SkipManualPrice    = "MANUAL_PRICE"
	SkipAgreementLine  = "AGREEMENT_LINE"
	SkipNotTradePrice  = "NOT_TRADE_AGREEMENT"
	SkipNoCurrentPrice = "NO_CURRENT_PRICE"
	SkipUnchanged      = "UNCHANGED"
	// SkipHandedOver is an invoiced line whose change to the same price row
	// an earlier run already handed to payables.
	SkipHandedOver = "HANDED_OVER"
)

// RepriceLine is one live (not cancelled) PO line as the run sees it.
type RepriceLine struct {
	LineNo         int
	ProductID      string
	Unit           string
	PriceSource    string
	InvoicedQty    decimal.Decimal
	AgreementDrawn bool
	// HandedOver is the price source an earlier run handed to payables for
	// this invoiced line ("" when none).
	HandedOver string
	Input      LineInput
}

// RepriceQuote is the trade agreement row that wins for one line now.
type RepriceQuote struct {
	Price     decimal.Decimal
	PriceUnit decimal.Decimal
	Source    string // PriceSourceTradePrefix + agreement id
}

// RepriceChange is one line's move to a new price. Qty is the quantity the
// amounts are for: the line quantity for a moved line, the invoiced quantity
// for an invoiced line handed to payables. Delta = NewAmount − OldAmount, both
// rounded line amounts after discount.
type RepriceChange struct {
	LineNo       int
	Qty          decimal.Decimal
	OldPrice     decimal.Decimal
	OldPriceUnit decimal.Decimal
	NewPrice     decimal.Decimal
	NewPriceUnit decimal.Decimal
	OldAmount    decimal.Decimal
	NewAmount    decimal.Decimal
	Delta        decimal.Decimal
	OldSource    string
	NewSource    string
}

// RepriceSkip is a line the run leaves alone, and why.
type RepriceSkip struct {
	LineNo int
	Reason string
}

// RepricePlan is the preview of one run on one PO. Changes are the lines that
// move; Invoiced are lines whose price changed but which are invoiced, so they
// stay at the old price and go to wave 18 (FR-18.18) instead; every other line
// is in Skipped. TotalDelta sums Changes only.
type RepricePlan struct {
	Changes    []RepriceChange
	Invoiced   []RepriceChange
	Skipped    []RepriceSkip
	TotalDelta decimal.Decimal
}

// RepriceSkipReason says why a line cannot be repriced whatever the current
// price is, or returns "" when the run must look its price up.
func RepriceSkipReason(l RepriceLine) string {
	switch {
	case l.PriceSource == PriceSourceManual:
		return SkipManualPrice
	case l.AgreementDrawn:
		return SkipAgreementLine
	case l.ProductID == "" || !strings.HasPrefix(l.PriceSource, PriceSourceTradePrefix):
		return SkipNotTradePrice
	}
	return ""
}

// CheckRepriceable allows a run only on a CONFIRMED PO: a draft is priced
// afresh on every edit, a PO in review already carries a pending change, and a
// finalized or cancelled PO is closed.
func CheckRepriceable(approval string) error {
	if approval != POStatusConfirmed {
		return fail(CodePOState, "purchase order is %s; a subsequent price change needs a CONFIRMED purchase order", approval)
	}
	return nil
}

// PlanReprice computes one run. quote returns the current winner for a line
// that RepriceSkipReason lets through; a line without one is skipped. minor is
// the currency's minor-unit count. Every slice of the result is ordered by
// line number and never nil.
func PlanReprice(lines []RepriceLine, quote func(RepriceLine) (RepriceQuote, bool), minor int32) (RepricePlan, error) {
	sorted := make([]RepriceLine, len(lines))
	copy(sorted, lines)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].LineNo < sorted[j].LineNo })
	plan := RepricePlan{Changes: []RepriceChange{}, Invoiced: []RepriceChange{}, Skipped: []RepriceSkip{}, TotalDelta: decimal.Zero}
	for _, l := range sorted {
		if reason := RepriceSkipReason(l); reason != "" {
			plan.Skipped = append(plan.Skipped, RepriceSkip{LineNo: l.LineNo, Reason: reason})
			continue
		}
		q, ok := quote(l)
		if !ok {
			plan.Skipped = append(plan.Skipped, RepriceSkip{LineNo: l.LineNo, Reason: SkipNoCurrentPrice})
			continue
		}
		if q.Price.Equal(l.Input.UnitPrice) && q.PriceUnit.Equal(l.Input.PriceUnit) {
			plan.Skipped = append(plan.Skipped, RepriceSkip{LineNo: l.LineNo, Reason: SkipUnchanged})
			continue
		}
		if l.InvoicedQty.IsPositive() {
			// BR-17.18: an invoiced line keeps its price; payables settles the
			// difference on the invoiced quantity, once per price row.
			if l.HandedOver == q.Source {
				plan.Skipped = append(plan.Skipped, RepriceSkip{LineNo: l.LineNo, Reason: SkipHandedOver})
				continue
			}
			c, err := repriceChange(l, q, l.InvoicedQty, minor)
			if err != nil {
				return RepricePlan{}, err
			}
			plan.Invoiced = append(plan.Invoiced, c)
			plan.Skipped = append(plan.Skipped, RepriceSkip{LineNo: l.LineNo, Reason: SkipInvoiced})
			continue
		}
		c, err := repriceChange(l, q, l.Input.Qty, minor)
		if err != nil {
			return RepricePlan{}, err
		}
		plan.Changes = append(plan.Changes, c)
		plan.TotalDelta = plan.TotalDelta.Add(c.Delta)
	}
	return plan, nil
}

// repriceChange prices qty of line l at its current and at the quoted price.
func repriceChange(l RepriceLine, q RepriceQuote, qty decimal.Decimal, minor int32) (RepriceChange, error) {
	in := l.Input
	in.Qty = qty
	before, err := ComputeLine(in, minor)
	if err != nil {
		return RepriceChange{}, err
	}
	in.UnitPrice, in.PriceUnit = q.Price, q.PriceUnit
	after, err := ComputeLine(in, minor)
	if err != nil {
		return RepriceChange{}, err
	}
	return RepriceChange{LineNo: l.LineNo, Qty: qty, OldPrice: l.Input.UnitPrice, OldPriceUnit: l.Input.PriceUnit,
		NewPrice: q.Price, NewPriceUnit: q.PriceUnit, OldAmount: before.Amount, NewAmount: after.Amount,
		Delta: after.Amount.Sub(before.Amount), OldSource: l.PriceSource, NewSource: q.Source}, nil
}
