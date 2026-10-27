package domain

import (
	"errors"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// PerDiemRate is one row of a per diem table (BR-43.6): lodging is charged per
// night, meals and incidentals (M&IE) per day. The table is pack data supplied by
// the caller; this slice does not load GSA or BMF tables.
type PerDiemRate struct {
	Country          string
	City             string
	Currency         string
	Lodging          decimal.Decimal
	MealsIncidentals decimal.Decimal
}

// MealsProvided marks meals the traveller did not pay for and that are therefore
// deducted from M&IE (BR-43.6: breakfast 20%, lunch 30%, dinner 50%).
type MealsProvided struct {
	Breakfast bool
	Lunch     bool
	Dinner    bool
}

// PerDiemDay is one travel day. Lodging is set on nights that are charged.
// Whether the day is partial is not an input: BR-43.6 makes the first and last
// day of the trip partial.
type PerDiemDay struct {
	Date    time.Time
	Lodging bool
	Meals   MealsProvided
}

// PerDiemLine is the computed amount for one day, rounded once to the minor
// unit of the rate currency.
type PerDiemLine struct {
	Date    time.Time
	Partial bool
	Lodging decimal.Decimal
	MIE     decimal.Decimal
	Total   decimal.Decimal
}

// PerDiemResult sums the days. Total = Lodging + MIE.
type PerDiemResult struct {
	Currency string
	Lines    []PerDiemLine
	Lodging  decimal.Decimal
	MIE      decimal.Decimal
	Total    decimal.Decimal
}

// ErrNoPerDiemDays is returned when a computation has no travel days.
var ErrNoPerDiemDays = errors.New("expense: per diem computation needs at least one day")

// ErrPerDiemDaysNotConsecutive is returned when the days are not one trip of
// consecutive calendar days in order (a repeated date would pay twice).
var ErrPerDiemDaysNotConsecutive = errors.New("expense: per diem days must be consecutive calendar days in order")

// BR-43.6 shares, in percent of the full-day M&IE rate.
const (
	partialDayPct = 75
	breakfastPct  = 20
	lunchPct      = 30
	dinnerPct     = 50
)

// ComputePerDiem applies BR-43.6 to one trip. The first and last day pay 75% of
// the M&IE rate (a one-day trip is both). Provided meals are deducted at their
// share of the full-day rate, also on partial days, and the day never goes below
// zero; this is how both the GSA per diem (FTR 301-11) and the German BMF
// Verpflegungspauschale (§ 9 (4a) EStG) take meal deductions. Each day is
// rounded half away from zero to the rate currency's minor unit, once.
func ComputePerDiem(rate PerDiemRate, days []PerDiemDay) (PerDiemResult, error) {
	if len(days) == 0 {
		return PerDiemResult{}, ErrNoPerDiemDays
	}
	for i := 1; i < len(days); i++ {
		if !dayOnly(days[i].Date).Equal(dayOnly(days[i-1].Date).AddDate(0, 0, 1)) {
			return PerDiemResult{}, ErrPerDiemDaysNotConsecutive
		}
	}
	places := money.MinorUnits(rate.Currency)
	res := PerDiemResult{Currency: rate.Currency, Lodging: decimal.Zero, MIE: decimal.Zero, Total: decimal.Zero}
	for i, d := range days {
		partial := i == 0 || i == len(days)-1
		mie := dayMIE(rate.MealsIncidentals, partial, d.Meals).Round(places)
		lodging := decimal.Zero
		if d.Lodging {
			lodging = rate.Lodging.Round(places)
		}
		line := PerDiemLine{Date: d.Date, Partial: partial, Lodging: lodging, MIE: mie, Total: lodging.Add(mie)}
		res.Lines = append(res.Lines, line)
		res.Lodging = res.Lodging.Add(lodging)
		res.MIE = res.MIE.Add(mie)
		res.Total = res.Total.Add(line.Total)
	}
	return res, nil
}

// dayMIE is the unrounded M&IE for one day: the day's allowance (full, or 75% on a
// partial day) less the provided meals at their share of the full-day rate.
func dayMIE(full decimal.Decimal, partial bool, meals MealsProvided) decimal.Decimal {
	allowancePct := int64(100)
	if partial {
		allowancePct = partialDayPct
	}
	var mealPct int64
	if meals.Breakfast {
		mealPct += breakfastPct
	}
	if meals.Lunch {
		mealPct += lunchPct
	}
	if meals.Dinner {
		mealPct += dinnerPct
	}
	net := full.Mul(decimal.New(allowancePct-mealPct, -2))
	return decimal.Max(decimal.Zero, net)
}
