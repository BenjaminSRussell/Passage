package domain

import (
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// CodeRetroactive is the PRC-0006 notice: a retroactive activation re-accrues posted invoices.
const CodeRetroactive = "PRC-0006"

// IsRetroactive reports whether an agreement starts before the day it was activated, so the
// invoices posted between valid_from and activation must be re-accrued (FR-26.23, PRC-0006).
func IsRetroactive(a RebateAgreement, activatedOn time.Time) bool {
	return dayOf(a.ValidFrom) < dayOf(activatedOn)
}

// InRetroScope reports whether an invoice day falls in the window a retroactive recompute
// replays: from valid_from up to the activation day, and never after valid_to (BR-26.14).
// Invoices after the activation day accrue through the regular invoice feed.
func InRetroScope(a RebateAgreement, activatedOn, invoiceDay time.Time) bool {
	d := dayOf(invoiceDay)
	return d >= dayOf(a.ValidFrom) && d <= dayOf(a.ValidTo) && d <= dayOf(activatedOn)
}

// RetroPeriod is one accrual period with its cumulative basis (after the replayed invoices
// are added) and the accrual already posted for it.
type RetroPeriod struct {
	Period   string
	CumValue decimal.Decimal
	CumQty   decimal.Decimal
	Posted   decimal.Decimal
}

// RetroDelta is the correction one period needs after a retroactive change (BR-26.14).
// A positive delta debits the expense account and credits the accrual account.
type RetroDelta struct {
	Period string
	Target decimal.Decimal
	Posted decimal.Decimal
	Delta  decimal.Decimal
	// CumValue and CumQty are the period's cumulative basis the target was computed on.
	CumValue decimal.Decimal
	CumQty   decimal.Decimal
}

// RetroDeltas recomputes every period at its cumulative basis and returns one delta per
// period whose posted accrual differs from the target, ordered by period key. Posted
// invoices are never re-priced; only the difference posts, once per period, with
// retroactive=true (BR-26.7, BR-26.14). Running it again on the result yields nothing.
func RetroDeltas(a RebateAgreement, periods []RetroPeriod) []RetroDelta {
	out := []RetroDelta{}
	for _, p := range periods {
		target := PeriodTarget(a, p.CumValue, p.CumQty)
		delta := target.Sub(p.Posted)
		if delta.IsZero() {
			continue
		}
		out = append(out, RetroDelta{Period: p.Period, Target: target, Posted: p.Posted, Delta: delta, CumValue: p.CumValue, CumQty: p.CumQty})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Period < out[j].Period })
	return out
}
