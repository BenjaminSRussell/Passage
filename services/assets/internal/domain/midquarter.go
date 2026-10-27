package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// CodeMidQuarterApplies is the AST-0511 warning raised when more than 40% of a
// tax year's placed-in-service basis falls in the fourth quarter (BR-37.5).
const CodeMidQuarterApplies = "AST-0511"

// midQuarterThreshold is the Q4 share above which mid-quarter applies; a share
// of exactly 40% does not trigger it ("more than 40%", IRS Pub. 946).
var midQuarterThreshold = decimal.RequireFromString("0.4")

// midQuarterMonths is the part of the first year charged under mid-quarter:
// an asset is treated as placed in service in the middle of its quarter.
var midQuarterMonths = [4]decimal.Decimal{
	decimal.RequireFromString("10.5"),
	decimal.RequireFromString("7.5"),
	decimal.RequireFromString("4.5"),
	decimal.RequireFromString("1.5"),
}

// Quarter returns the calendar quarter 1..4 of a date. Tax years are calendar
// years until fiscal calendars reach the asset books (NOT-IMPLEMENTED.md D1d).
func Quarter(d time.Time) int {
	return (int(d.Month())-1)/3 + 1
}

// MidQuarterFirstYear is the share of a full year charged in the first year
// under the mid-quarter convention: 10.5/12 for Q1, 7.5/12 for Q2, 4.5/12 for
// Q3 and 1.5/12 for Q4 (IRS Pub. 946 Tables A-2 to A-5).
func MidQuarterFirstYear(inService time.Time) decimal.Decimal {
	return midQuarterMonths[Quarter(inService)-1].Div(decimal.NewFromInt(12))
}

// YearAddition is one book placed in service during a tax year with the basis
// the mid-quarter test counts (its cost).
type YearAddition struct {
	InService time.Time
	Basis     decimal.Decimal
}

// MidQuarterResult is the outcome of the BR-37.5 test for one tax book and
// year. QuarterBasis[i] is the basis placed in service in quarter i+1; Q4Share
// is Q4 basis / total basis rounded to four decimals for display, while Applies
// compares the exact ratio.
type MidQuarterResult struct {
	Year         int                `json:"year"`
	TotalBasis   decimal.Decimal    `json:"totalBasis"`
	QuarterBasis [4]decimal.Decimal `json:"quarterBasis"`
	Q4Share      decimal.Decimal    `json:"q4Share"`
	Applies      bool               `json:"applies"`
	Warnings     []string           `json:"warnings"`
}

// MidQuarterTest applies BR-37.5 to the additions of one tax year: when the
// basis placed in service in Q4 is more than 40% of the year's basis, every
// asset of that year must use the mid-quarter convention (AST-0511 warning).
// Additions dated in other years are ignored; a year without basis does not
// apply.
func MidQuarterTest(year int, additions []YearAddition) MidQuarterResult {
	res := MidQuarterResult{Year: year, TotalBasis: decimal.Zero, Q4Share: decimal.Zero, Warnings: []string{}}
	for i := range res.QuarterBasis {
		res.QuarterBasis[i] = decimal.Zero
	}
	for _, a := range additions {
		if a.InService.Year() != year {
			continue
		}
		res.TotalBasis = res.TotalBasis.Add(a.Basis)
		q := Quarter(a.InService) - 1
		res.QuarterBasis[q] = res.QuarterBasis[q].Add(a.Basis)
	}
	if !res.TotalBasis.IsPositive() {
		return res
	}
	q4 := res.QuarterBasis[3]
	res.Applies = q4.GreaterThan(res.TotalBasis.Mul(midQuarterThreshold))
	res.Q4Share = q4.DivRound(res.TotalBasis, 4)
	if res.Applies {
		res.Warnings = append(res.Warnings, CodeMidQuarterApplies)
	}
	return res
}
