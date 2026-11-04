package store_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
	purchasingevents "github.com/BenjaminSRussell/passage/services/purchasing/internal/events"
	"github.com/BenjaminSRussell/passage/services/purchasing/internal/store"
)

func applyCancelled(t *testing.T, pool *pgxpool.Pool, s *store.Postgres, n int, in store.DropShipCancelledIn) (store.InboundChange, bool) {
	t.Helper()
	ctx := consumerContext(n)
	var out store.InboundChange
	var recorded bool
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) (err error) {
		out, recorded, err = s.ApplyDropShipCancelled(ctx, tx, in)
		return err
	})
	if err != nil {
		t.Fatalf("apply drop-ship cancelled: %v", err)
	}
	return out, recorded
}

func cancelling(po store.PurchaseOrder, lineNo int, qty, confirmed string) store.DropShipCancelledIn {
	return store.DropShipCancelledIn{EventID: ids.New(), LegalEntityID: legalEntity, DropShipID: ids.New(), DropShipNumber: "DS-0000002",
		PurchaseOrderID: po.ID, PurchaseOrderLineID: po.Lines[lineNo-1].ID, Qty: dec(qty), QtyConfirmed: dec(confirmed)}
}

func countEvents(t *testing.T, ctx context.Context, pool *pgxpool.Pool, typ string) int {
	t.Helper()
	n := 0
	for _, r := range outbox(t, ctx, pool) {
		if r.Type == typ {
			n++
		}
	}
	return n
}

// A drop-ship cancelled while open takes its quantity off the PO line the way a closing does (slice 19):
// the next version is confirmed at once, the drop-ship id is the idempotency key, what earlier closings of
// the drop-ship took off and what was confirmed stay on the line, and cancelling the whole of the only
// open line cancels the order.
func TestInbound_DropShipCancelled(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(87)
	po := inboundPO(t, ctx, s, ids.New())

	in := cancelling(po, 1, "30", "0")
	got, recorded := applyCancelled(t, pool, s, 87, in)
	if !recorded || got.Status != domain.InboundApplied || got.Source != domain.SourceDropShipCancelled || got.Action != domain.ActionCancelDropShip ||
		got.SourceKey != in.DropShipID || got.VersionNo != 2 || got.QtyAfter == nil || !got.QtyAfter.Equal(dec("70")) ||
		got.RequestedQty == nil || !got.RequestedQty.Equal(dec("30")) || got.Attempts != 1 || got.Resolution != "" || got.Note != "DS-0000002" {
		t.Fatalf("cancel 30 of 100 = %+v", got)
	}
	cur, _ := s.GetPurchaseOrder(ctx, po.ID)
	if cur.VersionNo != 2 || !cur.Lines[0].Qty.Equal(dec("70")) || !cur.Total.Equal(dec("700")) {
		t.Fatalf("PO after the cancellation: v%d qty %s total %s", cur.VersionNo, cur.Lines[0].Qty, cur.Total)
	}

	// The same cancellation delivered again changes nothing.
	in.EventID = ids.New()
	if same, recorded := applyCancelled(t, pool, s, 87, in); recorded || same.ID != got.ID {
		t.Fatalf("replayed cancellation = %+v recorded %v", same, recorded)
	}

	// A drop-ship closed for 5 and then cancelled (ordered 20, 10 confirmed): 20 - 10 - 5 = 5 comes off.
	closed := closing(cur, 1, "5")
	if c, _ := applyClosed(t, pool, s, 87, closed); c.Status != domain.InboundApplied {
		t.Fatalf("closing = %+v", c)
	}
	both := cancelling(cur, 1, "20", "10")
	both.DropShipID = closed.DropShipID
	rest, _ := applyCancelled(t, pool, s, 87, both)
	if rest.Status != domain.InboundApplied || rest.RequestedQty == nil || !rest.RequestedQty.Equal(dec("5")) || rest.QtyAfter == nil || !rest.QtyAfter.Equal(dec("60")) {
		t.Fatalf("cancel after a closing = %+v", rest)
	}

	// Everything already shipped or closed: nothing is left to cancel.
	none := cancelling(cur, 1, "10", "10")
	if ign, recorded := applyCancelled(t, pool, s, 87, none); !recorded || ign.Status != domain.InboundIgnored || ign.ReasonCode != domain.CodeInboundQuantity {
		t.Fatalf("nothing left = %+v", ign)
	}

	// An unknown line, and another tenant, record nothing.
	unknown := cancelling(cur, 1, "1", "0")
	unknown.PurchaseOrderLineID = ids.New()
	if _, recorded := applyCancelled(t, pool, s, 87, unknown); recorded {
		t.Fatal("unknown line was recorded")
	}
	if _, recorded := applyCancelled(t, pool, s, 88, cancelling(cur, 1, "1", "0")); recorded {
		t.Fatal("other tenant changed the PO")
	}

	// Cancelling all of the only open line cancels the order; a line with receipts refuses.
	single := inboundPO(t, ctx, s, ids.New())
	all, _ := applyCancelled(t, pool, s, 87, cancelling(single, 1, "100", "0"))
	if c, _ := s.GetPurchaseOrder(ctx, single.ID); all.Status != domain.InboundApplied || c.ApprovalStatus != domain.POStatusCancelled {
		t.Fatalf("cancel all: %+v, PO %s", all, c.ApprovalStatus)
	}
	received := inboundPO(t, ctx, s, ids.New())
	setLineQuantities(t, ctx, pool, received.ID, "10", "0")
	if r, _ := applyCancelled(t, pool, s, 87, cancelling(received, 1, "100", "0")); r.Status != domain.InboundRefused || r.ReasonCode != domain.CodeInboundQuantity {
		t.Fatalf("cancel all of a received line = %+v", r)
	}
}

