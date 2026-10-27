package store_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// orderFor is a DRAFT PO line by line for the given requisition lines (one
// EA at 10 each), as the API builds it on consolidation.
func orderFor(t *testing.T, vendor string, lines ...store.RequisitionLine) store.PurchaseOrder {
	po := store.PurchaseOrder{LegalEntityID: legalEntity, VendorID: vendor, Currency: "USD", ChangeManagement: true,
		DiscountTotal: decimal.Zero, ChargesTotal: decimal.Zero, TaxTotal: decimal.Zero}
	for i, l := range lines {
		po.Lines = append(po.Lines, store.PurchaseOrderLine{LineNo: i + 1, Description: "Item", Qty: l.Qty, Unit: l.Unit, UnitPrice: l.EstimatedUnitPrice,
			PriceUnit: dec("1"), DiscountPct: decimal.Zero, GrossAmount: l.Amount, DiscountAmount: decimal.Zero, LineAmount: l.Amount,
			TaxAmount: decimal.Zero, DeliveryDate: l.RequestedDate, PriceSource: "REQUISITION_ESTIMATE", RequisitionLineID: l.ID})
		po.Subtotal = po.Subtotal.Add(l.Amount)
	}
	po.Total = po.Subtotal
	return po
}

// approved creates and auto-approves a requisition (each amount is a line).
func approved(t *testing.T, s *store.Postgres, requester string, amounts ...string) store.Requisition {
	t.Helper()
	ctx := principalContext(31)
	r, err := s.CreateRequisition(ctx, requisition(t, requester, amounts...), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.SubmitRequisition(ctx, r.ID, 1, nil, "builtin-1", store.Idem{})
	if err != nil || r.Status != store.RequisitionApproved {
		t.Fatalf("approve = %v, %v", r.Status, err)
	}
	return r
}

// FR-17.3: consolidation links each requisition line to its PO line, closes a
// fully ordered requisition, replays by key, and never orders a line twice.
func TestConsolidation_LinksLinesClosesRequisitionsAndReplays(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(31)
	requester := ids.New()
	full := approved(t, s, requester, "10", "20")
	partial := approved(t, s, requester, "30", "40")
	draft, err := s.CreateRequisition(ctx, requisition(t, requester, "5"), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}

	queue, err := s.ListOpenRequisitionLines(ctx, store.SourcingFilter{Sourcing: store.SourcingNeedsSourcing}, "", 0)
	if err != nil || len(queue.Items) != 4 || queue.Items[0].RequisitionNumber == "" || queue.Items[0].LegalEntityID != legalEntity {
		t.Fatalf("queue = %+v, %v; want the 4 lines of approved requisitions without a vendor", queue.Items, err)
	}
	if vendorQueue, _ := s.ListOpenRequisitionLines(ctx, store.SourcingFilter{Sourcing: store.SourcingHasVendor}, "", 0); len(vendorQueue.Items) != 0 {
		t.Fatalf("HAS_VENDOR queue = %d lines, want none", len(vendorQueue.Items))
	}

	v1, v2 := ids.New(), ids.New()
	orders := []store.PurchaseOrder{orderFor(t, v1, full.Lines[0], partial.Lines[0]), orderFor(t, v2, full.Lines[1])}
	lineIDs := []string{full.Lines[0].ID, partial.Lines[0].ID, full.Lines[1].ID}
	out, err := s.ConsolidateRequisitionLines(ctx, orders, lineIDs, store.Idem{Key: "c1", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].Lines[1].RequisitionLineID != partial.Lines[0].ID {
		t.Fatalf("POs = %+v", out)
	}

	got, _ := s.GetRequisition(ctx, full.ID)
	if got.Status != "CLOSED" || got.Lines[0].Status != "CONSOLIDATED" || got.Lines[0].PurchaseOrderID != out[0].ID ||
		got.Lines[1].PurchaseOrderID != out[1].ID || got.Lines[1].PurchaseOrderLineNo != 1 {
		t.Fatalf("fully ordered requisition = %+v", got)
	}
	got, _ = s.GetRequisition(ctx, partial.ID)
	if got.Status != store.RequisitionApproved || got.Lines[0].PurchaseOrderLineNo != 2 || got.Lines[1].Status != "OPEN" {
		t.Fatalf("partly ordered requisition = %+v", got)
	}
	if queue, _ := s.ListOpenRequisitionLines(ctx, store.SourcingFilter{}, "", 0); len(queue.Items) != 1 || queue.Items[0].ID != partial.Lines[1].ID {
		t.Fatalf("queue after consolidation = %+v", queue.Items)
	}

	replay, err := s.ConsolidateRequisitionLines(ctx, orders, lineIDs, store.Idem{Key: "c1", Hash: "h"})
	if err != nil || len(replay) != 2 || replay[0].ID != out[0].ID && replay[0].ID != out[1].ID {
		t.Fatalf("replay = %+v, %v; want the same two POs", replay, err)
	}
	if _, err := s.ConsolidateRequisitionLines(ctx, orders[:1], lineIDs[:2], store.Idem{Key: "c2", Hash: "h2"}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("ordering a consolidated line again = %v", err)
	}
	for name, po := range map[string]store.PurchaseOrder{
		"a draft requisition's line": orderFor(t, v1, draft.Lines[0]),
		"an unknown line":            orderFor(t, v1, store.RequisitionLine{ID: ids.New(), Qty: dec("1"), Unit: "EA", EstimatedUnitPrice: dec("1"), Amount: dec("1"), RequestedDate: day(t, "2026-11-02")}),
		"the same line twice":        orderFor(t, v1, partial.Lines[1], partial.Lines[1]),
	} {
		if _, err := s.CreatePurchaseOrder(ctx, po, store.Idem{}); err == nil {
			t.Fatalf("%s: a PO was written", name)
		}
	}
	other := orderFor(t, v1, partial.Lines[1])
	other.LegalEntityID = ids.New()
	if _, err := s.CreatePurchaseOrder(ctx, other, store.Idem{}); problemCode(err) != store.CodeRequisitionLegalEntity {
		t.Fatalf("another legal entity = %v", err)
	}
	if page, _ := s.ListPurchaseOrders(ctx, store.PurchaseOrderFilter{}, "", 0); len(page.Items) != 2 {
		t.Fatalf("POs written = %d, want the 2 consolidated only", len(page.Items))
	}
}
