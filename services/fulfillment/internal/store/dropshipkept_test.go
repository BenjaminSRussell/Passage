package store_test

import (
	"context"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func dropShipRemainder(ds store.DropShip, version int, kept string) store.RemainderCancelledIn {
	return store.RemainderCancelledIn{OrderID: ds.OrderID, LegalEntityID: ds.LegalEntityID, Version: version, EventID: ids.New(),
		Lines: []store.RemainderLineIn{keep(ds.OrderLineID, kept)}}
}

// Sales cancels the remainder of an order with a drop-shipped line: the open quantity above the kept
// quantity is closed (no booking), a drop-ship with nothing shipped and nothing kept is cancelled,
// both events carry reason order_remainder_cancelled, and a redelivery changes nothing.
func TestRemainderCancelled_DropShipFollowsTheKeptQuantity(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	s := newStore(t, admin, app)
	ctx := ctxFor(91, "")
	le := ids.New()

	// Nothing shipped, line keeps 4 of 10: the drop-ship stays OPEN with 6 closed.
	a, err := s.CreateDropShip(ctx, dropShipInput(le, "10", "2.5"), idem("a"))
	if err != nil {
		t.Fatal(err)
	}
	in := dropShipRemainder(a, 2, "4")
	if res := applyRemainder(t, s, app, 91, in); res.DropShipsCut != 1 || res.DropShipsCancelled != 0 || res.Exceptions != 0 {
		t.Fatalf("result = %+v", res)
	}
	got, err := s.GetDropShip(ctx, a.ID)
	if err != nil || got.Status != domain.DropShipOpen || !got.QtyClosed.Equal(dec("6")) || !got.QtyOpen.Equal(dec("4")) || got.Version != a.Version+1 {
		t.Fatalf("drop-ship = %+v, %v", got, err)
	}
	if v := outboxData(t, admin, fulevents.DropShipClosed, "{reason}"); v != fulevents.ReasonOrderRemainderCancelled {
		t.Fatalf("dropship.closed reason = %q", v)
	}
	if res := applyRemainder(t, s, app, 91, in); res.DropShipsCut != 0 {
		t.Fatalf("redelivery cut again: %+v", res)
	}
	if n := countEvents(t, admin, fulevents.DropShipClosed); n != 1 {
		t.Fatalf("dropship.closed events = %d, want 1", n)
	}
	// The vendor may confirm what is kept; the line still has its active drop-ship, so a second one is refused.
	if _, err := s.ConfirmDropShip(ctx, a.ID, got.Version, store.DropShipConfirmInput{Qty: ptr(dec("4")), ShippedOn: today()}, idem("ca")); err != nil {
		t.Fatalf("confirm of the kept quantity: %v", err)
	}
	late := dropShipInput(le, "1", "2.5")
	late.OrderID, late.OrderLineID = a.OrderID, a.OrderLineID
	if _, err := s.CreateDropShip(ctx, late, idem("late")); problemCode(err) != store.CodeDropShipExists {
		t.Fatalf("a second drop-ship of the line: %v, want %s", err, store.CodeDropShipExists)
	}

	// Nothing shipped and nothing kept: cancelled.
	b, err := s.CreateDropShip(ctx, dropShipInput(le, "3", "1"), idem("b"))
	if err != nil {
		t.Fatal(err)
	}
	if res := applyRemainder(t, s, app, 91, dropShipRemainder(b, 2, "0")); res.DropShipsCancelled != 1 {
		t.Fatalf("result = %+v", res)
	}
	if got, _ := s.GetDropShip(ctx, b.ID); got.Status != domain.DropShipCancelled {
		t.Fatalf("status = %s, want CANCELLED", got.Status)
	}
	if v := outboxData(t, admin, fulevents.DropShipCancelled, "{reason}"); v != fulevents.ReasonOrderRemainderCancelled {
		t.Fatalf("dropship.cancelled reason = %q", v)
	}
	// After the cancel, a new drop-ship above what the line keeps is refused (FUL-0066).
	again := dropShipInput(le, "2", "1")
	again.OrderID, again.OrderLineID = b.OrderID, b.OrderLineID
	if _, err := s.CreateDropShip(ctx, again, idem("again")); problemCode(err) != store.CodeKeptExceeded {
		t.Fatalf("create above the kept quantity: %v", err)
	}
	// A line that keeps 3: a drop-ship of 3 is accepted.
	c, err := s.CreateDropShip(ctx, dropShipInput(le, "8", "1"), idem("c"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelDropShip(ctx, c.ID, c.Version, idem("cc")); err != nil {
		t.Fatal(err)
	}
	applyRemainder(t, s, app, 91, dropShipRemainder(c, 2, "3"))
	up := dropShipInput(le, "3", "1")
	up.OrderID, up.OrderLineID = c.OrderID, c.OrderLineID
	if _, err := s.CreateDropShip(ctx, up, idem("up")); err != nil {
		t.Fatalf("create of the kept quantity: %v", err)
	}
}

// A drop-ship created while the cancellation was being applied is not seen by the consumer and
// keeps its full quantity; the vendor confirmation is then held to the kept quantity (FUL-0066)
// and the drop-ship is left untouched by the refusal.
func TestDropShip_ConfirmAboveKeptIsRefused(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	s := newStore(t, admin, app)
	ctx := ctxFor(95, "")
	le := ids.New()
	ds, err := s.CreateDropShip(ctx, dropShipInput(le, "10", "2"), idem("ds"))
	if err != nil {
		t.Fatal(err)
	}
	// The consumer's row, written as if it committed after the drop-ship was created.
	if _, err := admin.Exec(context.Background(), `insert into order_line_kept (tenant_id, order_line_id, order_id, legal_entity_id, kept_qty, order_version, event_id)
		values ($1, $2, $3, $4, 4, 2, 'race')`, testkit.TenantID(95), ds.OrderLineID, ds.OrderID, le); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmDropShip(ctx, ds.ID, ds.Version, store.DropShipConfirmInput{Qty: ptr(dec("3")), ShippedOn: today()}, idem("c1")); err != nil {
		t.Fatalf("confirm within the kept quantity: %v", err)
	}
	got, _ := s.GetDropShip(ctx, ds.ID)
	if _, err := s.ConfirmDropShip(ctx, ds.ID, got.Version, store.DropShipConfirmInput{Qty: ptr(dec("2")), ShippedOn: today()}, idem("c2")); problemCode(err) != store.CodeKeptExceeded {
		t.Fatalf("confirm taking the total to 5 of a kept 4: %v, want %s", err, store.CodeKeptExceeded)
	}
	after, _ := s.GetDropShip(ctx, ds.ID)
	if after.Version != got.Version || !after.QtyConfirmed.Equal(dec("3")) || len(after.Confirmations) != 1 {
		t.Fatalf("refused confirmation changed the drop-ship: %+v", after)
	}
}

// A vendor shipment already booked above the kept quantity raises a supervisor exception and
// reverses nothing; a partly shipped drop-ship closes what the vendor has not shipped.
func TestRemainderCancelled_DropShipConfirmedBeyondKeptAndPartial(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	s := newStore(t, admin, app)
	ctx := ctxFor(92, "")
	le := ids.New()

	over, err := s.CreateDropShip(ctx, dropShipInput(le, "10", "2"), idem("o"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmDropShip(ctx, over.ID, over.Version, store.DropShipConfirmInput{Qty: ptr(dec("7")), ShippedOn: today()}, idem("co")); err != nil {
		t.Fatal(err)
	}
	res := applyRemainder(t, s, app, 92, dropShipRemainder(over, 2, "5"))
	if res.Exceptions != 1 || res.DropShipsCut != 1 {
		t.Fatalf("result = %+v", res)
	}
	got, _ := s.GetDropShip(ctx, over.ID)
	if got.Status != domain.DropShipConfirmed || !got.QtyConfirmed.Equal(dec("7")) || !got.QtyClosed.Equal(dec("3")) || len(got.Journal) != 2 {
		t.Fatalf("drop-ship = %+v", got)
	}
	exs, err := s.ListExceptions(ctx, store.ExceptionFilter{Status: "OPEN"})
	if err != nil || len(exs) != 1 || exs[0].Kind != store.ExceptionDropShipBeyondKept || exs[0].Qty != "2.000000" || exs[0].KeptQty != "5.000000" {
		t.Fatalf("exceptions = %+v, %v", exs, err)
	}

	part, err := s.CreateDropShip(ctx, dropShipInput(le, "10", "2"), idem("p"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmDropShip(ctx, part.ID, part.Version, store.DropShipConfirmInput{Qty: ptr(dec("2")), ShippedOn: today()}, idem("cp")); err != nil {
		t.Fatal(err)
	}
	if res := applyRemainder(t, s, app, 92, dropShipRemainder(part, 2, "6")); res.DropShipsCut != 1 || res.Exceptions != 0 {
		t.Fatalf("result = %+v", res)
	}
	got, _ = s.GetDropShip(ctx, part.ID)
	if got.Status != domain.DropShipPartial || !got.QtyOpen.Equal(dec("4")) || !got.QtyClosed.Equal(dec("4")) {
		t.Fatalf("drop-ship = %+v", got)
	}
	// Another tenant's event does not touch it.
	if res := applyRemainder(t, s, app, 93, dropShipRemainder(part, 9, "0")); res.DropShipsCancelled+res.DropShipsCut != 0 {
		t.Fatalf("other tenant: %+v", res)
	}
}

// Release reads order_line_kept: a line sales cut back is offered only for what it keeps beyond the
// live shipments (and is then not partial, so a repeat run skips it as already released), and with no
// live shipment and nothing kept it is skipped as order_remainder_cancelled.
func TestRelease_ReadsTheKeptQuantity(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	s := newStore(t, admin, app)
	ctx := ctxFor(94, "")
	f := releasedDefault(t, s, ctx, 10)
	line := f.shipment.Lines[0]
	// The shipment is cancelled by hand, then sales keeps 4 of the 10.
	if _, err := s.CancelShipment(ctx, f.shipment.ID, f.shipment.Version, idem("cx")); err != nil {
		t.Fatal(err)
	}
	applyRemainder(t, s, app, 94, store.RemainderCancelledIn{OrderID: f.shipment.OrderID, LegalEntityID: f.legalEntity, Version: 2, EventID: "k1",
		Lines: []store.RemainderLineIn{keep(line.OrderLineID, "4")}})
	offer := func() []domain.ReleaseGroup {
		g, skipped := domain.Release([]domain.ReleaseLine{{OrderID: f.shipment.OrderID, OrderLineID: line.OrderLineID, CustomerID: ids.New(), LegalEntityID: f.legalEntity,
			WarehouseID: f.warehouse, ProductID: ids.New(), Unit: "EA", Qty: decimal.NewFromInt(10), ConfirmedShipDate: today(), ATPOK: true}},
			domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
		if len(skipped) != 0 {
			t.Fatalf("skipped %v", skipped)
		}
		return g
	}
	first, err := s.RunRelease(ctx, f.warehouse, offer(), idem("r1"))
	if err != nil || len(first.Shipments) != 1 || !first.Shipments[0].Lines[0].Planned.Equal(dec("4")) {
		t.Fatalf("first run = %+v, %v; want 4 of 10", first, err)
	}
	// Released for all it keeps: not partial (nothing is left for a later run).
	if first.Shipments[0].Lines[0].Partial {
		t.Fatalf("a line capped at its kept quantity is flagged partial")
	}
	second, err := s.RunRelease(ctx, f.warehouse, offer(), idem("r2"))
	if err != nil || len(second.Shipments) != 0 || len(second.Skipped) != 1 || second.Skipped[0].Reason != domain.SkipAlreadyReleased {
		t.Fatalf("second run = %+v, %v", second, err)
	}
	// A line with a kept row and no shipment left on it is skipped with the new reason.
	if _, err := s.CancelShipment(ctx, first.Shipments[0].ID, first.Shipments[0].Version, idem("cx2")); err != nil {
		t.Fatal(err)
	}
	applyRemainder(t, s, app, 94, store.RemainderCancelledIn{OrderID: f.shipment.OrderID, LegalEntityID: f.legalEntity, Version: 3, EventID: "k2",
		Lines: []store.RemainderLineIn{keep(line.OrderLineID, "0")}})
	third, err := s.RunRelease(ctx, f.warehouse, offer(), idem("r3"))
	if err != nil || len(third.Shipments) != 0 || len(third.Skipped) != 1 || third.Skipped[0].Reason != domain.SkipOrderRemainderCancelled {
		t.Fatalf("third run = %+v, %v", third, err)
	}
}

func ptr(d decimal.Decimal) *decimal.Decimal { return &d }
