package store_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// TestCatchWeightTransactionsValueAtWeight is Wave 24 AC4: a receipt of 10 cases
// at 102 kg and 2.00/kg posts qty 102, cw_qty 10 and cost 204; a sales issue of
// −102 kg at fallback 1.50 costs 153, not 15 (cases × 1.50).
func TestCatchWeightTransactionsValueAtWeight(t *testing.T) {
	s, _, _ := setup(t)
	ctx := entityCtx(1, ids.New())
	product := ids.New()
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)

	receipt, _, err := s.CreateTransaction(ctx, store.CreateTransactionInput{
		LegalEntityID: legalEntity(ctx), SourceType: "RECEIPT_LINE", SourceID: ids.New(), Reference: "RCV-CW",
		ProductID: product, Dims: dims(), Qty: dec("102"), EnteredQty: dec("102"), EnteredUnit: "KG",
		Status: domain.StatusRegistered, Currency: "USD", CostPrice: dec("2"), CWQty: 10, Date: day, IdempotencyKey: ids.New(),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := s.GetTransaction(ctx, receipt.TransactionID)
	if err != nil || !got.Qty.Equal(dec("102")) || got.CWQty == nil || *got.CWQty != 10 || !got.CostAmountPhysical.Equal(dec("204")) {
		t.Fatalf("receipt %+v err=%v, want qty 102 cw_qty 10 cost 204", got, err)
	}

	issue, _, err := s.CreateTransaction(ctx, store.CreateTransactionInput{
		LegalEntityID: legalEntity(ctx), SourceType: "SALES_LINE", SourceID: ids.New(), Reference: "SO-CW",
		ProductID: product, Dims: dims(), Qty: dec("-102"), EnteredQty: dec("-102"), EnteredUnit: "KG",
		Status: domain.StatusOnOrder, Currency: "USD", CostPrice: dec("1.50"), CWQty: 10, Date: day, IdempotencyKey: ids.New(),
		Negative: domain.NegativePolicy{PhysicalAllowed: true, FinancialAllowed: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	sold, _, err := s.GetTransaction(ctx, issue.TransactionID)
	if err != nil || !sold.Qty.Equal(dec("-102")) || sold.CWQty == nil || *sold.CWQty != 10 {
		t.Fatalf("issue %+v err=%v, want qty -102 cw_qty 10", sold, err)
	}
	if !sold.CostAmountPhysical.Equal(dec("-153")) {
		t.Fatalf("issue cost %s, want -153 (weight × 1.50), not -15 (cases × 1.50)", sold.CostAmountPhysical)
	}
}
