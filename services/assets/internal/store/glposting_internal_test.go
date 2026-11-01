package store

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestGLNet_AnUnknownTransactionTypeIsAnErrorNotATransfer(t *testing.T) {
	net := map[string]decimal.Decimal{}
	if err := glNet(PlaceholderProfile, "Bogus", decimal.NewFromInt(10), decimal.Zero, decimal.Zero, net); err == nil {
		t.Fatal("expected an error")
	}
	for _, typ := range []string{"Acquisition", "AcquisitionAdj", "Depreciation", "DepreciationAdj", "Impairment", "Revaluation", "Transfer", "Split", "DisposalSale", "DisposalScrap"} {
		net := map[string]decimal.Decimal{}
		if err := glNet(PlaceholderProfile, typ, decimal.NewFromInt(10), decimal.NewFromInt(2), decimal.Zero, net); err != nil {
			t.Fatalf("%s: %v", typ, err)
		}
		total := decimal.Zero
		for _, v := range net {
			total = total.Add(v)
		}
		if !total.IsZero() {
			t.Fatalf("%s does not balance: %v", typ, net)
		}
	}
}
