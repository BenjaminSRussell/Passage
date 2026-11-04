package store_test

import (
	"context"
	"strings"
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

const inboundDuty = "purchasing.inbound.apply"

// consumerContext is the principal of the inbound consumer: a service actor with the apply duty and no
// legal entity, in the tenant of the event.
func consumerContext(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: "service:purchasing-inbound",
		Duties: map[string]struct{}{inboundDuty: {}}})
}

// inboundPO is a confirmed one-line PO (100 at 10.00, due 2027-03-10) for a known product.
func inboundPO(t *testing.T, ctx context.Context, s *store.Postgres, product string) store.PurchaseOrder {
	t.Helper()
	in := lifecyclePO(t, ids.New(), "100", "2027-03-10")
	in.Lines[0].ProductID = product
	return confirmedPO(t, ctx, s, in)
}

func applyClosed(t *testing.T, pool *pgxpool.Pool, s *store.Postgres, n int, in store.DropShipClosedIn) (store.InboundChange, bool) {
	t.Helper()
	ctx := consumerContext(n)
	var out store.InboundChange
	var recorded bool
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) (err error) {
		out, recorded, err = s.ApplyDropShipClosed(ctx, tx, in)
		return err
	})
	if err != nil {
		t.Fatalf("apply drop-ship closed: %v", err)
	}
	return out, recorded
}

func applyMessage(t *testing.T, pool *pgxpool.Pool, s *store.Postgres, n int, in store.PlanningMessageIn) (store.InboundChange, bool) {
	t.Helper()
	ctx := consumerContext(n)
	var out store.InboundChange
	var recorded bool
	err := dbx.WithTenantTx(ctx, pool, func(tx pgx.Tx) (err error) {
		out, recorded, err = s.ApplyPlanningMessage(ctx, tx, in)
		return err
	})
	if err != nil {
		t.Fatalf("apply planning message: %v", err)
	}
	return out, recorded
}

func closing(po store.PurchaseOrder, lineNo int, qty string) store.DropShipClosedIn {
	return store.DropShipClosedIn{EventID: ids.New(), LegalEntityID: legalEntity, DropShipID: ids.New(), DropShipNumber: "DS-0000001",
		PurchaseOrderID: po.ID, PurchaseOrderLineID: po.Lines[lineNo-1].ID, QtyClosed: dec(qty)}
}

func message(po store.PurchaseOrder, action, qty, date string) store.PlanningMessageIn {
	in := store.PlanningMessageIn{EventID: ids.New(), LegalEntityID: legalEntity, MessageID: ids.New(), Action: action, SupplyRef: po.Number,
		ItemID: po.Lines[0].ProductID, Qty: dec(qty), DecidedBy: "planner@example.com", Note: "agreed with the vendor"}
	if date != "" {
		d, err := domain.ParseDay(date)
		if err != nil {
			panic(err)
		}
		in.SuggestedDate = d
	}
	return in
}

