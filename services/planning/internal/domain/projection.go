package domain

import (
	"errors"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Supply schedule and inventory forecast projection (FR-22.21, FR-22.22).
//
// The projection walks day by day, then groups the days into buckets. The
// projected on-hand at the end of a bucket is the PAB of BR-22.1 step 5 without
// the safety stock netting: PAB(t) = PAB(t-1) + supply(t) + planned(t) - demand(t).
// Days are offsets from the calculation's day 0.

const (
	BucketDay   = "day"
	BucketWeek  = "week"
	BucketMonth = "month"
	// daysOfSupplyWindow is the look-ahead for average daily demand (FR-22.22).
	daysOfSupplyWindow = 30
	maxProjectionDays  = 3650
)

var (
	// ErrInvalidBucket is returned for a bucket other than day, week or month.
	ErrInvalidBucket = errors.New("planning: bucket must be day, week or month")
	// ErrInvalidHorizon is returned when the horizon is not between 1 and 3650 days.
	ErrInvalidHorizon = errors.New("planning: horizon must be 1 to 3650 days")
	// ErrNegativeInput is returned when an opening balance, cost or flow quantity is negative.
	ErrNegativeInput = errors.New("planning: quantities and cost must not be negative")
	// ErrInvalidCurrency is returned when the unit cost carries no ISO 4217 currency code.
	ErrInvalidCurrency = errors.New("planning: unit cost needs a three-letter currency")
)

// BucketDays returns the number of days in one bucket. A month is 30 days,
// which keeps the grid aligned to day offsets without calendar dates.
func BucketDays(bucket string) (int, error) {
	switch bucket {
	case BucketDay:
		return 1, nil
	case BucketWeek:
		return 7, nil
	case BucketMonth:
		return 30, nil
	}
	return 0, ErrInvalidBucket
}

// Flow is a quantity due on one day offset.
type Flow struct {
	Day int
	Qty decimal.Decimal
}

// ProjectionInput is everything the schedule needs for one item and site.
type ProjectionInput struct {
	OpeningOnHand decimal.Decimal
	SafetyStock   decimal.Decimal
	UnitCost      money.Amount // current cost per inventory unit (FR-22.22)
	Demand        []Flow
	Supply        []Flow // scheduled receipts: open purchase and production orders
	Planned       []Flow // planned supply from the plan run
	HorizonDays   int
	Bucket        string
}

// PeriodRow is one bucket of the schedule. Start and End are inclusive day offsets.
type PeriodRow struct {
	Start         int
	End           int
	Demand        decimal.Decimal
	Supply        decimal.Decimal
	Planned       decimal.Decimal
	ClosingOnHand decimal.Decimal
	SafetyStock   decimal.Decimal
	Shortage      bool
	Value         money.Amount // closing on-hand at unit cost, rounded to the currency's minor unit
	// DaysOfSupply is nil when no demand falls in the 30 days after the bucket.
	DaysOfSupply *decimal.Decimal
}

// Projection is the schedule plus its totals over the horizon.
type Projection struct {
	Periods       []PeriodRow
	TotalDemand   decimal.Decimal
	TotalSupply   decimal.Decimal
	TotalPlanned  decimal.Decimal
	ClosingOnHand decimal.Decimal
}

// Project builds the bucketed supply schedule and inventory projection.
// Flows before day 0 (past-due) are counted on day 0; flows at or after the
// horizon are outside the schedule and are ignored, except demand, which still
// feeds days-of-supply.
func Project(in ProjectionInput) (Projection, error) {
	if in.HorizonDays < 1 || in.HorizonDays > maxProjectionDays {
		return Projection{}, ErrInvalidHorizon
	}
	width, err := BucketDays(in.Bucket)
	if err != nil {
		return Projection{}, err
	}
	if in.OpeningOnHand.IsNegative() || in.SafetyStock.IsNegative() || in.UnitCost.Value.IsNegative() {
		return Projection{}, ErrNegativeInput
	}
	if len(in.UnitCost.Currency) != 3 {
		return Projection{}, ErrInvalidCurrency
	}
	for _, flows := range [][]Flow{in.Demand, in.Supply, in.Planned} {
		for _, f := range flows {
			if f.Qty.IsNegative() {
				return Projection{}, ErrNegativeInput
			}
		}
	}

	demand := dailyTotals(in.Demand, in.HorizonDays)
	supply := dailyTotals(in.Supply, in.HorizonDays)
	planned := dailyTotals(in.Planned, in.HorizonDays)

	onHand := in.OpeningOnHand
	out := Projection{}
	for start := 0; start < in.HorizonDays; start += width {
		end := start + width - 1
		if end > in.HorizonDays-1 {
			end = in.HorizonDays - 1
		}
		row := PeriodRow{Start: start, End: end, SafetyStock: in.SafetyStock}
		for d := start; d <= end; d++ {
			row.Demand = row.Demand.Add(demand[d])
			row.Supply = row.Supply.Add(supply[d])
			row.Planned = row.Planned.Add(planned[d])
		}
		onHand = onHand.Add(row.Supply).Add(row.Planned).Sub(row.Demand)
		row.ClosingOnHand = onHand
		row.Shortage = onHand.LessThan(in.SafetyStock)
		row.Value = in.UnitCost.Mul(onHand)
		row.DaysOfSupply = daysOfSupply(onHand, in.Demand, end)
		out.Periods = append(out.Periods, row)
		out.TotalDemand = out.TotalDemand.Add(row.Demand)
		out.TotalSupply = out.TotalSupply.Add(row.Supply)
		out.TotalPlanned = out.TotalPlanned.Add(row.Planned)
	}
	out.ClosingOnHand = onHand
	return out, nil
}

// dailyTotals sums flows per day offset inside the horizon; past-due flows fold into day 0.
func dailyTotals(flows []Flow, horizon int) []decimal.Decimal {
	days := make([]decimal.Decimal, horizon)
	for _, f := range flows {
		d := f.Day
		if d < 0 {
			d = 0
		}
		if d >= horizon {
			continue
		}
		days[d] = days[d].Add(f.Qty)
	}
	return days
}

// daysOfSupply is on-hand divided by the average daily demand of the 30 days
// after the bucket end. Negative on-hand reports zero days.
func daysOfSupply(onHand decimal.Decimal, demand []Flow, bucketEnd int) *decimal.Decimal {
	var window decimal.Decimal
	for _, f := range demand {
		d := f.Day
		if d < 0 {
			d = 0
		}
		if d > bucketEnd && d <= bucketEnd+daysOfSupplyWindow {
			window = window.Add(f.Qty)
		}
	}
	if !window.IsPositive() {
		return nil
	}
	avg := window.Div(decimal.NewFromInt(daysOfSupplyWindow))
	days := onHand.Div(avg)
	if days.IsNegative() {
		days = decimal.Zero
	}
	days = days.Round(2)
	return &days
}
