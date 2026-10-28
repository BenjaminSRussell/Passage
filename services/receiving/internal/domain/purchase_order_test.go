package domain

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestNetUnitPriceAppliesDiscountEvenWithoutAPriceUnit(t *testing.T) {
	line := PurchaseOrderLine{UnitPrice: decimal.NewFromInt(200), DiscountPct: decimal.NewFromInt(10)}
	if got := line.NetUnitPrice().String(); got != "180" {
		t.Fatalf("net %s", got)
	}
	line.PriceUnit = decimal.NewFromInt(100)
	if got := line.NetUnitPrice().String(); got != "1.8" {
		t.Fatalf("net per unit %s", got)
	}
}
