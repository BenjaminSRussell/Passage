package domain

import (
	"errors"
	"slices"

	"github.com/shopspring/decimal"
)

// Planning strategies (FR-22.19). Codes match the SAP strategy groups they map from.
const (
	StrategyMTSNet           = "mts-net"
	StrategyMTSGross         = "mts-gross"
	StrategyMTO              = "mto"
	StrategyATOFinal         = "assemble-to-order-final"
	StrategyPlanWithoutFinal = "plan-without-final-assembly"
	StrategySubassembly      = "subassembly"
)

// Consumption modes of an assemble-to-order-final strategy (FR-22.19).
const (
	ConsumptionBackward        = "backward"
	ConsumptionForward         = "forward"
	ConsumptionBackwardForward = "backward-forward"
)

// ErrInvalidStrategy is returned for an unknown strategy, consumption mode or window.
var ErrInvalidStrategy = errors.New("planning: invalid planning strategy or consumption setup")

var strategyCodes = []string{StrategyMTSNet, StrategyMTSGross, StrategyMTO, StrategyATOFinal, StrategyPlanWithoutFinal, StrategySubassembly}

// ValidStrategy reports whether code is one of the six FR-22.19 strategies.
func ValidStrategy(code string) bool { return slices.Contains(strategyCodes, code) }

// UsesMarking reports whether pegging under the strategy is marked so later
// runs cannot re-peg the supply (FR-22.19 notes: mto and plan-without-final-assembly).
func UsesMarking(code string) bool {
	return code == StrategyMTO || code == StrategyPlanWithoutFinal
}

// ForecastBucket is one forecast period [StartDay, EndDay] (inclusive) of an item.
type ForecastBucket struct {
	ID       string
	StartDay int
	EndDay   int
	Qty      decimal.Decimal
}

// SalesLine is a sales demand on day Day.
type SalesLine struct {
	ID  string
	Day int
	Qty decimal.Decimal
}

// SalesConsumption reports how much of one sales line consumed forecast and how much did not.
type SalesConsumption struct {
	ID         string
	Consumed   decimal.Decimal
	Unconsumed decimal.Decimal
}

// ConsumptionResult is the forecast left after consumption, per bucket in input order,
// and the consumption per sales line in input order.
type ConsumptionResult struct {
	Forecast []decimal.Decimal
	Sales    []SalesConsumption
}

// ConsumeForecast applies BR-22.13 for assemble-to-order-final. A sales line on
// day d consumes forecast from buckets overlapping [d - backward, d], latest
// first, then from buckets overlapping (d, d + forward], earliest first. The mode
// selects the window: backward ignores the forward window, forward ignores the
// backward window, backward-forward uses both. Lines are processed by day then id,
// so the result does not depend on input order. Forecast never goes negative.
//
// Sales that find no forecast are reported as Unconsumed; they stay in gross
// requirements on top of the remaining forecast (see GrossQty).
func ConsumeForecast(mode string, backwardDays, forwardDays int, buckets []ForecastBucket, sales []SalesLine) (ConsumptionResult, error) {
	if mode != ConsumptionBackward && mode != ConsumptionForward && mode != ConsumptionBackwardForward {
		return ConsumptionResult{}, ErrInvalidStrategy
	}
	if backwardDays < 0 || forwardDays < 0 {
		return ConsumptionResult{}, ErrInvalidStrategy
	}
	if mode == ConsumptionBackward {
		forwardDays = 0
	}
	if mode == ConsumptionForward {
		backwardDays = 0
	}
	for _, b := range buckets {
		if b.EndDay < b.StartDay || b.Qty.IsNegative() {
			return ConsumptionResult{}, ErrInvalidStrategy
		}
	}
	for _, s := range sales {
		if s.Qty.IsNegative() {
			return ConsumptionResult{}, ErrInvalidStrategy
		}
	}

	remaining := make([]decimal.Decimal, len(buckets))
	for i, b := range buckets {
		remaining[i] = b.Qty
	}
	byStart := make([]int, len(buckets))
	for i := range byStart {
		byStart[i] = i
	}
	slices.SortStableFunc(byStart, func(a, b int) int { return buckets[a].StartDay - buckets[b].StartDay })

	order := make([]int, len(sales))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		if sales[a].Day != sales[b].Day {
			return sales[a].Day - sales[b].Day
		}
		return compareStrings(sales[a].ID, sales[b].ID)
	})

	out := ConsumptionResult{Forecast: remaining, Sales: make([]SalesConsumption, len(sales))}
	for _, si := range order {
		s := sales[si]
		rem := s.Qty
		// Backward: buckets starting on or before d and ending on or after d - backward; latest start first.
		for k := len(byStart) - 1; k >= 0 && rem.IsPositive(); k-- {
			i := byStart[k]
			if buckets[i].StartDay <= s.Day && buckets[i].EndDay >= s.Day-backwardDays {
				rem = take(remaining, i, rem)
			}
		}
		// Forward: buckets ending after d and starting on or before d + forward; earliest start first.
		for k := 0; k < len(byStart) && rem.IsPositive(); k++ {
			i := byStart[k]
			if buckets[i].EndDay > s.Day && buckets[i].StartDay <= s.Day+forwardDays {
				rem = take(remaining, i, rem)
			}
		}
		out.Sales[si] = SalesConsumption{ID: s.ID, Consumed: s.Qty.Sub(rem), Unconsumed: rem}
	}
	return out, nil
}

// take removes up to rem from bucket i and returns what is still to be consumed.
func take(remaining []decimal.Decimal, i int, rem decimal.Decimal) decimal.Decimal {
	if remaining[i].GreaterThanOrEqual(rem) {
		remaining[i] = remaining[i].Sub(rem)
		return decimal.Zero
	}
	rem = rem.Sub(remaining[i])
	remaining[i] = decimal.Zero
	return rem
}

// GrossQty is the gross requirement of the consumed window: the forecast left
// after consumption plus every sales line (BR-22.13). For AC-22.14 this is
// (100 - 40) + 40 = 100 rather than 140.
func GrossQty(r ConsumptionResult, sales []SalesLine) decimal.Decimal {
	total := decimal.Zero
	for _, f := range r.Forecast {
		total = total.Add(f)
	}
	for _, s := range sales {
		total = total.Add(s.Qty)
	}
	return total
}