// A refused change leaves a notification event for the buyer, and a buyer settles it two ways (slice 19):
// acknowledge it, or retry it once the cause is gone. Both are one-time, keep the one row and its key, and
// write inboundchange.resolved.v1.
func TestInbound_SettleRefused(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(89)
	po := inboundPO(t, ctx, s, ids.New())
	// The vendor delivered 90: closing 20 goes below the received floor (PRC-PO-007).
	setLineQuantities(t, ctx, pool, po.ID, "90", "0")

	refused, _ := applyClosed(t, pool, s, 89, closing(po, 1, "20"))
	if refused.Status != domain.InboundRefused || refused.ReasonCode != domain.CodeReceivedFloor || refused.Attempts != 1 || refused.Resolution != "" {
		t.Fatalf("closing below received = %+v", refused)
	}
	if n := countEvents(t, ctx, pool, purchasingevents.InboundChangeRefused); n != 1 {
		t.Fatalf("refused events = %d", n)
	}
	for _, r := range outbox(t, ctx, pool) {
		if r.Type == purchasingevents.InboundChangeRefused && (r.Data["buyer_id"] != po.CreatedBy || r.Data["status"] != "REFUSED" || r.Data["change_id"] != refused.ID) {
			t.Fatalf("refused event = %v, want buyer %s", r.Data, po.CreatedBy)
		}
	}
	queue, _ := s.ListInboundChanges(ctx, store.InboundFilter{Status: domain.InboundRefused, Resolution: "NONE"}, "", 10)
	if len(queue.Items) != 1 || queue.Items[0].ID != refused.ID {
		t.Fatalf("buyer queue = %d", len(queue.Items))
	}

	// Retried while the cause is still there: refused again with the new attempt, still in the queue, no event.
	retried, err := s.RetryInboundChange(ctx, refused.ID, "", store.Idem{Key: "retry-1", Hash: "h1"})
	if err != nil || retried.Status != domain.InboundRefused || retried.Attempts != 2 || retried.Resolution != "" || retried.ID != refused.ID {
		t.Fatalf("retry with the cause in place = %+v, %v", retried, err)
	}
	if n := countEvents(t, ctx, pool, purchasingevents.InboundChangeResolved); n != 0 {
		t.Fatalf("resolved events after a failed retry = %d", n)
	}

	// The buyer fixes it: the vendor's receipt is corrected to 50. The retry now applies version 2.
	setLineQuantities(t, ctx, pool, po.ID, "50", "0")
	done, err := s.RetryInboundChange(ctx, refused.ID, "receipt corrected", store.Idem{Key: "retry-2", Hash: "h2"})
	if err != nil || done.Status != domain.InboundApplied || done.Resolution != domain.ResolutionRetried || done.Attempts != 3 || done.VersionNo != 2 ||
		done.ResolvedBy == "" || done.ResolvedAt == nil || done.ResolutionNote != "receipt corrected" || done.QtyAfter == nil || !done.QtyAfter.Equal(dec("80")) ||
		done.ReasonCode != "" || done.Reason != "" {
		t.Fatalf("retry after the fix = %+v, %v", done, err)
	}
	cur, _ := s.GetPurchaseOrder(ctx, po.ID)
	if cur.VersionNo != 2 || !cur.Lines[0].Qty.Equal(dec("80")) {
		t.Fatalf("PO after the retry: v%d qty %s", cur.VersionNo, cur.Lines[0].Qty)
	}
	if n := countEvents(t, ctx, pool, purchasingevents.InboundChangeResolved); n != 1 {
		t.Fatalf("resolved events = %d", n)
	}
	// The closing's own idempotency key still holds: a new event for it changes nothing.
	again := closing(po, 1, "20")
	again.DropShipID = refused.SourceKey[:36]
	if same, recorded := applyClosed(t, pool, s, 89, again); recorded || same.ID != refused.ID || same.Status != domain.InboundApplied {
		t.Fatalf("the closing again = %+v recorded %v", same, recorded)
	}

	// A settled change cannot be settled again, and a replayed key returns it as it is.
	for name, call := range map[string]func() error{
		"retry again": func() error {
			_, err := s.RetryInboundChange(ctx, refused.ID, "", store.Idem{Key: "retry-3", Hash: "h3"})
			return err
		},
		"acknowledge after": func() error {
			_, err := s.AcknowledgeInboundChange(ctx, refused.ID, "", store.Idem{Key: "ack-x", Hash: "hx"})
			return err
		},
	} {
		if problemCode(call()) != domain.CodeInboundNotOpen {
			t.Errorf("%s: want %s", name, domain.CodeInboundNotOpen)
		}
	}
	if replay, err := s.RetryInboundChange(ctx, refused.ID, "receipt corrected", store.Idem{Key: "retry-2", Hash: "h2"}); err != nil || replay.Attempts != 3 {
		t.Fatalf("replayed retry = %+v, %v", replay, err)
	}
	if _, err := s.RetryInboundChange(ctx, refused.ID, "", store.Idem{Key: "retry-2", Hash: "other"}); err == nil {
		t.Fatalf("reused key with another body = %v", err)
	}

	// Acknowledge: the buyer dealt with it by hand. The order is not touched, the status stays REFUSED.
	ack := inboundPO(t, ctx, s, ids.New())
	setLineQuantities(t, ctx, pool, ack.ID, "95", "0")
	r2, _ := applyClosed(t, pool, s, 89, closing(ack, 1, "20"))
	if r2.Status != domain.InboundRefused {
		t.Fatalf("second refused change = %+v", r2)
	}
	got, err := s.AcknowledgeInboundChange(ctx, r2.ID, "vendor will ship the rest", store.Idem{Key: "ack-1", Hash: "a1"})
	if err != nil || got.Status != domain.InboundRefused || got.Resolution != domain.ResolutionAcknowledged || got.ResolutionNote != "vendor will ship the rest" ||
		got.ResolvedAt == nil || got.ResolvedBy == "" || got.Attempts != 1 {
		t.Fatalf("acknowledge = %+v, %v", got, err)
	}
	if c, _ := s.GetPurchaseOrder(ctx, ack.ID); c.VersionNo != 1 {
		t.Fatalf("acknowledge changed the PO to version %d", c.VersionNo)
	}
	if _, err := s.RetryInboundChange(ctx, r2.ID, "", store.Idem{Key: "retry-4", Hash: "h4"}); problemCode(err) != domain.CodeInboundNotOpen {
		t.Fatalf("retry of an acknowledged change = %v", err)
	}
	queue, _ = s.ListInboundChanges(ctx, store.InboundFilter{Status: domain.InboundRefused, Resolution: "NONE"}, "", 10)
	acked, _ := s.ListInboundChanges(ctx, store.InboundFilter{Resolution: domain.ResolutionAcknowledged}, "", 10)
	if len(queue.Items) != 0 || len(acked.Items) != 1 || acked.Items[0].ID != r2.ID {
		t.Fatalf("queue %d, acknowledged %d", len(queue.Items), len(acked.Items))
	}
	if n := countEvents(t, ctx, pool, purchasingevents.InboundChangeResolved); n != 2 {
		t.Fatalf("resolved events = %d", n)
	}

	// An applied change and an unknown id are not settleable; another tenant cannot see the change.
	applied, _ := applyClosed(t, pool, s, 89, closing(inboundPO(t, ctx, s, ids.New()), 1, "1"))
	if _, err := s.AcknowledgeInboundChange(ctx, applied.ID, "", store.Idem{}); problemCode(err) != domain.CodeInboundNotOpen {
		t.Fatalf("acknowledge an applied change = %v", err)
	}
	if _, err := s.RetryInboundChange(ctx, ids.New(), "", store.Idem{}); problemCode(err) != store.CodeInboundChangeNotFound {
		t.Fatalf("retry of an unknown change = %v", err)
	}
	if _, err := s.RetryInboundChange(principalContext(90), r2.ID, "", store.Idem{}); problemCode(err) != store.CodeInboundChangeNotFound {
		t.Fatalf("another tenant retries = %v", err)
	}
}

