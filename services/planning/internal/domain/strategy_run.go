package domain

import (
	"slices"

	"github.com/shopspring/decimal"
)

// ForecastCellDemand is one forecast period of an item as a plan run reads it
// (FR-22.8): the demand reference, the period as day indexes (inclusive), the
// quantity as entered and the quantity after the model's reduction.
type ForecastCellDemand struct {
	Ref              string
	StartDay, EndDay int
	Qty, ReducedQty  decimal.Decimal
}

// PlanForecast returns the forecast demand a run nets for one item under its
// planning strategy (FR-22.19, BR-22.13); sales are the item's sales-line
// demand. The strategy codes:
//
//   - mto: no forecast; supply follows the sales lines only.
//   - mts-gross: the forecast as entered; sales orders do not reduce it.
//   - assemble-to-order-final: the forecast as entered, consumed by the sales
//     lines inside the consumption window (ConsumeForecast). Unconsumed sales
//     stay in the sales demand on top of the forecast left, so AC-22.14's
//     forecast 100 and sales 40 give 60 + 40 = 100, not 140.
//   - anything else (mts-net, and for now plan-without-final-assembly and
//     subassembly): the model's reduced forecast, as before strategies existed.
//
// Cells come back in input order with their quantity replaced; cells left at
// zero are dropped. A strategy with an invalid window is ErrInvalidStrategy.
func PlanForecast(st StrategySpec, cells []ForecastCellDemand, sales []Requirement) ([]Requirement, error) {
	qtys := make([]decimal.Decimal, len(cells))
	switch st.Code {
	case StrategyMTO:
		return nil, nil
	case StrategyMTSGross:
		for i, c := range cells {
			qtys[i] = c.Qty
		}
	case StrategyATOFinal:
		buckets := make([]ForecastBucket, len(cells))
		for i, c := range cells {
			buckets[i] = ForecastBucket{ID: c.Ref, StartDay: c.StartDay, EndDay: c.EndDay, Qty: c.Qty}
		}
		lines := make([]SalesLine, len(sales))
		for i, s := range sales {
			lines[i] = SalesLine{ID: s.Ref, Day: s.Date, Qty: s.Qty}
		}
		res, err := ConsumeForecast(st.ConsumptionMode, st.BackwardDays, st.ForwardDays, buckets, lines)
		if err != nil {
			return nil, err
		}
		copy(qtys, res.Forecast)
	default:
		for i, c := range cells {
			qtys[i] = c.ReducedQty
		}
	}
	out := make([]Requirement, 0, len(cells))
	for i, c := range cells {
		if !qtys[i].IsPositive() {
			continue
		}
		// Dated at the period start, as the reduced forecast always was.
		out = append(out, Requirement{Ref: c.Ref, RefType: RefForecast, Date: c.StartDay, Qty: qtys[i], Priority: PriorityForecast})
	}
	return slices.Clip(out), nil
}
