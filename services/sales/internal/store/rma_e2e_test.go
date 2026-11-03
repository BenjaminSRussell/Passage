package store

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	salesevents "github.com/BenjaminSRussell/passage/services/sales/internal/events"
)

type recordingInventory struct{ receipts []ReturnReceipt }

func (r *recordingInventory) ReceiveReturn(_ context.Context, in ReturnReceipt) (string, error) {
	r.receipts = append(r.receipts, in)
	return "rcpt-" + in.LineID, nil
}

type recordingAR struct{ credits []ARCredit }

func (r *recordingAR) PostRMACredit(_ context.Context, in ARCredit) (CreditNote, error) {
	r.credits = append(r.credits, in)
	return CreditNote{OpenItemID: "oi-" + in.RMAID, DocumentNumber: "CN-" + in.RMANumber, JournalPostingID: "je-" + in.RMAID}, nil
}

// TestRMA_ReturnToStockCreditsInventoryAndAR is AC-30.5 / issue #30 AC3:
// authorize → receive → inspect → disposition → credit for 4 × 100.00 USD
// RETURN_TO_STOCK at 15% restocking restocks 4 at original cost and posts
// Dr 4000 400 / Cr 4200 60 / Cr 1200 340 against AR.
func TestRMA_ReturnToStockCreditsInventoryAndAR(t *testing.T) {
	s, pool, ctx := migrated(t)
	inv, ar := &recordingInventory{}, &recordingAR{}
	s.WithInventory(inv).WithAR(ar)
	now := time.Date(2027, 3, 1, 12, 0, 0, 0, time.UTC)
	le, customer, product, site, warehouse := ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	in := NewReturnOrder{LegalEntityID: le, CustomerID: customer, ReturnReasonCode: "DAMAGED", Currency: "USD",
		IdempotencyKey: ids.New(), RequestHash: ids.New(), Actor: "cs-rep",
		Lines: []NewReturnLine{{LineNo: 1, ProductID: product, SiteID: site, WarehouseID: warehouse, Quantity: d(4),
			Unit: "EA", UnitPrice: decimal.RequireFromString("100.00"), Disposition: domain.DispositionReturnToStock}}}
	rma, created, err := s.CreateReturnOrder(ctx, in)
	if err != nil || !created || rma.Status != domain.RMARequested || rma.RMANumber == "" {
		t.Fatalf("create: %+v created=%v err=%v", rma, created, err)
	}

	auth, err := s.AuthorizeReturnOrder(ctx, rma.ID, rma.Version, "cs-rep", now)
	if err != nil || auth.Status != domain.RMAAuthorized || auth.ExpiresAt == nil || *auth.ExpiresAt != "2027-03-31" {
		t.Fatalf("authorize: %+v %v", auth, err)
	}
	recv, err := s.ReceiveReturnOrder(ctx, auth.ID, auth.Version, nil, "wh-clerk", now)
	if err != nil || recv.Status != domain.RMAReceived {
		t.Fatalf("receive: %+v %v", recv, err)
	}
	if len(inv.receipts) != 1 || !inv.receipts[0].Quantity.Equal(d(4)) || !inv.receipts[0].UnitCost.Equal(decimal.RequireFromString("100.00")) ||
		inv.receipts[0].ProductID != product || recv.Lines[0].InventoryReceiptID == nil {
		t.Fatalf("inventory receipt: %+v line=%+v", inv.receipts, recv.Lines)
	}
	insp, err := s.InspectReturnOrder(ctx, recv.ID, recv.Version, "qc", now)
	if err != nil || insp.Status != domain.RMAInspected {
		t.Fatalf("inspect: %+v %v", insp, err)
	}
	disp, err := s.DispositionReturnOrder(ctx, insp.ID, insp.Version, "returns", now)
	if err != nil || disp.Status != domain.RMADispositioned {
		t.Fatalf("disposition: %+v %v", disp, err)
	}
	credited, err := s.CreditReturnOrder(ctx, disp.ID, disp.Version, "ar-clerk", now)
	if err != nil || credited.Status != domain.RMAClosed {
		t.Fatalf("credit: %+v %v", credited, err)
	}
	if !credited.Gross.Equal(decimal.RequireFromString("400")) || !credited.Restocking.Equal(decimal.RequireFromString("60")) ||
		!credited.Net.Equal(decimal.RequireFromString("340")) || credited.CreditNoteID == nil || credited.JournalPostingID == nil {
		t.Fatalf("credit amounts %+v", credited)
	}
	if len(ar.credits) != 1 || !ar.credits[0].Gross.Equal(credited.Gross) || !ar.credits[0].Restocking.Equal(credited.Restocking) ||
		!ar.credits[0].Net.Equal(credited.Net) {
		t.Fatalf("AR credit %+v", ar.credits)
	}
	postings := domain.CreditNoteAmounts{Gross: credited.Gross, Restocking: credited.Restocking, Net: credited.Net}.Postings()
	if len(postings) != 3 || postings[0].Account != domain.AccountSalesReturns || !postings[0].Debit.Equal(decimal.RequireFromString("400")) ||
		postings[1].Account != domain.AccountRestocking || !postings[1].Credit.Equal(decimal.RequireFromString("60")) ||
		postings[2].Account != domain.AccountReceivable || !postings[2].Credit.Equal(decimal.RequireFromString("340")) {
		t.Fatalf("GL postings %v", postings)
	}

	got := outboxTypes(t, pool, rma.ID)
	want := []string{salesevents.ReturnOrderAuthorized, salesevents.ReturnOrderReceived, salesevents.ReturnOrderInspected,
		salesevents.ReturnOrderDispositioned, salesevents.ReturnOrderCredited}
	if len(got) != len(want) {
		t.Fatalf("events %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events %v", got)
		}
	}
}
