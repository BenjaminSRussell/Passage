package domain_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/money"
	"github.com/BenjaminSRussell/depot/services/receiving/internal/domain"
)

func TestResolvePolicy_PrecedenceIsVendorItemThenVendorThenItemThenLegalEntity(t *testing.T) {
	policies := []domain.MatchingPolicy{
		{ID: "entity"},
		{ID: "item", ItemID: "i1"},
		{ID: "vendor", VendorID: "v1"},
		{ID: "vendor-item", VendorID: "v1", ItemID: "i1"},
		{ID: "other-vendor", VendorID: "v2", ItemID: "i1"},
	}
	for _, tc := range []struct{ vendor, item, want string }{
		{"v1", "i1", "vendor-item"},
		{"V1", "I1", "vendor-item"},
		{"v1", "i2", "vendor"},
		{"v3", "i1", "item"},
		{"v3", "i2", "entity"},
		{"v1", "", "vendor"},
	} {
		got, ok := domain.ResolvePolicy(policies, tc.vendor, tc.item)
		if !ok || got.ID != tc.want {
			t.Errorf("vendor %q item %q resolved %q (found %v), want %q", tc.vendor, tc.item, got.ID, ok, tc.want)
		}
	}
	if _, ok := domain.ResolvePolicy(policies[1:2], "v1", "i2"); ok {
		t.Error("an item policy for another item must not match")
	}
	if _, ok := domain.ResolvePolicy(nil, "v1", "i1"); ok {
		t.Error("no stored policy must resolve to nothing so the default applies")
	}
}

func TestExceptionReasons_NamesTheFailingLinesAndTheTotal(t *testing.T) {
	zero := money.Amount{Value: decimal.Zero, Currency: "USD"}
	res := domain.Result{Status: domain.TotalVariance, Lines: []domain.LineResult{
		{Status: domain.Matched, PriceVariance: zero}, {Status: domain.PriceVariance, PriceVariance: zero}, {Status: domain.QtyVariance, PriceVariance: zero},
	}}
	if got, want := domain.ExceptionReasons(res), "line 2: PRICE_VARIANCE; line 3: QTY_VARIANCE; total: TOTAL_VARIANCE"; got != want {
		t.Fatalf("reasons %q, want %q", got, want)
	}
	if domain.IsVariance(domain.NoReceipt) || domain.IsVariance(domain.Matched) || !domain.IsVariance(domain.PriceVariance) {
		t.Fatal("only price, quantity and total variances raise an exception")
	}
}

func TestEvaluateWith_AppliesEachLinesOwnPolicy(t *testing.T) {
	strict := domain.DefaultPolicy()
	strict.PriceTolPct, strict.PriceTolAbs = decimal.Zero, decimal.Zero
	in := domain.Invoice{Currency: "USD", HeaderTotal: decimal.RequireFromString("21"), Lines: []domain.InvoiceLine{
		{Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(11), POUnitPrice: decimal.NewFromInt(10), ReceiptState: domain.Posted, ReceivedUninvoiced: decimal.NewFromInt(1)},
		{Qty: decimal.NewFromInt(1), UnitPrice: decimal.NewFromInt(10), POUnitPrice: decimal.NewFromInt(10), ReceiptState: domain.Posted, ReceivedUninvoiced: decimal.NewFromInt(1)},
	}}
	if res := domain.Evaluate(domain.DefaultPolicy(), in); res.Lines[0].Status != domain.Matched {
		t.Fatalf("the default tolerance accepts the first line, got %s", res.Lines[0].Status)
	}
	res := domain.EvaluateWith(domain.DefaultPolicy(), func(i int) domain.Policy {
		if i == 0 {
			return strict
		}
		return domain.DefaultPolicy()
	}, in)
	if res.Lines[0].Status != domain.PriceVariance || res.Lines[1].Status != domain.Matched || res.Status != domain.PriceVariance {
		t.Fatalf("per-line policies not applied: %+v", res)
	}
}
