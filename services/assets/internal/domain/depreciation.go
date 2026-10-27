package domain

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/money"
)

// Method is the depreciation method of one asset book. StraightLine depreciates
// per month over Life months; DecliningBalance depreciates per year over Life
// years, with an optional switch to straight line (MACRS-style at Factor 2).
type Method string

const (
	MethodStraightLine     Method = "StraightLine"
	MethodDecliningBalance Method = "DecliningBalance"
)

// Convention decides how much of the first period is charged; the last period
// takes the remainder of the life (FR-37.2). MidQuarter applies to annual
// declining-balance books (MACRS, BR-37.5). Mid-month (1st) and
// half-year-next-year are later slices (NOT-IMPLEMENTED.md D1d).
type Convention string

const (
	ConventionNone       Convention = "None"
	ConventionFullMonth  Convention = "FullMonth"
	ConventionMidMonth15 Convention = "MidMonth15"
	ConventionHalfYear   Convention = "HalfYear"
	ConventionMidQuarter Convention = "MidQuarter"
)

// Input is one book's depreciation profile applied to a cost basis. Life is in
// months for StraightLine and years for DecliningBalance; Factor is the
// declining-balance multiplier (2 = 200%).
type Input struct {
	Cost                 decimal.Decimal
	Salvage              decimal.Decimal
	Life                 int
	Method               Method
	Factor               decimal.Decimal
	SwitchToStraightLine bool
	InService            time.Time
	Convention           Convention
	Currency             string
}

// Line is one scheduled period. Period is "2026-03" for monthly lines and
// "2026" for annual lines.
type Line struct {
	Index       int
	Period      string
	Amount      decimal.Decimal
	Accumulated decimal.Decimal
	NBV         decimal.Decimal
}

const (
	maxMonthlyLife = 1200
	maxAnnualLife  = 100
)

var (
	one  = decimal.NewFromInt(1)
	half = decimal.RequireFromString("0.5")
)

// Schedule computes every period of a depreciation profile. The cumulative
// charge is rounded half-up to the currency minor unit at each step and the
// final period absorbs any residual, so the sum of amounts equals Cost - Salvage
// exactly (BR-37.1).
func Schedule(in Input) ([]Line, error) {
	if len(in.Currency) != 3 {
		return nil, fail(CodeInvalidDepreciation, "currency must be an ISO 4217 code")
	}
	if in.Cost.IsNegative() || in.Salvage.IsNegative() || in.Salvage.GreaterThan(in.Cost) {
		return nil, fail(CodeInvalidDepreciation, "salvage must be between 0 and cost")
	}
	minor := money.MinorUnits(in.Currency)
	basis := in.Cost.Sub(in.Salvage)
	switch in.Method {
	case MethodStraightLine:
		if in.Life < 1 || in.Life > maxMonthlyLife {
			return nil, fail(CodeInvalidDepreciation, "straight-line life must be 1 to 1200 months")
		}
		return straightLine(in, basis, minor)
	case MethodDecliningBalance:
		if in.Life < 1 || in.Life > maxAnnualLife {
			return nil, fail(CodeInvalidDepreciation, "declining-balance life must be 1 to 100 years")
		}
		if in.Factor.LessThan(one) || in.Factor.GreaterThan(decimal.NewFromInt(3)) {
			return nil, fail(CodeInvalidDepreciation, "declining-balance factor must be between 1 and 3")
		}
		return declining(in, basis, minor)
	default:
		return nil, fail(CodeInvalidDepreciation, "unknown depreciation method "+string(in.Method))
	}
}

func straightLineFirst(c Convention) (decimal.Decimal, error) {
	switch c {
	case ConventionNone, ConventionFullMonth:
		return one, nil
	case ConventionMidMonth15:
		return half, nil
	}
	return decimal.Zero, fail(CodeInvalidDepreciation, "convention "+string(c)+" does not apply to straight-line monthly schedules")
}

func decliningFirst(c Convention, inService time.Time) (decimal.Decimal, error) {
	switch c {
	case ConventionNone:
		return one, nil
	case ConventionHalfYear:
		return half, nil
	case ConventionMidQuarter:
		return MidQuarterFirstYear(inService), nil
	}
	return decimal.Zero, fail(CodeInvalidDepreciation, "convention "+string(c)+" does not apply to annual declining-balance schedules")
}

func straightLine(in Input, basis decimal.Decimal, minor int32) ([]Line, error) {
	first, err := straightLineFirst(in.Convention)
	if err != nil {
		return nil, err
	}
	life := decimal.NewFromInt(int64(in.Life))
	consumed := decimal.Zero
	cum := decimal.Zero
	var lines []Line
	for i := 0; consumed.LessThan(life); i++ {
		step := one
		if i == 0 {
			step = first
		}
		if rem := life.Sub(consumed); step.GreaterThan(rem) {
			step = rem
		}
		consumed = consumed.Add(step)
		target := basis.Mul(consumed).Div(life).Round(minor)
		period := time.Date(in.InService.Year(), in.InService.Month()+time.Month(i), 1, 0, 0, 0, 0, time.UTC).Format("2006-01")
		lines = append(lines, Line{Index: i + 1, Period: period, Amount: target.Sub(cum), Accumulated: target, NBV: in.Cost.Sub(target)})
		cum = target
	}
	return lines, nil
}

func declining(in Input, basis decimal.Decimal, minor int32) ([]Line, error) {
	first, err := decliningFirst(in.Convention, in.InService)
	if err != nil {
		return nil, err
	}
	life := decimal.NewFromInt(int64(in.Life))
	rate := in.Factor.Div(life)
	consumed := decimal.Zero
	cum := decimal.Zero
	var lines []Line
	for i := 0; consumed.LessThan(life); i++ {
		step := one
		if i == 0 {
			step = first
		}
		rem := life.Sub(consumed)
		if step.GreaterThan(rem) {
			step = rem
		}
		left := basis.Sub(cum)
		var amount decimal.Decimal
		if consumed.Add(step).GreaterThanOrEqual(life) {
			amount = left // final period plug: clears the residual
		} else {
			amount = left.Mul(rate).Mul(step)
			if in.SwitchToStraightLine {
				if sl := left.Div(rem).Mul(step); sl.GreaterThan(amount) {
					amount = sl
				}
			}
		}
		amount = amount.Round(minor)
		if amount.GreaterThan(left) {
			amount = left
		}
		cum = cum.Add(amount)
		consumed = consumed.Add(step)
		period := fmt.Sprintf("%04d", in.InService.Year()+i)
		lines = append(lines, Line{Index: i + 1, Period: period, Amount: amount, Accumulated: cum, NBV: in.Cost.Sub(cum)})
	}
	return lines, nil
}

// TotalAmount sums the Amount of every line.
func TotalAmount(lines []Line) decimal.Decimal {
	t := decimal.Zero
	for _, l := range lines {
		t = t.Add(l.Amount)
	}
	return t
}
