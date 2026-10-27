package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

var rfqDeadline = time.Date(2026, 11, 1, 17, 0, 0, 0, time.UTC)

func draftRFQ(t *testing.T) domain.RFQ {
	required := day(t, "2026-12-01")
	return domain.RFQ{LegalEntityID: legalEntity, ReplyDeadline: rfqDeadline, RankingRule: domain.RankLowestPrice,
		Lines: []domain.RFQLine{
			{ProductID: ids.New(), Qty: dec("10"), Unit: "EA"},
			{Description: "Calibration", Qty: dec("4"), Unit: "HR", RequiredDate: &required},
		}}
}

func replyTo(line, price string, lead int, at time.Time) domain.RFQReply {
	return domain.RFQReply{LineID: line, UnitPrice: dec(price), PriceUnit: dec("1"), Currency: "USD", LeadTimeDays: lead, ReceivedAt: at}
}

// sentRFQ creates and sends an RFQ to the vendors, returning it at version 2.
func sentRFQ(t *testing.T, ctx context.Context, s *store.Postgres, vendors ...string) domain.RFQ {
	t.Helper()
	r, err := s.CreateRFQ(ctx, draftRFQ(t), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	r, err = s.SendRFQ(ctx, r.ID, 1, vendors, store.Idem{Key: "send-" + r.ID, Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRFQ_CreateReplayListAndTenantIsolation(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(20)
	first, err := s.CreateRFQ(ctx, draftRFQ(t), store.Idem{Key: "c1", Hash: "h1"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Number == "" || first.Status != domain.RFQDraft || first.Version != 1 || len(first.Lines) != 2 ||
		first.Lines[1].LineNo != 2 || first.Lines[1].RequiredDate.String() != "2026-12-01" || !first.ReplyDeadline.Equal(rfqDeadline) {
		t.Fatalf("created %+v", first)
	}
	replay, err := s.CreateRFQ(ctx, draftRFQ(t), store.Idem{Key: "c1", Hash: "h1"})
	if err != nil || replay.ID != first.ID {
		t.Fatalf("replay = %s, %v; want %s", replay.ID, err, first.ID)
	}
	second := sentRFQ(t, ctx, s, ids.New())
	all, err := s.ListRFQs(ctx, store.RFQFilter{}, "", 0)
	if err != nil || len(all.Items) != 2 || all.Items[0].ID != second.ID || all.Items[0].LineCount != 2 || all.Items[0].VendorCount != 1 {
		t.Fatalf("list = %+v, %v", all, err)
	}
	sent, err := s.ListRFQs(ctx, store.RFQFilter{Statuses: []string{domain.RFQSent, domain.RFQRepliesOpen}}, "", 0)
	if err != nil || len(sent.Items) != 1 || sent.Items[0].Status != domain.RFQSent {
		t.Fatalf("sent filter = %+v, %v", sent, err)
	}
	page, err := s.ListRFQs(ctx, store.RFQFilter{}, "", 1)
	if err != nil || len(page.Items) != 1 || page.Next != second.ID {
		t.Fatalf("page = %+v, %v", page, err)
	}
	if _, err := s.GetRFQ(principalContext(21), first.ID); problemCode(err) != store.CodeRFQNotFound {
		t.Fatalf("other tenant read = %v; want not found", err)
	}
	other := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(20), UserID: ids.New(), LegalEntityID: ids.New()})
	if _, err := s.GetRFQ(other, first.ID); problemCode(err) != store.CodeRFQNotFound {
		t.Fatalf("read outside the legal entity scope = %v; want not found", err)
	}
}

func TestRFQ_SendCloseCancelStateAndVersion(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(22)
	v1, v2 := ids.New(), ids.New()
	r, err := s.CreateRFQ(ctx, draftRFQ(t), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SendRFQ(ctx, r.ID, 5, []string{v1}, store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale send = %v", err)
	}
	sent, err := s.SendRFQ(ctx, r.ID, 1, []string{v1, v2}, store.Idem{Key: "s", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if sent.Status != domain.RFQSent || sent.Version != 2 || len(sent.Invitations) != 2 || sent.Invitations[0].Status != "INVITED" {
		t.Fatalf("sent = %+v", sent)
	}
	again, err := s.SendRFQ(ctx, r.ID, 1, []string{v1, v2}, store.Idem{Key: "s", Hash: "h"})
	if err != nil || again.Version != 2 {
		t.Fatalf("replayed send = %+v, %v; want the RFQ as it is", again, err)
	}
	if _, err := s.SendRFQ(ctx, r.ID, 2, []string{v1}, store.Idem{}); problemCode(err) != domain.CodeRFQState {
		t.Fatalf("second send = %v; want PRC-RFQ-STATE", err)
	}
	events := outbox(t, ctx, pool)
	if len(events) != 1 || events[0].Type != purchasingevents.RFQSent {
		t.Fatalf("outbox = %+v; want one rfq.sent", events)
	}
	if d := events[0].Data; d["rfq_id"] != r.ID || len(d["vendor_ids"].([]any)) != 2 || len(d["lines"].([]any)) != 2 ||
		d["reply_deadline"] != "2026-11-01T17:00:00Z" || d["legal_entity_id"] != legalEntity {
		t.Fatalf("rfq.sent payload = %v", d)
	}
	closed, err := s.ActOnRFQ(ctx, r.ID, 2, domain.RFQActionClose, store.Idem{})
	if err != nil || closed.Status != domain.RFQClosed || closed.Version != 3 {
		t.Fatalf("close = %+v, %v", closed, err)
	}
	if _, err := s.RecordRFQReplies(ctx, r.ID, v1, []domain.RFQReply{replyTo(r.Lines[0].ID, "1", 1, rfqDeadline)}, store.Idem{}); problemCode(err) != domain.CodeRFQState {
		t.Fatalf("reply to a CLOSED RFQ = %v", err)
	}
	cancelled, err := s.ActOnRFQ(ctx, r.ID, 3, domain.RFQActionCancel, store.Idem{})
	if err != nil || cancelled.Status != domain.RFQCancelled {
		t.Fatalf("cancel = %+v, %v", cancelled, err)
	}
}

func TestRFQ_RepliesLateFlagRevisionAndInvitation(t *testing.T) {
	s := store.New(migrated(t))
	ctx := principalContext(23)
	a, b, stranger := ids.New(), ids.New(), ids.New()
	r := sentRFQ(t, ctx, s, a, b)
	l1, l2 := r.Lines[0].ID, r.Lines[1].ID
	before, after := rfqDeadline.Add(-time.Hour), rfqDeadline.Add(time.Minute)

	if _, err := s.RecordRFQReplies(ctx, r.ID, stranger, []domain.RFQReply{replyTo(l1, "1", 1, before)}, store.Idem{}); problemCode(err) != domain.CodeRFQNotInvited {
		t.Fatalf("uninvited vendor = %v", err)
	}
	if _, err := s.RecordRFQReplies(ctx, r.ID, a, []domain.RFQReply{replyTo(ids.New(), "1", 1, before)}, store.Idem{}); problemCode(err) != domain.CodeValidation {
		t.Fatalf("unknown line = %v", err)
	}
	got, err := s.RecordRFQReplies(ctx, r.ID, a, []domain.RFQReply{replyTo(l1, "10", 5, before), replyTo(l2, "120", 2, before)}, store.Idem{Key: "ra", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.RFQRepliesOpen || got.Version != 3 || len(got.Replies) != 2 || got.Replies[0].Late {
		t.Fatalf("after A's replies = %+v", got)
	}
	// A's on-time revision replaces the price; a late revision is refused and keeps it.
	got, err = s.RecordRFQReplies(ctx, r.ID, a, []domain.RFQReply{replyTo(l1, "9.75", 4, before)}, store.Idem{})
	if err != nil || len(got.Replies) != 2 {
		t.Fatalf("revision = %+v, %v", got.Replies, err)
	}
	if _, err := s.RecordRFQReplies(ctx, r.ID, a, []domain.RFQReply{replyTo(l1, "5", 1, after)}, store.Idem{}); problemCode(err) != domain.CodeRFQLateRevision {
		t.Fatalf("late revision of an on-time reply = %v", err)
	}
	got, err = s.RecordRFQReplies(ctx, r.ID, b, []domain.RFQReply{replyTo(l1, "8", 3, after)}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	var fromA, fromB domain.RFQReply
	for _, rp := range got.Replies {
		switch {
		case rp.VendorID == a && rp.LineID == l1:
			fromA = rp
		case rp.VendorID == b:
			fromB = rp
		}
	}
	if !fromA.UnitPrice.Equal(dec("9.75")) || fromA.Late || !fromB.Late || !fromB.ReceivedAt.Equal(after) {
		t.Fatalf("A %+v B %+v; want A revised on time and B late", fromA, fromB)
	}
	for _, inv := range got.Invitations {
		if inv.Status != "REPLIED" {
			t.Errorf("invitation %+v not REPLIED", inv)
		}
	}
}

// AC-17.8 against Postgres: the award creates a DRAFT PO with the reply price
// and lead time, links it to the reply, records the override reason, and is
// written with rfq.awarded in one transaction; a replay returns the same POs.
func TestRFQ_AwardCreatesLinkedPOAndReplays(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(24)
	a, late := ids.New(), ids.New()
	r := sentRFQ(t, ctx, s, a, late)
	l1, l2 := r.Lines[0].ID, r.Lines[1].ID
	before, after := rfqDeadline.Add(-time.Hour), rfqDeadline.Add(time.Hour)
	if _, err := s.RecordRFQReplies(ctx, r.ID, a, []domain.RFQReply{replyTo(l1, "10", 5, before)}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	r, err := s.RecordRFQReplies(ctx, r.ID, late, []domain.RFQReply{replyTo(l2, "100", 2, after)}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	replyOf := func(vendor string) domain.RFQReply {
		for _, rp := range r.Replies {
			if rp.VendorID == vendor {
				return rp
			}
		}
		t.Fatalf("no reply from %s", vendor)
		return domain.RFQReply{}
	}
	ra, rl := replyOf(a), replyOf(late)
	today := day(t, "2026-10-26")
	awards := []domain.Award{{LineID: l1, ReplyID: ra.ID}, {LineID: l2, ReplyID: rl.ID, OverrideReason: "Only quote received"}}
	orders := []store.AwardedOrder{
		awardedOrder(a, r.Lines[0], ra, "100", "2026-10-31"),
		awardedOrder(late, r.Lines[1], rl, "400", "2026-12-01"),
	}

	if _, _, err := s.AwardRFQ(ctx, r.ID, r.Version-1, awards, orders, today, store.Idem{}); problemCode(err) != store.CodeVersionConflict {
		t.Fatalf("stale award = %v", err)
	}
	if _, _, err := s.AwardRFQ(ctx, r.ID, r.Version, []domain.Award{{LineID: l2, ReplyID: rl.ID}}, orders[1:], today, store.Idem{}); problemCode(err) != domain.CodeRFQLate {
		t.Fatalf("late award without reason = %v", err)
	}
	if _, _, err := s.AwardRFQ(ctx, r.ID, r.Version, awards, orders[:1], today, store.Idem{}); err == nil {
		t.Fatalf("orders that do not price every award must be refused")
	}

	out, pos, err := s.AwardRFQ(ctx, r.ID, r.Version, awards, orders, today, store.Idem{Key: "aw", Hash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Status != domain.RFQAwarded || len(pos) != 2 {
		t.Fatalf("award = %s with %d POs", out.Status, len(pos))
	}
	po := pos[0]
	if po.ApprovalStatus != "DRAFT" || po.VendorID != a || !po.Total.Equal(dec("100")) || po.Lines[0].PriceSource != "RFQ_REPLY:"+ra.ID ||
		po.Lines[0].DeliveryDate.String() != "2026-10-31" {
		t.Fatalf("PO %+v", po)
	}
	for _, rp := range out.Replies {
		if !rp.Awarded {
			t.Errorf("reply %s not marked awarded", rp.ID)
		}
		if rp.ID == rl.ID && rp.OverrideReason != "Only quote received" {
			t.Errorf("override reason %q not stored", rp.OverrideReason)
		}
		if rp.ID == ra.ID && rp.OverrideReason != "" {
			t.Errorf("on-time reply got an override reason %q", rp.OverrideReason)
		}
	}
	var linked int
	if err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select count(*) from purchase_order_line where rfq_reply_id is not null`).Scan(&linked)
	}); err != nil || linked != 2 {
		t.Fatalf("linked PO lines = %d, %v", linked, err)
	}

	again, rePOs, err := s.AwardRFQ(ctx, r.ID, r.Version, awards, nil, today, store.Idem{Key: "aw", Hash: "h"})
	if err != nil || again.Status != domain.RFQAwarded || len(rePOs) != 2 || (rePOs[0].ID != pos[0].ID && rePOs[0].ID != pos[1].ID) {
		t.Fatalf("replay = %s, %d POs, %v; want the same POs", again.Status, len(rePOs), err)
	}
	if _, _, err := s.AwardRFQ(ctx, r.ID, out.Version, awards, orders, today, store.Idem{}); problemCode(err) != domain.CodeRFQState {
		t.Fatalf("second award = %v; want PRC-RFQ-STATE", err)
	}

	events := outbox(t, ctx, pool)
	last := events[len(events)-1]
	if last.Type != purchasingevents.RFQAwarded {
		t.Fatalf("last event %s", last.Type)
	}
	lines := last.Data["awards"].([]any)
	first, second := lines[0].(map[string]any), lines[1].(map[string]any)
	if len(lines) != 2 || first["purchase_order_id"] != po.ID || first["vendor_id"] != a || first["unit_price"] != "10" ||
		second["override_reason"] != "Only quote received" || second["line_no"] != float64(2) {
		t.Fatalf("rfq.awarded payload = %v", last.Data)
	}
}

// awardedOrder is a one-line DRAFT PO priced from a reply, as the API builds it.
func awardedOrder(vendor string, l domain.RFQLine, rp domain.RFQReply, total, due string) store.AwardedOrder {
	d, _ := domain.ParseDay(due)
	return store.AwardedOrder{
		Order: store.PurchaseOrder{LegalEntityID: legalEntity, VendorID: vendor, Currency: "USD", ChangeManagement: true,
			Subtotal: dec(total), DiscountTotal: decimal.Zero, ChargesTotal: decimal.Zero, TaxTotal: decimal.Zero, Total: dec(total),
			Lines: []store.PurchaseOrderLine{{LineNo: 1, ProductID: l.ProductID, Description: l.Description, Qty: l.Qty, Unit: l.Unit,
				UnitPrice: rp.UnitPrice, PriceUnit: dec("1"), DiscountPct: decimal.Zero, GrossAmount: dec(total), DiscountAmount: decimal.Zero,
				LineAmount: dec(total), TaxAmount: decimal.Zero, DeliveryDate: d, PriceSource: "RFQ_REPLY:" + rp.ID}}},
		ReplyIDs: []string{rp.ID},
	}
}
