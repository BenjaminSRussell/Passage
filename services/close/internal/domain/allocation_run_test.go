package domain_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

func TestProposeAllocation_FixedPercentagesAndOffset(t *testing.T) {
	dests := []domain.AllocationDest{
		{Key: "A", Account: "6100", Dimensions: "CC=A", Weight: decimal.NewFromInt(30)},
		{Key: "B", Account: "6100", Dimensions: "CC=B", Weight: decimal.NewFromInt(50)},
		{Key: "C", Account: "6100", Dimensions: "CC=C", Weight: decimal.NewFromInt(20)},
	}
	entry, err := domain.ProposeAllocation(decimal.RequireFromString("10000.01"), dests, "6100", "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entry.Destinations) != 3 || !entry.Destinations[1].Amount.Equal(decimal.RequireFromString("5000.01")) {
		t.Fatalf("destinations %+v", entry.Destinations)
	}
	if entry.Offset.Account != "6100" || !entry.Offset.Amount.Equal(decimal.RequireFromString("10000.01")) {
		t.Fatalf("offset %+v", entry.Offset)
	}
	sum := decimal.Zero
	for _, d := range entry.Destinations {
		sum = sum.Add(d.Amount)
	}
	if !sum.Equal(entry.Offset.Amount) {
		t.Fatalf("destinations %s != offset %s", sum, entry.Offset.Amount)
	}
}

func TestProposeAllocation_ZeroBasis(t *testing.T) {
	_, err := domain.ProposeAllocation(decimal.NewFromInt(10), []domain.AllocationDest{{Key: "A", Account: "1", Weight: decimal.Zero}}, "2", "", 2)
	if code(err) != domain.CodeZeroBasis {
		t.Fatalf("got %v", err)
	}
}
