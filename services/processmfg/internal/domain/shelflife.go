package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// civil truncates t to its calendar date in UTC.
func civil(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// ShelfDates implements FR-24.6: best-before = manufacturing date +
// best-before days, expiry = manufacturing date + shelf-life days.
func ShelfDates(manufactured time.Time, bestBeforeDays, shelfLifeDays int) (bestBefore, expiry time.Time, err error) {
	if bestBeforeDays < 0 || shelfLifeDays < 0 {
		return bestBefore, expiry, errorf(CodeInvalid, "shelf-life days must not be negative")
	}
	d := civil(manufactured)
	return d.AddDate(0, 0, bestBeforeDays), d.AddDate(0, 0, shelfLifeDays), nil
}

// ShortestExpiry implements the inherit-shortest-shelf-life rule (BR-24.4): the
// output expiry is the earliest of its own expiry and the sender expiries.
func ShortestExpiry(own time.Time, senders []time.Time) time.Time {
	out := civil(own)
	for _, s := range senders {
		if s := civil(s); s.Before(out) {
			out = s
		}
	}
	return out
}

// WeightedAverage implements BR-24.11: Σ(v·w) / Σw rounded to scale places.
// values and weights are parallel slices of senders that carry a value.
func WeightedAverage(values, weights []decimal.Decimal, scale int32) (decimal.Decimal, error) {
	if len(values) == 0 || len(values) != len(weights) {
		return decimal.Zero, errorf(CodeInvalid, "values and weights must be non-empty and of equal length")
	}
	num, den := decimal.Zero, decimal.Zero
	for i := range values {
		if weights[i].IsNegative() {
			return decimal.Zero, errorf(CodeInvalid, "weights must not be negative")
		}
		num = num.Add(values[i].Mul(weights[i]))
		den = den.Add(weights[i])
	}
	if !den.IsPositive() {
		return decimal.Zero, errorf(CodeInvalid, "weights sum to zero")
	}
	return num.DivRound(den, 24).Round(scale), nil
}
