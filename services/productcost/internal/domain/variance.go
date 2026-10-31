package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// CodeVarianceIdentity is raised when the decomposition cannot close to the cent (BR-39.9).
const CodeVarianceIdentity = "CST-0720"

// CodedError is a domain failure that carries a message-catalog code.
type CodedError struct {
	Code   string
	Detail string
}

func (e *CodedError) Error() string { return e.Code + ": " + e.Detail }

// maxResidual bounds the rounding residual at the posting scale. The total and
// the four type cells of a component are each rounded by less than half a
// minor unit, and the residual is a whole number of minor units, so it is at
// most two of them (0.02 in a two-decimal currency); a larger one is a defect.
func maxResidual(scale int32) decimal.Decimal {
	return decimal.New(2, -scale)
}

// OrderLine is one input or activity of a production order.
//
// A planned line has PlannedQty for the order and StdUnitCost per unit. A fixed
// per-lot line (setup) has Fixed set: EstPerLot is its estimate for one lot of
// the order and StdUnitCost its standard per unit at the costing lot size. An
// unplanned line (substitution) has Planned false and no standard.
// ActualQty and ActualUnitCost are the consumed or posted amounts; for a fixed
// line ActualQty is the number of lots (normally 1).
type OrderLine struct {
	Component      string          `json:"component"`
	Planned        bool            `json:"planned"`
	Fixed          bool            `json:"fixed,omitempty"`
	PlannedQty     decimal.Decimal `json:"plannedQty"`
	StdUnitCost    decimal.Decimal `json:"stdUnitCost"`
	EstPerLot      decimal.Decimal `json:"estPerLot"`
	ActualQty      decimal.Decimal `json:"actualQty"`
	ActualUnitCost decimal.Decimal `json:"actualUnitCost"`
}

// Order is an ended production order. PlannedQty is Q_order and RAFQty the
// quantity reported as finished, Q_raf.
type Order struct {
	// Currency sets the posting precision (BR-39.1); empty means two decimals.
	Currency   string          `json:"currency,omitempty"`
	PlannedQty decimal.Decimal `json:"plannedQty"`
	RAFQty     decimal.Decimal `json:"rafQty"`
	Lines      []OrderLine     `json:"lines"`
}

// Types are the four variance categories (FR-39.8). Unfavorable is positive.
type Types struct {
	LotSize      decimal.Decimal `json:"lotSize"`
	Quantity     decimal.Decimal `json:"quantity"`
	Price        decimal.Decimal `json:"price"`
	Substitution decimal.Decimal `json:"substitution"`
}

// Sum returns LSV + QV + PV + SV.
func (t Types) Sum() decimal.Decimal {
	return t.LotSize.Add(t.Quantity).Add(t.Price).Add(t.Substitution)
}

// ComponentVariance is the decomposition of one cost component.
type ComponentVariance struct {
	Component string          `json:"component"`
	Total     decimal.Decimal `json:"total"`
	Types     Types           `json:"types"`
}

// Decomposition is the variance of one order. Total is actual minus the
// standard cost of the reported-as-finished output.
type Decomposition struct {
	Total       decimal.Decimal     `json:"total"`
	Types       Types               `json:"types"`
	ByComponent []ComponentVariance `json:"byComponent"`
	Residual    decimal.Decimal     `json:"residual"`
}

// cell accumulates unrounded amounts for one component, each multiplied by
// Q_order so that only multiplications happen per line and the one division
// by Q_order is taken per cell before rounding (see Decompose). The zero value
// is zero.
type cell struct {
	total, lot, qty, price, sub decimal.Decimal
}

