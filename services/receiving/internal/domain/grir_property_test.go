package domain_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/platform/money"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/domain"
)

func TestGrir_NetsToZero(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ctx := context.Background()
		ledger := &domain.FakeLedger{}
		periods := domain.FakePeriods{Status: domain.PeriodOpen}
		lines := rapid.IntRange(1, 1000).Draw(t, "lines")
		var items []*domain.GrirItem
		var receipt domain.Receipt
		for i := 0; i < lines; i++ {
			qty := decimal.NewFromInt(int64(rapid.IntRange(1, 500).Draw(t, "qty")))
			priceCents := int64(rapid.IntRange(1, 1_000_000).Draw(t, "priceCents"))
			receipt.Lines = append(receipt.Lines, domain.ReceiptLine{
				LineNo: i + 1, ProductID: "p", Qty: qty, OrderedRemaining: qty,
				UnitPrice: money.Amount{Value: decimal.New(priceCents, -2), Currency: "USD"},
			})
		}
		receipt.ID, receipt.State = "r", domain.Registered
		_, got, err := receipt.Post(ctx, ledger, testProfile)
		if err != nil {
			t.Fatal(err)
		}
		for i := range got {
			items = append(items, &got[i])
		}
		partial := rapid.Bool().Draw(t, "partialFirst")
		for i, item := range items {
			price := receipt.Lines[i].UnitPrice.Value
			if partial && item.ReceivedQty.Cmp(decimal.NewFromInt(1)) > 0 {
				first := item.ReceivedQty.Div(decimal.NewFromInt(3)).Floor()
				if first.Sign() > 0 {
					if _, err := domain.PostInvoice(ctx, domain.VendorInvoice{ID: "p", PostingDate: august15, Currency: "USD", Lines: []domain.PostLine{{Item: item, Qty: first, UnitPrice: price}}}, periods, ledger, testProfile); err != nil {
						t.Fatal(err)
					}
				}
			}
			rest := item.ReceivedQty.Sub(item.InvoicedQty)
			if _, err := domain.PostInvoice(ctx, domain.VendorInvoice{ID: "f", PostingDate: august15, Currency: "USD", Lines: []domain.PostLine{{Item: item, Qty: rest, UnitPrice: price}}}, periods, ledger, testProfile); err != nil {
				t.Fatal(err)
			}
			if !item.OpenAmount().IsZero() {
				t.Fatalf("line %d open item %s, want 0", i+1, item.OpenAmount())
			}
		}
		if bal := ledger.Balance("2100"); !bal.IsZero() {
			t.Fatalf("GR/IR ledger balance %s, want 0", bal)
		}
	})
}
