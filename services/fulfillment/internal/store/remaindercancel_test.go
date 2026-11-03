package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
	"github.com/BenjaminSRussell/passage/services/fulfillment/migrations"
)

// consumerCtx is the principal the order consumer runs as: the tenant of the event, no legal entity,
// no all-scope duty (so only the legal-entity scope the store sets for the event admits shipment rows).
func consumerCtx(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: "service:fulfillment-order",
		Duties: map[string]struct{}{"fulfillment.order.sync": {}}})
}

func newStore(t *testing.T, admin, app *pgxpool.Pool) *store.Postgres {
	t.Helper()
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return store.New(app, "0614141", 0)
}

func applyRemainder(t *testing.T, s *store.Postgres, app *pgxpool.Pool, n int, in store.RemainderCancelledIn) store.RemainderCancelledResult {
	t.Helper()
	ctx := consumerCtx(n)
	var res store.RemainderCancelledResult
	err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		var err error
		res, err = s.ApplyOrderRemainderCancelled(ctx, tx, in)
		return err
	})
	if err != nil {
		t.Fatalf("ApplyOrderRemainderCancelled: %v", err)
	}
	return res
}

func keep(lineID, qty string) store.RemainderLineIn {
	return store.RemainderLineIn{OrderLineID: lineID, KeptQty: d(qty)}
}

// Unpicked quantity is cut to the kept quantity and a shipment with nothing left is cancelled:
// shipment.closed (reason order_remainder_cancelled) and shipment.cancelled tell inventory to release,
// an older sales order version changes nothing, and a redelivery changes nothing more.
func TestRemainderCancelled_CutsUnpickedAndCancelsEmptyShipments(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	s := newStore(t, admin, app)
	ctx := ctxFor(81, "")
	f := releasedDefault(t, s, ctx, 10, 5)
	l1, l2 := f.shipment.Lines[0], f.shipment.Lines[1]
	in := store.RemainderCancelledIn{OrderID: f.shipment.OrderID, LegalEntityID: f.legalEntity, Version: 3, EventID: "e1",
		Lines: []store.RemainderLineIn{keep(l1.OrderLineID, "4"), keep(l2.OrderLineID, "0")}}
	res := applyRemainder(t, s, app, 81, in)
	if res.LinesApplied != 2 || res.ShipmentsCut != 1 || res.ShipmentsCancelled != 0 || res.Exceptions != 0 {
		t.Fatalf("result = %+v", res)
	}
	got, err := s.GetShipment(ctx, f.shipment.ID)
	if err != nil || got.Status != domain.StatusReleased || got.Version != f.shipment.Version+1 {
		t.Fatalf("shipment = %+v, %v", got, err)
	}
	if !got.Lines[0].Planned.Equal(d("4")) || !got.Lines[0].Closed.Equal(d("6")) || !got.Lines[1].Planned.IsZero() || !got.Lines[1].Closed.Equal(d("5")) {
		t.Fatalf("lines = %+v", got.Lines)
	}
	if n := countEvents(t, admin, fulevents.ShipmentClosed); n != 1 {
		t.Fatalf("shipment.closed events = %d, want 1", n)
	}
	if got := outboxData(t, admin, fulevents.ShipmentClosed, "{reason}"); got != fulevents.ReasonOrderRemainderCancelled {
		t.Fatalf("reason = %q", got)
	}
	// Redelivery with the same version and an older version change nothing for the lines.
	if res := applyRemainder(t, s, app, 81, in); res.ShipmentsCut != 0 {
		t.Fatalf("redelivery cut again: %+v", res)
	}
	older := in
	older.Version = 2
	older.Lines = []store.RemainderLineIn{keep(l1.OrderLineID, "0")}
	if res := applyRemainder(t, s, app, 81, older); res.LinesApplied != 0 || res.ShipmentsCut != 0 {
		t.Fatalf("older version applied: %+v", res)
	}
	// The kept quantity keeps the line from shipping more than 4.
	if _, err := s.ConfirmPick(ctx, f.shipment.ID, got.Version, []store.PickInput{{ShipmentLineID: l1.ID, Picked: d("5")}}, idem("over")); problemCode(err) != store.CodeOverPick {
		t.Fatalf("pick above the cut plan: %v", err)
	}
	// Sales then gives up the rest: the shipment is cancelled.
	last := store.RemainderCancelledIn{OrderID: f.shipment.OrderID, LegalEntityID: f.legalEntity, Version: 4, EventID: "e2", Lines: []store.RemainderLineIn{keep(l1.OrderLineID, "0")}}
	if res := applyRemainder(t, s, app, 81, last); res.ShipmentsCancelled != 1 {
		t.Fatalf("last = %+v", res)
	}
	if got, _ := s.GetShipment(ctx, f.shipment.ID); got.Status != domain.StatusCancelled {
		t.Fatalf("status = %s, want CANCELLED", got.Status)
	}
	if n := countEvents(t, admin, fulevents.ShipmentCancelled); n != 1 {
		t.Fatalf("shipment.cancelled events = %d", n)
	}
	// Another tenant's consumer sees nothing of it.
	if res := applyRemainder(t, s, app, 82, store.RemainderCancelledIn{OrderID: f.shipment.OrderID, LegalEntityID: f.legalEntity, Version: 9, EventID: "x", Lines: last.Lines}); res.ShipmentsCut != 0 {
		t.Fatalf("other tenant cut: %+v", res)
	}
}

