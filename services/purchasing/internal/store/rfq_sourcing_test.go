package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

// rfqFor is a DRAFT RFQ raised for the requisition lines, one RFQ line each.
func rfqFor(lines ...store.RequisitionLine) domain.RFQ {
	in := domain.RFQ{LegalEntityID: legalEntity, ReplyDeadline: rfqDeadline, RankingRule: domain.RankLowestPrice}
	for _, l := range lines {
		in.Lines = append(in.Lines, domain.RFQLine{Description: l.Description, Qty: l.Qty, Unit: l.Unit, RequiredDate: &l.RequestedDate,
			RequisitionLineID: l.ID})
	}
	return in
}

// FR-17.3, FR-17.4: an RFQ raised for requisition lines holds them out of the consolidation queue, a second
// running RFQ and a PO cannot take them, cancelling returns them, and an award orders them through the PO line.
func TestRFQ_RaisedForRequisitionLines(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	req := approved(t, s, ids.New(), "10", "20", "30")
	l1, l2, l3 := req.Lines[0], req.Lines[1], req.Lines[2]

	held, err := s.CreateRFQ(ctx, rfqFor(l1, l2), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if held.Lines[0].RequisitionLineID != l1.ID || held.Lines[1].RequisitionLineID != l2.ID {
		t.Fatalf("lines do not keep their requisition lines: %+v", held.Lines)
	}
	queue, err := s.ListOpenRequisitionLines(ctx, store.SourcingFilter{}, "", 0)
	if err != nil || len(queue.Items) != 1 || queue.Items[0].ID != l3.ID {
		t.Fatalf("queue = %+v, %v; want only the line no RFQ holds", queue.Items, err)
	}
	if _, err := s.CreateRFQ(ctx, rfqFor(l2, l3), store.Idem{}); problemCode(err) != store.CodeRequisitionOnRFQ {
		t.Fatalf("second running RFQ on a held line = %v", err)
	}
	if again, _ := s.ListRFQs(ctx, store.RFQFilter{}, "", 0); len(again.Items) != 1 {
		t.Fatalf("a refused RFQ was written: %d RFQs", len(again.Items))
	}
	if _, err := s.CreatePurchaseOrder(ctx, orderFor(t, ids.New(), l1), store.Idem{}); problemCode(err) != store.CodeRequisitionOnRFQ {
		t.Fatalf("PO on a held line = %v", err)
	}
	dup := rfqFor(l3, l3)
	if _, err := s.CreateRFQ(ctx, dup, store.Idem{}); err == nil {
		t.Fatal("the same requisition line twice on one RFQ was accepted")
	}
	foreign := rfqFor(l3)
	foreign.LegalEntityID = ids.New()
	if _, err := s.CreateRFQ(ctx, foreign, store.Idem{}); problemCode(err) != store.CodeRequisitionLegalEntity {
		t.Fatalf("another legal entity = %v", err)
	}
	if _, err := s.CreateRFQ(ctx, rfqFor(store.RequisitionLine{ID: ids.New(), Qty: dec("1"), Unit: "EA", Description: "x", RequestedDate: day(t, "2026-11-02")}),
		store.Idem{}); problemCode(err) != store.CodeRequisitionLineNotFound {
		t.Fatalf("unknown requisition line = %v", err)
	}

	// Cancelling releases the lines.
	if _, err := s.ActOnRFQ(ctx, held.ID, held.Version, domain.RFQActionCancel, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if queue, _ := s.ListOpenRequisitionLines(ctx, store.SourcingFilter{}, "", 0); len(queue.Items) != 3 {
		t.Fatalf("queue after cancel = %d lines, want 3", len(queue.Items))
	}

	// A new RFQ for two lines, sent, replied and awarded for line 1 only.
	r, err := s.CreateRFQ(ctx, rfqFor(l1, l2), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	vendor := ids.New()
	r, err = s.SendRFQ(ctx, r.ID, r.Version, []string{vendor}, store.Idem{Key: "send", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.RecordRFQReplies(ctx, r.ID, vendor, []domain.RFQReply{replyTo(r.Lines[0].ID, "10", 3, rfqDeadline.Add(-3600e9))}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	reply := r.Replies[0]
	order := awardedOrder(vendor, r.Lines[0], reply, "10", "2026-11-10")
	order.Order.Lines[0].RequisitionLineID = l1.ID
	out, pos, err := s.AwardRFQ(ctx, r.ID, r.Version, []domain.Award{{LineID: r.Lines[0].ID, ReplyID: reply.ID}}, []store.AwardedOrder{order},
		day(t, "2026-10-26"), store.Idem{Key: "aw", Hash: "h"})
	if err != nil || out.Status != domain.RFQAwarded || len(pos) != 1 {
		t.Fatalf("award = %+v, %d POs, %v", out.Status, len(pos), err)
	}
	got, _ := s.GetRequisition(ctx, req.ID)
	if got.Lines[0].Status != "CONSOLIDATED" || got.Lines[0].PurchaseOrderID != pos[0].ID {
		t.Fatalf("awarded requisition line = %+v", got.Lines[0])
	}
	// The line that was not awarded is free again once the RFQ is AWARDED.
	queue, _ = s.ListOpenRequisitionLines(ctx, store.SourcingFilter{}, "", 0)
	if len(queue.Items) != 2 {
		t.Fatalf("queue after award = %d lines, want the 2 not ordered", len(queue.Items))
	}
	var awarded *purchasingevents.RFQAwardedLine
	for _, row := range outbox(t, ctx, pool) {
		if row.Type == purchasingevents.RFQAwarded {
			awards, _ := row.Data["awards"].([]any)
			if len(awards) == 1 && awards[0].(map[string]any)["requisition_line_id"] == l1.ID {
				awarded = &purchasingevents.RFQAwardedLine{}
			}
		}
	}
	if awarded == nil {
		t.Fatal("rfq.awarded.v1 does not carry requisition_line_id")
	}
}

// FR-17.3, FR-17.4: starting the same RFQ again with its key returns it instead of refusing the lines it
// holds; an award that cannot order a requisition line rolls back whole (the RFQ keeps its status and its
// lines); an award to two vendors orders both lines and closes the requisition; and a cancelled PO line
// puts its requisition line back in the queue, since an AWARDED RFQ no longer holds it.
func TestRFQ_RequisitionLineAwardIsAtomicAndReleasesOnCancel(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(31)
	site, warehouse := ids.New(), ids.New()
	draft := requisition(t, ids.New(), "10", "20")
	draft.Lines[1].SiteID, draft.Lines[1].WarehouseID = site, warehouse
	req, err := s.CreateRequisition(ctx, draft, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if req, err = s.SubmitRequisition(ctx, req.ID, 1, nil, "builtin-1", store.Idem{}); err != nil {
		t.Fatal(err)
	}
	l1, l2 := req.Lines[0], req.Lines[1]

	key := store.Idem{Key: "from-req", Hash: "h"}
	r, err := s.CreateRFQ(ctx, rfqFor(l1, l2), key)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateRFQ(ctx, rfqFor(l1, l2), key)
	if err != nil || again.ID != r.ID {
		t.Fatalf("replay = %s, %v; want RFQ %s", again.ID, err, r.ID)
	}

	v1, v2 := ids.New(), ids.New()
	if r, err = s.SendRFQ(ctx, r.ID, r.Version, []string{v1, v2}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	before := rfqDeadline.Add(-3600e9)
	if _, err = s.RecordRFQReplies(ctx, r.ID, v1, []domain.RFQReply{replyTo(r.Lines[0].ID, "10", 3, before)}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if r, err = s.RecordRFQReplies(ctx, r.ID, v2, []domain.RFQReply{replyTo(r.Lines[1].ID, "20", 3, before)}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	replyOf := map[string]domain.RFQReply{}
	for _, rp := range r.Replies {
		replyOf[rp.LineID] = rp
	}
	awards := []domain.Award{{LineID: r.Lines[0].ID, ReplyID: replyOf[r.Lines[0].ID].ID}, {LineID: r.Lines[1].ID, ReplyID: replyOf[r.Lines[1].ID].ID}}
	orders := func(second string) []store.AwardedOrder {
		a := awardedOrder(v1, r.Lines[0], replyOf[r.Lines[0].ID], "10", "2026-11-10")
		a.Order.Lines[0].RequisitionLineID = l1.ID
		b := awardedOrder(v2, r.Lines[1], replyOf[r.Lines[1].ID], "20", "2026-11-10")
		b.Order.Lines[0].RequisitionLineID = second
		return []store.AwardedOrder{a, b}
	}

	// The second PO names a requisition line that is already ordered: nothing of the award stays.
	other := approved(t, s, ids.New(), "5")
	if _, err := s.CreatePurchaseOrder(ctx, orderFor(t, ids.New(), other.Lines[0]), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AwardRFQ(ctx, r.ID, r.Version, awards, orders(other.Lines[0].ID), day(t, "2026-10-26"), store.Idem{}); problemCode(err) != store.CodeRequisitionState {
		t.Fatalf("award with an ordered requisition line = %v", err)
	}
	if cur, _ := s.GetRFQ(ctx, r.ID); cur.Status != domain.RFQRepliesOpen || cur.Version != r.Version {
		t.Fatalf("RFQ after a failed award = %s v%d; want %s v%d", cur.Status, cur.Version, domain.RFQRepliesOpen, r.Version)
	}
	if got, _ := s.GetRequisition(ctx, req.ID); got.Lines[0].Status != "OPEN" || got.Status != store.RequisitionApproved {
		t.Fatalf("requisition after a failed award = %s, line 1 %s", got.Status, got.Lines[0].Status)
	}
	if _, err := s.CreatePurchaseOrder(ctx, orderFor(t, ids.New(), l1), store.Idem{}); problemCode(err) != store.CodeRequisitionOnRFQ {
		t.Fatalf("PO on a line still held after a failed award = %v", err)
	}

	_, pos, err := s.AwardRFQ(ctx, r.ID, r.Version, awards, orders(l2.ID), day(t, "2026-10-26"), store.Idem{})
	if err != nil || len(pos) != 2 {
		t.Fatalf("award = %d POs, %v", len(pos), err)
	}
	got, _ := s.GetRequisition(ctx, req.ID)
	if got.Status != "CLOSED" || got.Lines[0].Status != "CONSOLIDATED" || got.Lines[1].Status != "CONSOLIDATED" {
		t.Fatalf("requisition after the award = %s, lines %s %s; want CLOSED with both ordered", got.Status, got.Lines[0].Status, got.Lines[1].Status)
	}

	var second store.PurchaseOrder
	for _, po := range pos {
		if po.VendorID == v2 {
			second = po
		}
	}
	// The awarded line keeps what consolidation would have taken from the requisition line.
	if got := second.Lines[0]; got.Category != "MRO" || got.SiteID != site || got.WarehouseID != warehouse {
		t.Fatalf("awarded PO line = category %q site %q warehouse %q; want the requisition line's", got.Category, got.SiteID, got.WarehouseID)
	}
	if _, err := s.ActOnPurchaseOrder(ctx, second.ID, second.RowVersion, domain.POActionCancel, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	queue, _ := s.ListOpenRequisitionLines(ctx, store.SourcingFilter{}, "", 0)
	if len(queue.Items) != 1 || queue.Items[0].ID != l2.ID {
		t.Fatalf("queue after the PO is cancelled = %+v; want requisition line 2 back", queue.Items)
	}
}