// Decompose splits an order's variance into LSV, QV, PV and SV per component.
//
// Per line, with RAF output r and sq = PlannedQty × r ÷ Q_order:
//   - planned variable line: QV = (actual qty − sq) × std; PV = actual qty × (actual cost − std)
//   - fixed per-lot line: LSV = est × r ÷ Q_order − std × r; PV = actual − est × r ÷ Q_order
//   - unplanned line: SV = actual qty × actual cost
//
// Each line's categories sum to its actual minus standard cost, so the order
// closes exactly. Cells are rounded to the currency's minor unit, and each
// component's rounding residual is assigned to that component's quantity
// variance, so the identity holds per component as well as per order (FR-39.8
// assigns it to the QV of the largest planned line; the per-line detail is not
// returned in slice 1, so the component's QV stands in for it). A residual
// beyond the rounding bound fails with CST-0720 (BR-39.9).
func Decompose(o Order) (Decomposition, error) {
	if !o.PlannedQty.IsPositive() {
		return Decomposition{}, fmt.Errorf("planned quantity must be positive")
	}
	if o.RAFQty.IsNegative() {
		return Decomposition{}, fmt.Errorf("reported-as-finished quantity must not be negative")
	}
	scale := PostingScale(o.Currency)
	cells := map[string]*cell{}
	get := func(c string) *cell {
		if cells[c] == nil {
			cells[c] = &cell{}
		}
		return cells[c]
	}
	// Every amount below is the line's amount times Q_order (qo), so a line only
	// multiplies and each cell divides once: dividing RAF by Q_order first
	// (a non-terminating ratio such as 55/76) shifted amounts that are exactly
	// on a half minor unit to the wrong side of the rounding.
	qo := o.PlannedQty
	for _, ln := range o.Lines {
		c := get(ln.Component)
		actual := ln.ActualQty.Mul(ln.ActualUnitCost).Mul(qo)
		switch {
		case ln.Planned && ln.Fixed:
			est := ln.EstPerLot.Mul(o.RAFQty)
			std := ln.StdUnitCost.Mul(o.RAFQty).Mul(qo)
			c.lot = c.lot.Add(est.Sub(std))
			c.price = c.price.Add(actual.Sub(est))
			c.total = c.total.Add(actual.Sub(std))
		case ln.Planned:
			sq := ln.PlannedQty.Mul(o.RAFQty)
			c.qty = c.qty.Add(ln.ActualQty.Mul(qo).Sub(sq).Mul(ln.StdUnitCost))
			c.price = c.price.Add(ln.ActualQty.Mul(ln.ActualUnitCost.Sub(ln.StdUnitCost)).Mul(qo))
			c.total = c.total.Add(actual.Sub(sq.Mul(ln.StdUnitCost)))
		default:
			c.sub = c.sub.Add(actual)
			c.total = c.total.Add(actual)
		}
	}

	out := Decomposition{ByComponent: []ComponentVariance{}}
	per := func(v decimal.Decimal) decimal.Decimal { return v.Div(qo).Round(scale) }
	for _, comp := range sortedKeys(cells) {
		c := cells[comp]
		cv := ComponentVariance{Component: comp, Total: per(c.total), Types: Types{
			LotSize: per(c.lot), Quantity: per(c.qty),
			Price: per(c.price), Substitution: per(c.sub),
		}}
		out.ByComponent = append(out.ByComponent, cv)
		out.Total = out.Total.Add(cv.Total)
		out.Types.LotSize = out.Types.LotSize.Add(cv.Types.LotSize)
		out.Types.Quantity = out.Types.Quantity.Add(cv.Types.Quantity)
		out.Types.Price = out.Types.Price.Add(cv.Types.Price)
		out.Types.Substitution = out.Types.Substitution.Add(cv.Types.Substitution)
	}
	// Each component's rounding residual goes to its own quantity variance
	// (the cell that absorbs the unexplained cents; FR-39.8).
	for i := range out.ByComponent {
		cv := &out.ByComponent[i]
		r := cv.Total.Sub(cv.Types.Sum())
		if r.IsZero() {
			continue
		}
		if !r.Abs().LessThanOrEqual(maxResidual(scale)) {
			return Decomposition{}, &CodedError{Code: CodeVarianceIdentity, Detail: fmt.Sprintf("%s residual %s", cv.Component, r)}
		}
		cv.Types.Quantity = cv.Types.Quantity.Add(r)
		out.Types.Quantity = out.Types.Quantity.Add(r)
		out.Residual = out.Residual.Add(r)
	}
	if !out.Total.Equal(out.Types.Sum()) {
		return Decomposition{}, &CodedError{Code: CodeVarianceIdentity, Detail: fmt.Sprintf("total %s differs from categories %s", out.Total, out.Types.Sum())}
	}
	return out, nil
}
