package domain

import (
	"math"
	"time"

	"github.com/shopspring/decimal"
)

// Limits of a statistical forecast generation (FR-22.9). History and horizon are
// calendar months, the grain of the forecast service.
const (
	MaxGenerateSeries   = 1000
	MaxGenerateHorizon  = 24
	DefaultHistoryMonth = 36 // tenant parameter "forecast history months" (4.22.13)
	MinHistoryMonths    = 12
	MaxHistoryMonths    = 120
	MaxHistoryValues    = 600
)

// MonthPeriod is one calendar month of a statistical forecast.
type MonthPeriod struct {
	Start, End time.Time
}

// MonthPeriods returns n consecutive calendar months from first, which must be
// the first day of a month (the caller validates).
func MonthPeriods(first time.Time, n int) []MonthPeriod {
	out := make([]MonthPeriod, n)
	for i := range out {
		start := time.Date(first.Year(), first.Month()+time.Month(i), 1, 0, 0, 0, 0, time.UTC)
		out[i] = MonthPeriod{Start: start, End: start.AddDate(0, 1, -1)}
	}
	return out
}

// MaxForecastQty is the largest quantity a forecast cell stores (numeric(18,6)).
var MaxForecastQty = decimal.RequireFromString("999999999999.999999")

// QtyFromFloat turns a modelled value into a stored quantity: never negative or
// non-finite, rounded half away from zero to 6 decimals (BR-22.10; planning
// holds no unit of measure, so not to the item's unit decimals). ok is false
// when the value exceeds MaxForecastQty and cannot be stored.
func QtyFromFloat(f float64) (q decimal.Decimal, ok bool) {
	if math.IsNaN(f) || math.IsInf(f, -1) || f <= 0 {
		return decimal.Zero, true
	}
	if math.IsInf(f, 1) {
		return decimal.Zero, false
	}
	q = decimal.NewFromFloat(f).Round(6)
	return q, q.LessThanOrEqual(MaxForecastQty)
}
