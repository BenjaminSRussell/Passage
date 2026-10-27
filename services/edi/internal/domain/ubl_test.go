package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// consistentTotals is a 150.00 invoice: 100.00 at 21 % (S) and 50.00 at 0 % (Z).
func consistentTotals() UBLTotals {
	return UBLTotals{
		Lines:         []TaxedLine{{Category: "S", Percent: d("21"), Amount: d("100.00")}, {Category: "Z", Percent: d("0"), Amount: d("50.00")}},
		LineExtension: d("150.00"), Allowances: d("0"), Charges: d("0"), TaxExclusive: d("150.00"),
		TaxAmount: d("21.00"), TaxInclusive: d("171.00"), Prepaid: d("0"), Rounding: d("0"), Payable: d("171.00"),
		TaxSubtotals: []TaxSubtotal{
			{Category: "S", Percent: d("21"), Taxable: d("100.00"), Tax: d("21.00")},
			{Category: "Z", Percent: d("0"), Taxable: d("50.00"), Tax: d("0.00")},
		},
	}
}

func TestCheckArithmetic(t *testing.T) {
	tests := []struct {
		name      string
		edit      func(*UBLTotals)
		wantCount int
	}{
		{"consistent invoice passes", func(*UBLTotals) {}, 0},
		{"line sum differs from LineExtensionAmount", func(u *UBLTotals) { u.Lines[0].Amount = d("99.00") }, 2},
		{"exclusive ignores allowances", func(u *UBLTotals) { u.Allowances = d("10.00") }, 1},
		{"payable ignores rounding", func(u *UBLTotals) { u.Rounding = d("0.01") }, 1},
		{"category tax is wrong", func(u *UBLTotals) { u.TaxSubtotals[0].Tax = d("20.00") }, 2},
		{"three decimals", func(u *UBLTotals) { u.Payable = d("171.001") }, 2},
		{"a second standard rate needs its own breakdown", func(u *UBLTotals) {
			// 100.00 at 21 % plus a 10.00 line at 9 %, reported as one S breakdown.
			u.Lines = append(u.Lines, TaxedLine{Category: "S", Percent: d("9"), Amount: d("10.00")})
			u.LineExtension, u.TaxExclusive = d("160.00"), d("160.00")
			u.TaxInclusive, u.Payable = d("181.00"), d("181.00")
		}, 1},
		{"breakdowns per rate pass", func(u *UBLTotals) {
			u.Lines = append(u.Lines, TaxedLine{Category: "S", Percent: d("9"), Amount: d("10.00")})
			u.TaxSubtotals = append(u.TaxSubtotals, TaxSubtotal{Category: "S", Percent: d("9"), Taxable: d("10.00"), Tax: d("0.90")})
			u.LineExtension, u.TaxExclusive = d("160.00"), d("160.00")
			u.TaxAmount, u.TaxInclusive, u.Payable = d("21.90"), d("181.90"), d("181.90")
		}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			u := consistentTotals()
			tc.edit(&u)
			issues := CheckArithmetic(u)
			if len(issues) != tc.wantCount {
				t.Fatalf("got %d issues %+v, want %d", len(issues), issues, tc.wantCount)
			}
			for _, i := range issues {
				if i.Code != CodeArithmetic {
					t.Fatalf("issue code = %s", i.Code)
				}
			}
		})
	}
}