// A closed drop-ship lowers the PO line it bought against (FR-17.8): the next version is confirmed at
// once, totals follow, the event rides with the change, a second closing of the same drop-ship changes
// nothing, a quantity below what was received is refused and recorded, and the last open line takes the
// order with it.
func TestInbound_DropShipClosed(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(81)
	product := ids.New()
	po := inboundPO(t, ctx, s, product)
	if !ids.Valid(po.Lines[0].ID) {
		t.Fatalf("line id = %q", po.Lines[0].ID)
	}

	got, recorded := applyClosed(t, pool, s, 81, closing(po, 1, "4"))
	if !recorded || got.Status != domain.InboundApplied || got.Source != domain.SourceDropShipClosed || got.LineNo != 1 || got.VersionNo != 2 ||
		got.ReapprovalRequired || got.QtyBefore == nil || !got.QtyBefore.Equal(dec("100")) || got.QtyAfter == nil || !got.QtyAfter.Equal(dec("96")) ||
		got.PONumber != po.Number || got.PurchaseOrderID != po.ID || got.ProductID != product {
		t.Fatalf("closed 4 of 100 = %+v", got)
	}
	cur, err := s.GetPurchaseOrder(ctx, po.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.VersionNo != 2 || cur.ApprovalStatus != domain.POStatusConfirmed || !cur.Lines[0].Qty.Equal(dec("96")) ||
		!cur.Lines[0].LineAmount.Equal(dec("960")) || !cur.Total.Equal(dec("960")) || cur.ChangedBy != "" {
		t.Fatalf("PO after the closing: v%d %s qty %s amount %s total %s", cur.VersionNo, cur.ApprovalStatus, cur.Lines[0].Qty, cur.Lines[0].LineAmount, cur.Total)
	}
	types := eventTypes(outbox(t, ctx, pool))
	want := []string{purchasingevents.POConfirmed, purchasingevents.POConfirmed, purchasingevents.POVersionCreated, purchasingevents.InboundChangeRecorded}
	if len(types) != len(want) {
		t.Fatalf("outbox = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("outbox = %v, want %v", types, want)
		}
	}

	// The drop-ship id with its running closed total is the idempotency key: the same closing in a second
	// event returns the first change.
	first := closing(po, 1, "4")
	first.DropShipID = strings.SplitN(got.SourceKey, ":", 2)[0]
	same, recorded := applyClosed(t, pool, s, 81, first)
	if recorded || same.ID != got.ID {
		t.Fatalf("replayed drop-ship = %+v recorded %v", same, recorded)
	}
	if cur, _ = s.GetPurchaseOrder(ctx, po.ID); cur.VersionNo != 2 {
		t.Fatalf("replay wrote version %d", cur.VersionNo)
	}

	// The same drop-ship closed again (sales cut the order back, then the buyer closed the rest): qty_closed
	// is fulfillment's running total, so the line goes down by the 2 the total adds, not by 6 again.
	later := first
	later.EventID, later.QtyClosed = ids.New(), dec("6")
	more, recorded := applyClosed(t, pool, s, 81, later)
	if !recorded || more.Status != domain.InboundApplied || more.RequestedQty == nil || !more.RequestedQty.Equal(dec("2")) ||
		more.QtyAfter == nil || !more.QtyAfter.Equal(dec("94")) || more.VersionNo != 3 {
		t.Fatalf("second closing of the drop-ship = %+v", more)
	}
	// An older closing delivered late adds nothing and is recorded as ignored.
	stale := first
	stale.EventID, stale.QtyClosed = ids.New(), dec("5")
	if old, recorded := applyClosed(t, pool, s, 81, stale); !recorded || old.Status != domain.InboundIgnored || !old.RequestedQty.IsZero() {
		t.Fatalf("stale closing = %+v", old)
	}
	if cur, _ = s.GetPurchaseOrder(ctx, po.ID); cur.VersionNo != 3 || !cur.Lines[0].Qty.Equal(dec("94")) {
		t.Fatalf("after the second closing: v%d qty %s", cur.VersionNo, cur.Lines[0].Qty)
	}

	// Below what the vendor delivered: the received floor (PRC-PO-007) refuses, the PO is untouched.
	setLineQuantities(t, ctx, pool, po.ID, "90", "0")
	refused, recorded := applyClosed(t, pool, s, 81, closing(cur, 1, "10"))
	if !recorded || refused.Status != domain.InboundRefused || refused.ReasonCode != domain.CodeReceivedFloor || refused.QtyAfter != nil || refused.VersionNo != 0 {
		t.Fatalf("below received = %+v", refused)
	}
	if cur, _ = s.GetPurchaseOrder(ctx, po.ID); cur.VersionNo != 3 || !cur.Lines[0].Qty.Equal(dec("94")) {
		t.Fatalf("refused closing moved the PO: v%d qty %s", cur.VersionNo, cur.Lines[0].Qty)
	}

	// A line this service does not hold, or one on another order, records nothing.
	other := closing(po, 1, "1")
	other.PurchaseOrderLineID = ids.New()
	if _, recorded := applyClosed(t, pool, s, 81, other); recorded {
		t.Fatal("unknown line was recorded")
	}
	wrongOrder := closing(po, 1, "1")
	wrongOrder.PurchaseOrderID = ids.New()
	if _, recorded := applyClosed(t, pool, s, 81, wrongOrder); recorded {
		t.Fatal("line of another order was recorded")
	}

	// Another tenant sees none of it and cannot apply a change to this PO.
	if page, _ := s.ListInboundChanges(principalContext(82), store.InboundFilter{}, "", 10); len(page.Items) != 0 {
		t.Fatalf("other tenant sees %d changes", len(page.Items))
	}
	if _, recorded := applyClosed(t, pool, s, 82, closing(po, 1, "1")); recorded {
		t.Fatal("other tenant changed the PO")
	}
}

// Closing everything of the only open line cancels the order; with a second line it cancels just the line.
func TestInbound_DropShipClosedCancels(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(83)

	single := inboundPO(t, ctx, s, ids.New())
	got, _ := applyClosed(t, pool, s, 83, closing(single, 1, "100"))
	if got.Status != domain.InboundApplied || got.QtyAfter == nil || !got.QtyAfter.IsZero() {
		t.Fatalf("close all = %+v", got)
	}
	if cur, _ := s.GetPurchaseOrder(ctx, single.ID); cur.ApprovalStatus != domain.POStatusCancelled || cur.Lines[0].Status != domain.POLineCancelled {
		t.Fatalf("PO after closing its only line: %s, line %s", cur.ApprovalStatus, cur.Lines[0].Status)
	}

	in := lifecyclePO(t, ids.New(), "100", "2027-03-10")
	second := in.Lines[0]
	second.LineNo, second.Qty, second.GrossAmount, second.LineAmount = 2, dec("10"), dec("100"), dec("100")
	in.Lines = append(in.Lines, second)
	in.Subtotal, in.Total = dec("1100"), dec("1100")
	two := confirmedPO(t, ctx, s, in)
	got, _ = applyClosed(t, pool, s, 83, closing(two, 2, "10"))
	cur, _ := s.GetPurchaseOrder(ctx, two.ID)
	if got.Status != domain.InboundApplied || got.LineNo != 2 || cur.ApprovalStatus != domain.POStatusConfirmed || cur.Lines[1].Status != domain.POLineCancelled ||
		cur.Lines[0].Status != domain.POLineOpen || !cur.Total.Equal(dec("1000")) {
		t.Fatalf("closing line 2: %+v, PO %s total %s lines %s/%s", got, cur.ApprovalStatus, cur.Total, cur.Lines[0].Status, cur.Lines[1].Status)
	}
	// Closing a line that is cancelled already is nothing to do.
	if got, _ = applyClosed(t, pool, s, 83, closing(two, 2, "1")); got.Status != domain.InboundIgnored || got.ReasonCode != domain.CodeInboundNoLine {
		t.Fatalf("closing a cancelled line = %+v", got)
	}
}

// Planning messages a planner applied (message.applied.v1) move the line of the item on the PO the supply
// reference names: a date move, a decrease, an increase past 5 % that waits for re-approval (and then
// stops later messages while the order is in review), a cancel; the message id is the key; a supply that
// is no PO of this entity records nothing.
func TestInbound_PlanningMessages(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(84)
	po := inboundPO(t, ctx, s, ids.New())

	got, recorded := applyMessage(t, pool, s, 84, message(po, domain.ActionPostpone, "100", "2027-03-17"))
	if !recorded || got.Status != domain.InboundApplied || got.VersionNo != 2 || got.DateBefore == nil || got.DateBefore.String() != "2027-03-10" ||
		got.DateAfter == nil || got.DateAfter.String() != "2027-03-17" || got.RequestedBy != "planner@example.com" || got.Note != "agreed with the vendor" {
		t.Fatalf("postpone = %+v", got)
	}
	cur, _ := s.GetPurchaseOrder(ctx, po.ID)
	if cur.Lines[0].DeliveryDate.String() != "2027-03-17" || cur.VersionNo != 2 || !cur.Lines[0].Qty.Equal(dec("100")) {
		t.Fatalf("PO after postpone: v%d due %s", cur.VersionNo, cur.Lines[0].DeliveryDate)
	}
	v2, err := s.GetPurchaseOrderVersion(ctx, po.ID, 2)
	if err != nil || !hasPatchPath(v2.Patch, "/lines/0/deliveryDate") || v2.ConfirmedAt == nil {
		t.Fatalf("version 2 = %+v, %v", v2, err)
	}

	// The message id is the idempotency key.
	again := message(po, domain.ActionPostpone, "100", "2027-03-17")
	again.MessageID = got.SourceKey
	if same, recorded := applyMessage(t, pool, s, 84, again); recorded || same.ID != got.ID {
		t.Fatalf("same message again = %+v recorded %v", same, recorded)
	}

	// Already there, and the wrong direction.
	if ign, _ := applyMessage(t, pool, s, 84, message(po, domain.ActionAdvance, "100", "2027-03-17")); ign.Status != domain.InboundIgnored || ign.VersionNo != 0 {
		t.Fatalf("advance to the same date = %+v", ign)
	}
	if ref, _ := applyMessage(t, pool, s, 84, message(po, domain.ActionPostpone, "100", "2027-03-01")); ref.Status != domain.InboundRefused || ref.ReasonCode != domain.CodeInboundDate {
		t.Fatalf("postpone to an earlier date = %+v", ref)
	}

	dec20, _ := applyMessage(t, pool, s, 84, message(po, domain.ActionDecrease, "20", ""))
	if dec20.Status != domain.InboundApplied || dec20.QtyAfter == nil || !dec20.QtyAfter.Equal(dec("80")) || dec20.ReapprovalRequired {
		t.Fatalf("decrease 20 = %+v", dec20)
	}
	if cur, _ = s.GetPurchaseOrder(ctx, po.ID); !cur.Total.Equal(dec("800")) {
		t.Fatalf("total after decrease = %s", cur.Total)
	}

	// 80 -> 90 is above 5 %: the change is version 4 waiting for re-approval, and the order is in review.
	inc, _ := applyMessage(t, pool, s, 84, message(po, domain.ActionIncrease, "10", ""))
	if inc.Status != domain.InboundApplied || !inc.ReapprovalRequired || inc.VersionNo != 4 || inc.QtyAfter == nil || !inc.QtyAfter.Equal(dec("90")) {
		t.Fatalf("increase 10 = %+v", inc)
	}
	if cur, _ = s.GetPurchaseOrder(ctx, po.ID); cur.ApprovalStatus != domain.POStatusInReview {
		t.Fatalf("PO after a reapproval change: %s", cur.ApprovalStatus)
	}
	held, _ := applyMessage(t, pool, s, 84, message(po, domain.ActionCancel, "90", ""))
	if held.Status != domain.InboundRefused || held.ReasonCode != domain.CodeInboundOrderState {
		t.Fatalf("cancel while in review = %+v", held)
	}

	// A supply that is no PO here, an action that is not for a PO and another legal entity record nothing.
	notPO := message(po, domain.ActionCancel, "5", "")
	notPO.SupplyRef = "PL-0000001"
	if _, recorded := applyMessage(t, pool, s, 84, notPO); recorded {
		t.Fatal("planned order supply was recorded")
	}
	otherEntity := message(po, domain.ActionCancel, "5", "")
	otherEntity.LegalEntityID = ids.New()
	if _, recorded := applyMessage(t, pool, s, 84, otherEntity); recorded {
		t.Fatal("another legal entity's message was recorded")
	}

	page, err := s.ListInboundChanges(ctx, store.InboundFilter{PurchaseOrderID: po.ID}, "", 50)
	if err != nil || len(page.Items) != 6 || page.Items[0].ID != held.ID {
		t.Fatalf("list = %d items (first %v), %v", len(page.Items), page.Items[0].Action, err)
	}
	refusedOnly, _ := s.ListInboundChanges(ctx, store.InboundFilter{Status: domain.InboundRefused, Source: domain.SourcePlanningMessage}, "", 50)
	if len(refusedOnly.Items) != 2 {
		t.Fatalf("refused = %d", len(refusedOnly.Items))
	}
	first, _ := s.ListInboundChanges(ctx, store.InboundFilter{}, "", 2)
	rest, _ := s.ListInboundChanges(ctx, store.InboundFilter{}, first.Next, 50)
	if first.Next == "" || len(first.Items) != 2 || len(rest.Items) != 4 || rest.Items[0].ID == first.Items[1].ID {
		t.Fatalf("paging: %d then %d (next %q)", len(first.Items), len(rest.Items), first.Next)
	}
	one, err := s.GetInboundChange(ctx, got.ID)
	if err != nil || one.ID != got.ID || one.Action != domain.ActionPostpone {
		t.Fatalf("get = %+v, %v", one, err)
	}
	if _, err := s.GetInboundChange(principalContext(85), got.ID); problemCode(err) != store.CodeInboundChangeNotFound {
		t.Fatalf("another tenant reads the change = %v", err)
	}
}

// A message names a supply, not a line: with two open lines of the item a decrease is refused as ambiguous,
// and a date move is narrowed to the line whose open quantity equals the message quantity.
func TestInbound_PlanningAmbiguity(t *testing.T) {
	pool := migrated(t)
	s := store.New(pool)
	ctx := principalContext(86)
	product := ids.New()
	in := lifecyclePO(t, ids.New(), "100", "2027-03-10")
	in.Lines[0].ProductID = product
	second := in.Lines[0]
	second.LineNo, second.Qty, second.GrossAmount, second.LineAmount = 2, dec("10"), dec("100"), dec("100")
	in.Lines = append(in.Lines, second)
	in.Subtotal, in.Total = dec("1100"), dec("1100")
	po := confirmedPO(t, ctx, s, in)

	// Two open lines of the item: a decrease names a supply, not a line, so it is refused as ambiguous.
	amb, _ := applyMessage(t, pool, s, 86, message(po, domain.ActionDecrease, "5", ""))
	if amb.Status != domain.InboundRefused || amb.ReasonCode != domain.CodeInboundAmbiguous {
		t.Fatalf("ambiguous decrease = %+v", amb)
	}
	// A date move is narrowed by the open quantity planning sends.
	mv, _ := applyMessage(t, pool, s, 86, message(po, domain.ActionPostpone, "10", "2027-04-01"))
	if mv.Status != domain.InboundApplied || mv.LineNo != 2 {
		t.Fatalf("postpone of the 10-unit line = %+v", mv)
	}
	cur, _ := s.GetPurchaseOrder(ctx, po.ID)
	if cur.Lines[0].DeliveryDate.String() != "2027-03-10" || cur.Lines[1].DeliveryDate.String() != "2027-04-01" {
		t.Fatalf("dates after narrowed postpone: %s, %s", cur.Lines[0].DeliveryDate, cur.Lines[1].DeliveryDate)
	}
}
