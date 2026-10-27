package domain

import (
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// maxRateScale is the most decimals a cost rate may carry (numeric(20,6)).
const maxRateScale = 6

// ActivityWork is what the jobs of one flow activity completed in a costing
// period: how many jobs and the quantity they processed (reported plus scrap,
// in the item's inventory unit). CycleTimeS is the activity's cycle time per
// unit, the figure FR-24.14 compares with takt time.
type ActivityWork struct {
	ActivityID string
	Sequence   int
	Name       string
	Kind       string
	CycleTimeS int
	Jobs       int
	Qty        decimal.Decimal
}

// ConversionLine is the backflushed conversion cost of one activity. Rate and
// Cost are nil for a service activity: its cost is the subcontracting purchase
// price (FR-24.20), not a work-cell rate. Rate is also nil for an activity
// without jobs in the period that was given no rate; its cost is zero.
type ConversionLine struct {
	ActivityWork
	Hours decimal.Decimal
	Rate  *money.Amount
	Cost  *money.Amount
}

// Backflush is the conversion cost of a flow period (FR-24.12): one line per
// activity of the flow, in sequence order, and their total.
type Backflush struct {
	Lines      []ConversionLine
	Conversion money.Amount
}

// BackflushConversion accumulates conversion cost by activity for a period
// (FR-24.12: cycle time × cost rate). An activity's hours are its cycle time
// per unit times the quantity its jobs processed; its cost is hours × its
// hourly rate, rounded once to the currency's minor unit, and the total is the
// sum of the rounded activity costs, so it equals the lines exactly. rates maps
// activity id to an hourly rate in currency. Every process or transfer
// activity with completed jobs needs a rate; a rate for an activity that is not
// in the flow or is a service activity, or a rate that is negative, too large
// or has more than six decimals, is COM-0001.
func BackflushConversion(currency string, work []ActivityWork, rates map[string]decimal.Decimal) (Backflush, error) {
	if !ValidCurrency(currency) {
		return Backflush{}, errorf(CodeInvalid, "currency must be a three-letter ISO 4217 code")
	}
	byID := make(map[string]ActivityWork, len(work))
	for _, w := range work {
		byID[w.ActivityID] = w
	}
	for id, r := range rates {
		w, ok := byID[id]
		switch {
		case !ok:
			return Backflush{}, errorf(CodeInvalid, "activity %s is not an activity of this flow", id)
		case w.Kind == "service":
			return Backflush{}, errorf(CodeInvalid, "activity %s is a service activity: its cost comes from the purchase agreement, not a rate", w.Name)
		case r.IsNegative() || !r.LessThan(maxAmount()):
			return Backflush{}, errorf(CodeInvalid, "the rate of activity %s must be zero or more and below 10^14", w.Name)
		case !r.Equal(r.Round(maxRateScale)):
			return Backflush{}, errorf(CodeInvalid, "the rate of activity %s has more than %d decimals", w.Name, maxRateScale)
		}
	}
	hour := decimal.NewFromInt(3600)
	out := Backflush{Lines: make([]ConversionLine, 0, len(work)), Conversion: money.Amount{Value: decimal.Zero, Currency: currency}}
	for _, w := range work {
		seconds := decimal.NewFromInt(int64(w.CycleTimeS)).Mul(w.Qty)
		line := ConversionLine{ActivityWork: w, Hours: seconds.Div(hour).Round(4)}
		if w.Kind != "service" {
			rate, ok := rates[w.ActivityID]
			if !ok && w.Jobs > 0 {
				return Backflush{}, errorf(CodeInvalid, "activity %s completed %d jobs in the period and needs a cost rate", w.Name, w.Jobs)
			}
			// Multiply before dividing so the only rounding is the final one.
			cost := money.Amount{Value: seconds.Mul(rate).Div(hour), Currency: currency}.Round()
			line.Cost = &cost
			if ok {
				line.Rate = &money.Amount{Value: rate, Currency: currency}
			}
			sum, err := out.Conversion.Add(cost)
			if err != nil {
				return Backflush{}, err
			}
			out.Conversion = sum
		}
		out.Lines = append(out.Lines, line)
	}
	return out, nil
}