// A refused planning message is retried from its stored request: the warehouse scope, quantity and date
// come back from the row, and a change refused because the order was in review applies once it is confirmed.
func TestInbound_RetryPlanningMessage(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(91)
	po := inboundPO(t, ctx, s, ids.New())

	// An increase past 5 % puts the order in review; the next message is refused until it is confirmed.
	inc, _ := applyMessage(t, pool, s, 91, message(po, domain.ActionIncrease, "10", ""))
	if !inc.ReapprovalRequired {
		t.Fatalf("increase = %+v", inc)
	}
	wh := ids.New()
	msg := message(po, domain.ActionPostpone, "110", "2027-04-02")
	msg.WarehouseID = wh
	held, _ := applyMessage(t, pool, s, 91, msg)
	if held.Status != domain.InboundRefused || held.ReasonCode != domain.CodeInboundOrderState || held.WarehouseID != wh {
		t.Fatalf("postpone while in review = %+v", held)
	}
	cur, _ := s.GetPurchaseOrder(ctx, po.ID)
	walk(t, ctx, s, cur, domain.POActionApprove, domain.POActionConfirm)

	done, err := s.RetryInboundChange(ctx, held.ID, "", store.Idem{Key: "pm-1", Hash: "p1"})
	if err != nil || done.Status != domain.InboundApplied || done.Resolution != domain.ResolutionRetried || done.DateAfter == nil || done.DateAfter.String() != "2027-04-02" ||
		done.Attempts != 2 || done.WarehouseID != wh {
		t.Fatalf("retried postpone = %+v, %v", done, err)
	}
	if c, _ := s.GetPurchaseOrder(ctx, po.ID); c.Lines[0].DeliveryDate.String() != "2027-04-02" {
		t.Fatalf("due date after the retry = %s", c.Lines[0].DeliveryDate)
	}
}