// Picked quantity above the new plan goes back to stock (picked_unpacked) and its unshipped tracking
// entries are dropped; packed quantity cannot be cut, so the post is refused (FUL-0066) and a
// PACKED_BEYOND_KEPT exception names the surplus.
func TestRemainderCancelled_PackedQuantityBlocksThePostAndRaisesAnException(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	s := newStore(t, admin, app)
	ctx := ctxFor(83, "")
	f := releasedDefault(t, s, ctx, 10)
	l1 := f.shipment.Lines[0]
	picked, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, []store.PickInput{{ShipmentLineID: l1.ID, Picked: d("9")}}, idem("pick"))
	if err != nil {
		t.Fatal(err)
	}
	packed, err := s.PackContainer(ctx, f.shipment.ID, picked.Version, "CARTON-C12", []store.PackInput{{ShipmentLineID: l1.ID, Qty: d("6")}}, idem("pack"))
	if err != nil {
		t.Fatal(err)
	}
	in := store.RemainderCancelledIn{OrderID: f.shipment.OrderID, LegalEntityID: f.legalEntity, Version: 2, EventID: "e1", Lines: []store.RemainderLineIn{keep(l1.OrderLineID, "4")}}
	res := applyRemainder(t, s, app, 83, in)
	if res.ShipmentsCut != 1 || res.Exceptions != 1 {
		t.Fatalf("result = %+v", res)
	}
	got, _ := s.GetShipment(ctx, f.shipment.ID)
	// planned 10 -> 6 (the packed quantity stays), picked 9 -> 6; every picked unit is now packed,
	// so the shipment moves on to PACKED as a pack that got there would.
	if got.Status != domain.StatusPacked || !got.Lines[0].Planned.Equal(d("6")) || !got.Lines[0].Picked.Equal(d("6")) || !got.Lines[0].Packed.Equal(d("6")) || !got.Lines[0].Closed.Equal(d("4")) {
		t.Fatalf("shipment = %s line %+v", got.Status, got.Lines[0])
	}
	if got := outboxData(t, admin, fulevents.ShipmentClosed, "{lines,0,picked_unpacked}"); got != "3" {
		t.Fatalf("picked_unpacked = %q, want 3", got)
	}
	if _, err := s.PostPackingSlip(ctx, f.shipment.ID, got.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(got, "1")}, idem("post-6")); problemCode(err) != store.CodeKeptExceeded {
		t.Fatalf("post of 6 = %v, want %s", err, store.CodeKeptExceeded)
	}
	// The supervisor unpacks: the 2 above the kept quantity are cut from the plan and go back to
	// stock, the kept 4 are repacked and post.
	unpacked, err := s.UnpackContainer(ctx, f.shipment.ID, got.Version, packed.Container.ID, idem("unpack"))
	if err != nil || unpacked.Status != domain.StatusPicked || !unpacked.Lines[0].Planned.Equal(d("4")) || !unpacked.Lines[0].Picked.Equal(d("4")) ||
		!unpacked.Lines[0].Packed.IsZero() || !unpacked.Lines[0].Closed.Equal(d("6")) {
		t.Fatalf("unpack = %s %+v, %v", unpacked.Status, unpacked.Lines, err)
	}
	if n := countEvents(t, admin, fulevents.ShipmentClosed); n != 2 {
		t.Fatalf("shipment.closed events = %d, want 2 (event cut and unpack cut)", n)
	}
	repacked, err := s.PackContainer(ctx, f.shipment.ID, unpacked.Version, "CARTON-C12", []store.PackInput{{ShipmentLineID: l1.ID, Qty: d("4")}}, idem("repack"))
	if err != nil || repacked.Shipment.Status != domain.StatusPacked {
		t.Fatalf("repack = %+v, %v", repacked.Shipment, err)
	}
	if _, err := s.PostPackingSlip(ctx, f.shipment.ID, repacked.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(got, "1")}, idem("post-4")); err != nil {
		t.Fatalf("post of the kept 4: %v", err)
	}
	list, err := s.ListExceptions(ctx, store.ExceptionFilter{Status: "OPEN"})
	if err != nil || len(list) != 1 || list[0].Kind != store.ExceptionPackedBeyondKept || list[0].Qty != "2.000000" || list[0].KeptQty != "4.000000" ||
		list[0].ShipmentID == nil || *list[0].ShipmentID != f.shipment.ID || list[0].LegalEntityID != f.legalEntity {
		t.Fatalf("exceptions = %+v, %v", list, err)
	}
	if n := countEvents(t, admin, fulevents.ExceptionRaised); n != 1 {
		t.Fatalf("exception.raised events = %d", n)
	}
	// Resolve it: a stale version is refused, a version match closes it, a second resolve is refused.
	ex := list[0]
	if _, err := s.ResolveException(ctx, ex.ID, 5, "unpacked", idem("res")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale resolve: %v", err)
	}
	key := idem("res-ok")
	done, err := s.ResolveException(ctx, ex.ID, ex.Version, "unpacked and put back", key)
	if err != nil || done.Status != "RESOLVED" || done.Version != 2 || done.ResolvedBy == nil || *done.ResolvedBy != "user-test" || done.ResolutionNote == nil {
		t.Fatalf("resolve = %+v, %v", done, err)
	}
	if replay, err := s.ResolveException(ctx, ex.ID, ex.Version, "unpacked and put back", key); err != nil || replay.Version != 2 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := s.ResolveException(ctx, ex.ID, 2, "again", idem("res-2")); problemCode(err) != store.CodeExceptionResolved {
		t.Fatalf("second resolve: %v", err)
	}
	if n := countEvents(t, admin, fulevents.ExceptionResolved); n != 1 {
		t.Fatalf("exception.resolved events = %d", n)
	}
	if open, _ := s.ListExceptions(ctx, store.ExceptionFilter{Status: "OPEN"}); len(open) != 0 {
		t.Fatalf("open after resolve = %+v", open)
	}
	if _, err := s.GetException(ctxFor(84, ""), ex.ID); problemCode(err) != store.CodeExceptionNotFound {
		t.Fatalf("other tenant get: %v", err)
	}
}

