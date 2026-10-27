package domain

import (
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

// lpDec parses a decimal literal for the line-pricing tests; "" is zero.
func lpDec(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	return decimal.RequireFromString(s)
}

func TestResolveLinePrice_Policies(t *testing.T) {
	on := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	table := []TransferPrice{
		{Lender: "A", Borrower: "B", Category: CategoryItem, Role: "WIDGET", Method: TPTable, UnitPrice: lpDec("24.00"), Currency: "EUR", EffectiveFrom: on},
		{Lender: "A", Borrower: "B", Category: CategoryItem, Role: "GADGET", Method: TPCostPlus, MarkupPct: lpDec("10"), Currency: "EUR", EffectiveFrom: on},
		{Lender: "A", Borrower: "B", Category: CategoryItem, Role: "BOLT", Method: TPCost, Currency: "EUR", EffectiveFrom: on},
		{Lender: "A", Borrower: "B", Category: CategoryItem, Role: "USDONLY", Method: TPTable, UnitPrice: lpDec("1"), Currency: "USD", EffectiveFrom: on},
		{Lender: "A", Borrower: "B", Category: CategoryItem, Role: "BADMETHOD", Method: "Weird", Currency: "EUR", EffectiveFrom: on},
	}
	cases := []struct {
		name     string
		policy   PricingPolicy
		markup   string
		in       LineInput
		wantUnit string
		wantExt  string
		wantSrc  string
		wantM    string
		wantErr  string
	}{
		{name: "price list", policy: PricePriceList, in: LineInput{Item: "X", Currency: "EUR", On: on, Quantity: lpDec("100"), ListPrice: lpDec("25.00"), UnitCost: lpDec("10")},
			wantUnit: "25.00", wantExt: "2500.00", wantSrc: SourcePartnerPolicy, wantM: "PriceList"},
		{name: "cost plus", policy: PriceCostPlus, markup: "10", in: LineInput{Item: "X", Currency: "EUR", On: on, Quantity: lpDec("8"), ListPrice: lpDec("99"), UnitCost: lpDec("60")},
			wantUnit: "66", wantExt: "528.00", wantSrc: SourcePartnerPolicy, wantM: TPCostPlus},
		{name: "table table method", policy: PriceTable, in: LineInput{Item: "WIDGET", Currency: "EUR", On: on, Quantity: lpDec("3"), UnitCost: lpDec("10")},
			wantUnit: "24.00", wantExt: "72.00", wantSrc: SourceTransferTable, wantM: TPTable},
		{name: "table cost plus method", policy: PriceTable, in: LineInput{Item: "GADGET", Currency: "EUR", On: on, Quantity: lpDec("2"), UnitCost: lpDec("50")},
			wantUnit: "55", wantExt: "110.00", wantSrc: SourceTransferTable, wantM: TPCostPlus},
		{name: "table cost method", policy: PriceTable, in: LineInput{Item: "BOLT", Currency: "EUR", On: on, Quantity: lpDec("4"), UnitCost: lpDec("0.25")},
			wantUnit: "0.25", wantExt: "1.00", wantSrc: SourceTransferTable, wantM: TPCost},
		{name: "table missing item", policy: PriceTable, in: LineInput{Item: "NOPE", Currency: "EUR", On: on, Quantity: lpDec("1")}, wantErr: CodeTransferPrice},
		{name: "table currency mismatch", policy: PriceTable, in: LineInput{Item: "USDONLY", Currency: "EUR", On: on, Quantity: lpDec("1")}, wantErr: CodeInvalid},
		{name: "table unknown method", policy: PriceTable, in: LineInput{Item: "BADMETHOD", Currency: "EUR", On: on, Quantity: lpDec("1")}, wantErr: CodeInvalid},
		{name: "bad currency", policy: PricePriceList, in: LineInput{Item: "X", Currency: "eu", On: on, Quantity: lpDec("1")}, wantErr: CodeInvalid},
		{name: "negative quantity", policy: PricePriceList, in: LineInput{Item: "X", Currency: "EUR", On: on, Quantity: lpDec("-1")}, wantErr: CodeInvalid},
		{name: "zero quantity", policy: PricePriceList, in: LineInput{Item: "X", Currency: "EUR", On: on, Quantity: lpDec("0")}, wantErr: CodeInvalid},
		{name: "negative cost", policy: PriceCostPlus, in: LineInput{Item: "X", Currency: "EUR", On: on, Quantity: lpDec("1"), UnitCost: lpDec("-1")}, wantErr: CodeInvalid},
		{name: "cost plus extended rounds half away from zero once", policy: PriceCostPlus, markup: "12.5", in: LineInput{Item: "X", Currency: "EUR", On: on, Quantity: lpDec("3"), UnitCost: lpDec("0.13")},
			wantUnit: "0.14625", wantExt: "0.44", wantSrc: SourcePartnerPolicy, wantM: TPCostPlus},
		{name: "JPY has no minor unit", policy: PricePriceList, in: LineInput{Item: "X", Currency: "JPY", On: on, Quantity: lpDec("3"), ListPrice: lpDec("333.5")},
			wantUnit: "333.5", wantExt: "1001.00", wantSrc: SourcePartnerPolicy, wantM: "PriceList"},
		{name: "empty item", policy: PricePriceList, in: LineInput{Currency: "EUR", On: on, Quantity: lpDec("1")}, wantErr: CodeInvalid},
		{name: "unknown policy", policy: "Nope", in: LineInput{Item: "X", Currency: "EUR", On: on, Quantity: lpDec("1")}, wantErr: CodeInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := Partner{SellerEntity: "A", BuyerEntity: "B", Pricing: tc.policy, MarkupPct: lpDec(tc.markup)}
			got, err := ResolveLinePrice(p, table, tc.in)
			if tc.wantErr != "" {
				var de *Error
				if !errors.As(err, &de) || de.Code != tc.wantErr {
					t.Fatalf("want %s, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if !got.UnitPrice.Equal(lpDec(tc.wantUnit)) {
				t.Errorf("unit = %s, want %s", got.UnitPrice, tc.wantUnit)
			}
			if got.Extended.Value.StringFixed(2) != tc.wantExt {
				t.Errorf("extended = %s, want %s", got.Extended.Value.StringFixed(2), tc.wantExt)
			}
			if got.Basis.Source != tc.wantSrc || got.Basis.Method != tc.wantM {
				t.Errorf("basis = %+v", got.Basis)
			}
		})
	}
}

func TestResolveLinePrice_EffectiveDate(t *testing.T) {
	jan := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	jul := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	table := []TransferPrice{
		{Lender: "A", Borrower: "B", Category: CategoryItem, Role: "X", Method: TPTable, UnitPrice: lpDec("10"), Currency: "EUR", EffectiveFrom: jan},
		{Lender: "A", Borrower: "B", Category: CategoryItem, Role: "X", Method: TPTable, UnitPrice: lpDec("12"), Currency: "EUR", EffectiveFrom: jul},
	}
	p := Partner{SellerEntity: "A", BuyerEntity: "B", Pricing: PriceTable}
	before := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	got, err := ResolveLinePrice(p, table, LineInput{Item: "X", Currency: "EUR", On: before, Quantity: lpDec("1")})
	if err != nil || !got.UnitPrice.Equal(lpDec("10")) {
		t.Fatalf("March should use January row, got %v %v", got.UnitPrice, err)
	}
	if !got.Basis.EffectiveFrom.Equal(jan) {
		t.Errorf("basis effective from = %s, want the January row", got.Basis.EffectiveFrom)
	}
	got, err = ResolveLinePrice(p, table, LineInput{Item: "X", Currency: "EUR", On: jul, Quantity: lpDec("1")})
	if err != nil || !got.UnitPrice.Equal(lpDec("12")) {
		t.Fatalf("July should use July row, got %v %v", got.UnitPrice, err)
	}
	_, err = ResolveLinePrice(p, nil, LineInput{Item: "X", Currency: "EUR", On: jul, Quantity: lpDec("1")})
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeTransferPrice {
		t.Fatalf("empty table should fail with IC-0821, got %v", err)
	}
}
