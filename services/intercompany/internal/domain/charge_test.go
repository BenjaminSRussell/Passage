package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestPriceCharge_AC4011(t *testing.T) {
	// AC-40.11: 8 hours at cost 60.00 with cost-plus 10 % gives cost 480.00,
	// price 528.00 and IC margin 48.00.
	got, err := PriceCharge(decimal.NewFromInt(8), decimal.NewFromInt(60), decimal.NewFromInt(10), "USD")
	if err != nil {
		t.Fatal(err)
	}
	if got.Cost.String() != "480.00 USD" || got.Price.String() != "528.00 USD" || got.Margin.String() != "48.00 USD" {
		t.Fatalf("charge = cost %s price %s margin %s", got.Cost, got.Price, got.Margin)
	}
}

func TestPriceCharge_Rejects(t *testing.T) {
	cases := []struct {
		name     string
		qty      decimal.Decimal
		cost     decimal.Decimal
		markup   decimal.Decimal
		currency string
	}{
		{"zero quantity", decimal.Zero, decimal.NewFromInt(60), decimal.Zero, "USD"},
		{"negative cost", decimal.NewFromInt(1), decimal.NewFromInt(-1), decimal.Zero, "USD"},
		{"bad currency", decimal.NewFromInt(1), decimal.NewFromInt(1), decimal.Zero, "US"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := PriceCharge(c.qty, c.cost, c.markup, c.currency); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