// A fully packed shipment cannot be cut: the post of more than the kept quantity is refused (FUL-0066)
// until the supervisor unpacks, and then the kept quantity can ship.
func TestRemainderCancelled_PostBeyondKeptIsRefused(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	s := newStore(t, admin, app)
	ctx := ctxFor(86, "")
	f := releasedDefault(t, s, ctx, 10)
	l1 := f.shipment.Lines[0]
	packed := pickAndPack(t, s, ctx, f.shipment)
	in := store.RemainderCancelledIn{OrderID: f.shipment.OrderID, LegalEntityID: f.legalEntity, Version: 2, EventID: "e1", Lines: []store.RemainderLineIn{keep(l1.OrderLineID, "4")}}
	if res := applyRemainder(t, s, app, 86, in); res.Exceptions != 1 || res.ShipmentsCut != 0 {
		t.Fatalf("result = %+v (nothing is cut: all 10 are packed)", res)
	}
	got, _ := s.GetShipment(ctx, f.shipment.ID)
	if got.Status != domain.StatusPacked || !got.Lines[0].Planned.Equal(d("10")) {
		t.Fatalf("shipment = %s %+v", got.Status, got.Lines[0])
	}
	if _, err := s.PostPackingSlip(ctx, f.shipment.ID, got.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(got, "1")}, idem("post")); problemCode(err) != store.CodeKeptExceeded {
		t.Fatalf("post = %v, want %s", err, store.CodeKeptExceeded)
	}
	// A line sales never cut back is not checked: another shipment posts freely.
	g := releasedDefault(t, s, ctx, 3)
	p2 := pickAndPack(t, s, ctx, g.shipment)
	if _, err := s.PostPackingSlip(ctx, g.shipment.ID, p2.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(g.shipment, "1")}, idem("post2")); err != nil {
		t.Fatalf("uncut order post: %v", err)
	}
	_ = packed
}

