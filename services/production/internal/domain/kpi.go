package domain

import "github.com/shopspring/decimal"

// KPIGroup is what the order KPI inquiry counts for one group of orders
// (PRD 4.21.14): orders ended in the period, how many of them with a delivery
// date finished on or before it, the cycle time of those that were started, the
// quantities reported as finished and in error, and the orders still open.
type KPIGroup struct {
	Ended        int
	Dated        int // ended orders that carry a delivery date
	OnTime       int // of Dated, finished on or before the delivery date
	Cycled       int // ended orders with a start in the status log before their finish
	CycleDaysSum decimal.Decimal
	QtyFinished  decimal.Decimal
	QtyError     decimal.Decimal
	Open         int // not Ended and not Cancelled
	Overdue      int // of Open, delivery date before the as-of date
}

// Add merges another group into g, as the totals row does.
func (g *KPIGroup) Add(o KPIGroup) {
	g.Ended += o.Ended
	g.Dated += o.Dated
	g.OnTime += o.OnTime
	g.Cycled += o.Cycled
	g.CycleDaysSum = g.CycleDaysSum.Add(o.CycleDaysSum)
	g.QtyFinished = g.QtyFinished.Add(o.QtyFinished)
	g.QtyError = g.QtyError.Add(o.QtyError)
	g.Open += o.Open
	g.Overdue += o.Overdue
}

// OnTimePct is the share of dated ended orders that ended on time, rounded to
// PctScale places; nil when no ended order has a delivery date.
func (g KPIGroup) OnTimePct() *decimal.Decimal {
	if g.Dated == 0 {
		return nil
	}
	p := decimal.NewFromInt(int64(g.OnTime)).Mul(hundred).DivRound(decimal.NewFromInt(int64(g.Dated)), PctScale+4).Round(PctScale)
	return &p
}

// AvgCycleDays is the mean days from the first start to the finish of the orders
// that were started, rounded to two places; nil when none was.
func (g KPIGroup) AvgCycleDays() *decimal.Decimal {
	if g.Cycled == 0 {
		return nil
	}
	v := g.CycleDaysSum.DivRound(decimal.NewFromInt(int64(g.Cycled)), 6).Round(2)
	return &v
}

// ScrapPct is the error quantity as a percentage of everything reported
// (finished plus error), rounded to PctScale places; nil when nothing was reported.
func (g KPIGroup) ScrapPct() *decimal.Decimal {
	total := g.QtyFinished.Add(g.QtyError)
	if !total.IsPositive() {
		return nil
	}
	p := g.QtyError.Mul(hundred).DivRound(total, PctScale+4).Round(PctScale)
	return &p
}
