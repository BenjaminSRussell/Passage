package domain

import (
	"fmt"
	"strings"

	"github.com/shopspring/decimal"
)

// MethodMACRS depreciates an annual tax book with the IRS MACRS GDS percentage
// tables (Pub. 946 Appendix A, Tables A-1 to A-5): 200% declining balance for
// 3-, 5-, 7- and 10-year property and 150% for 15- and 20-year property, each
// switching to straight line, under the half-year or mid-quarter convention.
// The year's charge is the unadjusted basis times the published percentage,
// so it matches the table to the cent rather than the formula the table was
// built from (the IRS rounds its percentages). Salvage is ignored by MACRS and
// must be zero.
const MethodMACRS Method = "MACRS"

// macrsTables holds IRS Pub. 946 Tables A-1 (half-year) and A-2 to A-5
// (mid-quarter, placed in service in quarter 1 to 4), percent of the
// unadjusted basis by recovery year, transcribed from the published tables.
// Keys: convention row ("HY", "Q1".."Q4"), then the recovery period in years.
var macrsTables = map[string]map[int]string{
	"HY": {
		3:  "33.33 44.45 14.81 7.41",
		5:  "20.00 32.00 19.20 11.52 11.52 5.76",
		7:  "14.29 24.49 17.49 12.49 8.93 8.92 8.93 4.46",
		10: "10.00 18.00 14.40 11.52 9.22 7.37 6.55 6.55 6.56 6.55 3.28",
		15: "5.00 9.50 8.55 7.70 6.93 6.23 5.90 5.90 5.91 5.90 5.91 5.90 5.91 5.90 5.91 2.95",
		20: "3.750 7.219 6.677 6.177 5.713 5.285 4.888 4.522 4.462 4.461 4.462 4.461 4.462 4.461 4.462 4.461 4.462 4.461 4.462 4.461 2.231",
	},
	"Q1": {
		3:  "58.33 27.78 12.35 1.54",
		5:  "35.00 26.00 15.60 11.01 11.01 1.38",
		7:  "25.00 21.43 15.31 10.93 8.75 8.74 8.75 1.09",
		10: "17.50 16.50 13.20 10.56 8.45 6.76 6.55 6.55 6.56 6.55 0.82",
		15: "8.75 9.13 8.21 7.39 6.65 5.99 5.90 5.91 5.90 5.91 5.90 5.91 5.90 5.91 5.90 0.74",
		20: "6.563 7.000 6.482 5.996 5.546 5.130 4.746 4.459 4.459 4.459 4.459 4.460 4.459 4.460 4.459 4.460 4.459 4.460 4.459 4.460 0.565",
	},
	"Q2": {
		3:  "41.67 38.89 14.14 5.30",
		5:  "25.00 30.00 18.00 11.37 11.37 4.26",
		7:  "17.85 23.47 16.76 11.97 8.87 8.87 8.87 3.34",
		10: "12.50 17.50 14.00 11.20 8.96 7.17 6.55 6.55 6.56 6.55 2.46",
		15: "6.25 9.38 8.44 7.59 6.83 6.15 5.91 5.90 5.91 5.90 5.91 5.90 5.91 5.90 5.91 2.21",
		20: "4.688 7.148 6.612 6.116 5.658 5.233 4.841 4.478 4.463 4.463 4.463 4.463 4.463 4.463 4.462 4.463 4.462 4.463 4.462 4.463 1.673",
	},
	"Q3": {
		3:  "25.00 50.00 16.67 8.33",
		5:  "15.00 34.00 20.40 12.24 11.30 7.06",
		7:  "10.71 25.51 18.22 13.02 9.30 8.85 8.86 5.53",
		10: "7.50 18.50 14.80 11.84 9.47 7.58 6.55 6.55 6.56 6.55 4.10",
		15: "3.75 9.63 8.66 7.80 7.02 6.31 5.90 5.90 5.91 5.90 5.91 5.90 5.91 5.90 5.91 3.69",
		20: "2.813 7.289 6.742 6.237 5.769 5.336 4.936 4.566 4.460 4.460 4.460 4.460 4.461 4.460 4.461 4.460 4.461 4.460 4.461 4.460 2.788",
	},
	"Q4": {
		3:  "8.33 61.11 20.37 10.19",
		5:  "5.00 38.00 22.80 13.68 10.94 9.58",
		7:  "3.57 27.55 19.68 14.06 10.04 8.73 8.73 7.64",
		10: "2.50 19.50 15.60 12.48 9.98 7.99 6.55 6.55 6.56 6.55 5.74",
		15: "1.25 9.88 8.89 8.00 7.20 6.48 5.90 5.90 5.90 5.91 5.90 5.91 5.90 5.91 5.90 5.17",
		20: "0.938 7.430 6.872 6.357 5.880 5.439 5.031 4.654 4.458 4.458 4.458 4.458 4.458 4.458 4.458 4.458 4.458 4.459 4.458 4.459 3.901",
	},
}

// MACRSRates returns the published percentages for a recovery period and
// convention row ("HY", "Q1".."Q4"), or false when Pub. 946 has no such row.
func MACRSRates(row string, life int) ([]decimal.Decimal, bool) {
	s, ok := macrsTables[row][life]
	if !ok {
		return nil, false
	}
	fields := strings.Fields(s)
	out := make([]decimal.Decimal, len(fields))
	for i, f := range fields {
		out[i] = decimal.RequireFromString(f)
	}
	return out, true
}

func macrsRow(in Input) (string, error) {
	switch in.Convention {
	case ConventionHalfYear:
		return "HY", nil
	case ConventionMidQuarter:
		return fmt.Sprintf("Q%d", Quarter(in.InService)), nil
	}
	return "", fail(CodeInvalidDepreciation, "MACRS takes the HalfYear or MidQuarter convention")
}

// macrs charges cost x the published percentage each recovery year, rounded
// half-up to the minor unit; the last year takes the remainder so the sum is
// the cost exactly (BR-37.1).
func macrs(in Input, minor int32) ([]Line, error) {
	if !in.Salvage.IsZero() {
		return nil, fail(CodeInvalidDepreciation, "MACRS ignores salvage; the salvage value must be 0")
	}
	row, err := macrsRow(in)
	if err != nil {
		return nil, err
	}
	rates, ok := MACRSRates(row, in.Life)
	if !ok {
		return nil, fail(CodeInvalidDepreciation, "MACRS recovery period must be 3, 5, 7, 10, 15 or 20 years")
	}
	cum := decimal.Zero
	lines := make([]Line, 0, len(rates))
	for i, r := range rates {
		amount := in.Cost.Mul(r).Div(hundred).Round(minor)
		if left := in.Cost.Sub(cum); i == len(rates)-1 || amount.GreaterThan(left) {
			amount = left
		}
		cum = cum.Add(amount)
		lines = append(lines, Line{Index: i + 1, Period: fmt.Sprintf("%04d", in.InService.Year()+i), Amount: amount, Accumulated: cum, NBV: in.Cost.Sub(cum)})
	}
	return lines, nil
}