// Settling runs as the calling user, inside the user's own legal-entity scope (never the consumer's): a
// buyer scoped to another legal entity cannot see the change, and the order version a retry writes names
// the buyer as its editor. A retry and an acknowledgement racing on one change are serialized on the
// change row: exactly one settles it, the other gets PRC-INB-008, and one resolved event is written.
func TestInbound_SettleRunsAsCallerAndSerializes(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	const n = 96
	ctx := principalContext(n)
	user := func(le string) context.Context {
		return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), LegalEntityID: le, UserID: ids.New()})
	}
	refusedClosing := func() (store.PurchaseOrder, store.InboundChange) {
		po := inboundPO(t, ctx, s, ids.New())
		setLineQuantities(t, ctx, pool, po.ID, "90", "0")
		c, _ := applyClosed(t, pool, s, n, closing(po, 1, "20"))
		if c.Status != domain.InboundRefused {
			t.Fatalf("closing below received = %+v", c)
		}
		setLineQuantities(t, ctx, pool, po.ID, "50", "0") // the cause is gone: a retry would apply
		return po, c
	}

	// Another legal entity's buyer: the change is not there for them, and the order is not touched.
	po, c := refusedClosing()
	outsider := user("00000000-0000-7000-8000-0000000001e3")
	if _, err := s.RetryInboundChange(outsider, c.ID, "", store.Idem{Key: "out-r", Hash: "o1"}); problemCode(err) != store.CodeInboundChangeNotFound {
		t.Fatalf("retry from another legal entity = %v", err)
	}
	if _, err := s.AcknowledgeInboundChange(outsider, c.ID, "", store.Idem{Key: "out-a", Hash: "o2"}); problemCode(err) != store.CodeInboundChangeNotFound {
		t.Fatalf("acknowledge from another legal entity = %v", err)
	}
	if cur, _ := s.GetPurchaseOrder(ctx, po.ID); cur.VersionNo != 1 {
		t.Fatalf("an out-of-scope retry moved the order to version %d", cur.VersionNo)
	}

	// The buyer of the legal entity retries: the new version is the buyer's edit, not the consumer's.
	buyer := user(legalEntity)
	buyerID := func() string { p, _ := tenant.FromContext(buyer); return p.UserID }()
	done, err := s.RetryInboundChange(buyer, c.ID, "", store.Idem{Key: "in-r", Hash: "i1"})
	if err != nil || done.Status != domain.InboundApplied || done.ResolvedBy != buyerID {
		t.Fatalf("retry by the buyer = %+v, %v", done, err)
	}
	if cur, _ := s.GetPurchaseOrder(ctx, po.ID); cur.VersionNo != 2 || cur.ChangedBy != buyerID {
		t.Fatalf("order after the buyer's retry: v%d changed by %q, want %q", cur.VersionNo, cur.ChangedBy, buyerID)
	}

	// A retry and an acknowledgement at the same moment.
	po, c = refusedClosing()
	before := countEvents(t, ctx, pool, purchasingevents.InboundChangeResolved)
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, 2)
	for i, settle := range []func() error{
		func() error {
			_, err := s.RetryInboundChange(user(legalEntity), c.ID, "", store.Idem{Key: "race-r", Hash: "r"})
			return err
		},
		func() error {
			_, err := s.AcknowledgeInboundChange(user(legalEntity), c.ID, "", store.Idem{Key: "race-a", Hash: "a"})
			return err
		},
	} {
		wg.Add(1)
		go func(i int, settle func() error) {
			defer wg.Done()
			<-start
			errs[i] = settle()
		}(i, settle)
	}
	close(start)
	wg.Wait()
	won := 0
	for _, err := range errs {
		switch {
		case err == nil:
			won++
		case problemCode(err) != domain.CodeInboundNotOpen:
			t.Fatalf("racing settlement failed with %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d settlements won the race (errors %v)", won, errs)
	}
	got, _ := s.GetInboundChange(ctx, c.ID)
	cur, _ := s.GetPurchaseOrder(ctx, po.ID)
	switch got.Resolution {
	case domain.ResolutionRetried:
		if errs[0] != nil || got.Status != domain.InboundApplied || cur.VersionNo != 2 {
			t.Fatalf("retry won: change %+v, order v%d", got, cur.VersionNo)
		}
	case domain.ResolutionAcknowledged:
		if errs[1] != nil || got.Status != domain.InboundRefused || cur.VersionNo != 1 {
			t.Fatalf("acknowledgement won: change %+v, order v%d", got, cur.VersionNo)
		}
	default:
		t.Fatalf("change after the race = %+v", got)
	}
	if n := countEvents(t, ctx, pool, purchasingevents.InboundChangeResolved) - before; n != 1 {
		t.Fatalf("resolved events from the race = %d", n)
	}
}