// The race: a slip was posted for 6 before sales kept 4. The unshipped rest is cut, the shipment
// becomes SHIPPED, the posted quantity is not touched and an exception for the 2 that left is raised
// (visible to a user scoped to the legal entity, not to another one). A further post is refused.
func TestRemainderCancelled_SlipAlreadyPostedRaisesAnException(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	s := newStore(t, admin, app)
	ctx := ctxFor(85, "")
	f := releasedDefault(t, s, ctx, 10, 5)
	keepOpen(t, s, ctx, f.warehouse, f.legalEntity)
	slip := shipPart(t, s, ctx, f)
	l1, l2 := f.shipment.Lines[0], f.shipment.Lines[1]
	in := store.RemainderCancelledIn{OrderID: f.shipment.OrderID, LegalEntityID: f.legalEntity, Version: 6, EventID: "race",
		Lines: []store.RemainderLineIn{keep(l1.OrderLineID, "4"), keep(l2.OrderLineID, "5")}}
	res := applyRemainder(t, s, app, 85, in)
	if res.Exceptions != 1 || res.ShipmentsCut != 1 {
		t.Fatalf("result = %+v", res)
	}
	got, _ := s.GetShipment(ctx, f.shipment.ID)
	if got.Status != domain.StatusShipped || !got.Lines[0].Shipped.Equal(d("6")) || !got.Lines[0].Planned.Equal(d("6")) || !got.Lines[0].Closed.Equal(d("4")) {
		t.Fatalf("shipment = %s line %+v", got.Status, got.Lines[0])
	}
	list, err := s.ListExceptions(ctxFor(85, f.legalEntity), store.ExceptionFilter{})
	if err != nil || len(list) != 1 || list[0].Kind != store.ExceptionSlipPostedBeyondKept || list[0].PackingSlipID == nil || *list[0].PackingSlipID != slip.ID ||
		list[0].OrderLineID != l1.OrderLineID || list[0].Qty != "2.000000" {
		t.Fatalf("exceptions = %+v, %v", list, err)
	}
	if other, err := s.ListExceptions(ctxFor(85, "00000000-0000-0000-0000-0000000000aa"), store.ExceptionFilter{}); err != nil || len(other) != 0 {
		t.Fatalf("other legal entity sees %+v, %v", other, err)
	}
	// A second event about the same case under another event id does not raise a second open exception.
	in.EventID = "race-2"
	applyRemainder(t, s, app, 85, in)
	if n := countEvents(t, admin, fulevents.ExceptionRaised); n != 1 {
		t.Fatalf("exception.raised events = %d, want 1", n)
	}
}

// A cut that leaves nothing to pick on a line confirms it, so a shipment whose other lines are
// picked moves on to PICKED; batch entries that still fit what stays picked are kept, and entries
// beyond it are dropped for capture again.
func TestRemainderCancelled_AdvancesPickingAndKeepsFittingTracking(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	s := newStore(t, admin, app)
	ctx := ctxFor(87, "")
	sh := releasedTracked(t, s, ctx, 10, 5)
	l1, l2 := sh.Lines[0], sh.Lines[1]
	picked, err := s.ConfirmPick(ctx, sh.ID, sh.Version, []store.PickInput{{ShipmentLineID: l1.ID, Picked: d("8")}}, idem("pick"))
	if err != nil || picked.Status != domain.StatusPicking {
		t.Fatalf("pick = %s, %v", picked.Status, err)
	}
	if _, err := capture(t, s, ctx, picked, 0, domain.TrackingEntry{BatchNo: "L-1", Qty: d("3")}); err != nil {
		t.Fatal(err)
	}
	in := store.RemainderCancelledIn{OrderID: sh.OrderID, LegalEntityID: sh.LegalEntityID, Version: 2, EventID: "e1",
		Lines: []store.RemainderLineIn{keep(l1.OrderLineID, "6"), keep(l2.OrderLineID, "0")}}
	applyRemainder(t, s, app, 87, in)
	got, _ := s.GetShipment(ctx, sh.ID)
	if got.Status != domain.StatusPicked || !got.Lines[0].Planned.Equal(d("6")) || !got.Lines[0].Picked.Equal(d("6")) || !got.Lines[1].Planned.IsZero() {
		t.Fatalf("shipment = %s %+v", got.Status, got.Lines)
	}
	if len(got.Lines[0].Tracking) != 1 {
		t.Fatalf("tracking that fits was dropped: %+v", got.Lines[0].Tracking)
	}
	in.Version, in.EventID, in.Lines = 3, "e2", []store.RemainderLineIn{keep(l1.OrderLineID, "2")}
	applyRemainder(t, s, app, 87, in)
	got, _ = s.GetShipment(ctx, sh.ID)
	if !got.Lines[0].Picked.Equal(d("2")) || len(got.Lines[0].Tracking) != 0 {
		t.Fatalf("tracking beyond what stays picked kept: picked %s %+v", got.Lines[0].Picked, got.Lines[0].Tracking)
	}
}
