package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func saveTol(t *testing.T, s *store.Postgres, ctx context.Context, product, customer *string, pct string, version int) store.DeliveryTolerance {
	t.Helper()
	tol, err := s.SaveDeliveryTolerance(ctx, store.DeliveryToleranceInput{ProductID: product, CustomerID: customer, OverdeliveryPct: d(pct)}, version, idem("tol"))
	if err != nil {
		t.Fatalf("SaveDeliveryTolerance %s: %v", pct, err)
	}
	return tol
}

func TestDeliveryTolerance_SaveVersionReplayEventAndTenantIsolation(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	product, customer := ids.New(), ids.New()
	in := store.DeliveryToleranceInput{ProductID: &product, CustomerID: &customer, OverdeliveryPct: d("5")}

	if _, err := s.SaveDeliveryTolerance(ctx, in, 1, idem("early")); problemCode(err) != store.CodeSettingVersion {
		t.Fatalf("first save at version 1: %v", err)
	}
	key := idem("first")
	first, err := s.SaveDeliveryTolerance(ctx, in, 0, key)
	if err != nil || first.Version != 1 || !first.OverdeliveryPct.Equal(d("5")) || first.ID == "" {
		t.Fatalf("first save = %+v, %v", first, err)
	}
	if replay, err := s.SaveDeliveryTolerance(ctx, in, 0, key); err != nil || replay.ID != first.ID || replay.Version != 1 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if n := countEvents(t, admin, fulevents.DeliveryToleranceChanged); n != 1 {
		t.Fatalf("events after save and replay = %d, want 1", n)
	}
	if _, err := s.SaveDeliveryTolerance(ctx, in, 0, idem("stale")); problemCode(err) != store.CodeSettingVersion {
		t.Fatalf("stale save: %v", err)
	}
	in.OverdeliveryPct = d("7.5")
	second, err := s.SaveDeliveryTolerance(ctx, in, 1, idem("second"))
	if err != nil || second.ID != first.ID || second.Version != 2 || !second.OverdeliveryPct.Equal(d("7.5")) {
		t.Fatalf("second save = %+v, %v", second, err)
	}
	if got := outboxData(t, admin, fulevents.DeliveryToleranceChanged, "{previous_overdelivery_pct}"); got != "5" {
		t.Fatalf("previous in event = %q, want 5", got)
	}
	if got := outboxData(t, admin, fulevents.DeliveryToleranceChanged, "{overdelivery_pct}"); got != "7.5" {
		t.Fatalf("overdelivery_pct in event = %q, want 7.5", got)
	}
	// Another scope is another row; the list is most specific first.
	general := saveTol(t, s, ctx, nil, nil, "1", 0)
	onProduct := saveTol(t, s, ctx, &product, nil, "2", 0)
	if general.ID == onProduct.ID || general.ID == first.ID {
		t.Fatal("each scope must be its own row")
	}
	list, err := s.ListDeliveryTolerances(ctx, "", "", 10)
	if err != nil || len(list) != 3 || list[0].ID != first.ID || list[2].ID != general.ID {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if list, err := s.ListDeliveryTolerances(ctx, product, customer, 10); err != nil || len(list) != 1 || list[0].ID != first.ID {
		t.Fatalf("filtered list = %+v, %v", list, err)
	}
	// Row-level security: another tenant sees none of it and starts at version 0.
	other := ctxFor(2, "")
	if list, err := s.ListDeliveryTolerances(other, "", "", 10); err != nil || len(list) != 0 {
		t.Fatalf("other tenant list = %+v, %v", list, err)
	}
	if saved := saveTol(t, s, other, &product, &customer, "9", 0); saved.ID == first.ID || saved.Version != 1 {
		t.Fatalf("other tenant save = %+v", saved)
	}
}

// BR-28.7 end to end: a pick above the plan is FUL-0004 with no tolerance,
// FUL-0005 above the tolerance and accepted within it. The over-picked line
// packs, posts the whole picked quantity, completes the shipment (nothing is
// left to close) and pick.confirmed names the quantity above the plan.
func TestOverDeliveryTolerance_PickPackPost(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(4, "")
	f := released(t, s, ctx, 100)
	line := f.shipment.Lines[0]
	pick := func(sh store.Shipment, qty string) (store.Shipment, error) {
		return s.ConfirmPick(ctx, sh.ID, sh.Version, []store.PickInput{{ShipmentLineID: line.ID, Picked: d(qty)}}, idem("pick"))
	}
	if _, err := pick(f.shipment, "103"); problemCode(err) != store.CodeOverPick {
		t.Fatalf("no tolerance: %v, want %s", err, store.CodeOverPick)
	}

	// A general 2% tolerance: 102 is the most.
	saveTol(t, s, ctx, nil, nil, "2", 0)
	if _, err := pick(f.shipment, "102.5"); problemCode(err) != store.CodeOverShip {
		t.Fatalf("above 2%%: %v, want %s", err, store.CodeOverShip)
	}
	// A tolerance for another customer does not apply; the product and customer scope wins over the general one.
	other := ids.New()
	saveTol(t, s, ctx, &line.ProductID, &other, "50", 0)
	saveTol(t, s, ctx, &line.ProductID, &f.shipment.CustomerID, "5", 0)
	if _, err := pick(f.shipment, "105.01"); problemCode(err) != store.CodeOverShip {
		t.Fatalf("above 5%%: %v, want %s", err, store.CodeOverShip)
	}
	picked, err := pick(f.shipment, "105")
	if err != nil {
		t.Fatalf("within 5%% (product and customer scope): %v", err)
	}
	got := picked.Lines[0]
	if !got.Picked.Equal(d("105")) || !got.OverTolerancePct.Equal(d("5")) || !got.Planned.Equal(d("100")) || picked.Status != domain.StatusPicked {
		t.Fatalf("after over-pick: %+v status %s", got, picked.Status)
	}
	if over := outboxData(t, admin, fulevents.PickConfirmed, "{lines,0,over_picked}"); over != "5" {
		t.Fatalf("over_picked in pick.confirmed = %q, want 5", over)
	}
	if back := outboxData(t, admin, fulevents.PickConfirmed, "{lines,0,backordered}"); back != "0" {
		t.Fatalf("backordered = %q, want 0", back)
	}

	// Packing the picked quantity and posting ships all 105 and completes the shipment.
	pr, err := s.PackContainer(ctx, picked.ID, picked.Version, "CARTON-C12", []store.PackInput{{ShipmentLineID: line.ID, Qty: d("105")}}, idem("pack"))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	slip, err := s.PostPackingSlip(ctx, picked.ID, pr.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(picked, "2.00")}, idem("post"))
	if err != nil || !slip.Lines[0].Qty.Equal(d("105")) {
		t.Fatalf("post = %+v, %v", slip, err)
	}
	done, err := s.GetShipment(ctx, picked.ID)
	if err != nil || done.Status != domain.StatusShipped || !done.Lines[0].Shipped.Equal(d("105")) {
		t.Fatalf("after post: %+v, %v", done, err)
	}
	// Nothing is left to close or re-pick on an over-shipped line.
	if _, err := s.CloseRemainder(ctx, done.ID, done.Version, idem("close")); problemCode(err) != store.CodeInvalidMove && problemCode(err) != store.CodeStatus {
		t.Fatalf("close an over-shipped shipment: %v", err)
	}
	// Cancelling the slip puts the whole 105 back.
	if _, err := s.CancelPackingSlip(ctx, slip.ID, idem("cancel")); err != nil {
		t.Fatalf("cancel slip: %v", err)
	}
}

// The database refuses a picked quantity beyond planned plus the tolerance kept
// on the line, whatever the service does.
func TestOverDeliveryTolerance_DatabaseCheck(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(5, "")
	f := released(t, s, ctx, 10)
	line := f.shipment.Lines[0]
	if _, err := admin.Exec(context.Background(), `update shipment_line set qty_picked = 10.5 where id = $1`, line.ID); err == nil {
		t.Fatal("picked above planned with no tolerance must violate the check")
	}
	if _, err := admin.Exec(context.Background(), `update shipment_line set over_tolerance_pct = 5, qty_picked = 10.5 where id = $1`, line.ID); err != nil {
		t.Fatalf("picked within the stored tolerance: %v", err)
	}
	if _, err := admin.Exec(context.Background(), `update shipment_line set qty_picked = 10.51 where id = $1`, line.ID); err == nil {
		t.Fatal("picked above planned plus the tolerance must violate the check")
	}
}

// After sales cancelled the remainder of an order line (order_line_kept), the
// tolerance never takes the line beyond what it keeps: such a pick is refused
// at once (FUL-0066) instead of at post. A line sales never cut back is limited
// by the tolerance only.
func TestOverDeliveryTolerance_KeptQuantityCapsTheOverPick(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	s := newStore(t, admin, app)
	ctx := ctxFor(86, "")
	f := released(t, s, ctx, 10, 10)
	a, b := f.shipment.Lines[0], f.shipment.Lines[1]
	saveTol(t, s, ctx, nil, nil, "20", 0)
	applyRemainder(t, s, app, 86, store.RemainderCancelledIn{OrderID: f.shipment.OrderID, LegalEntityID: f.legalEntity, Version: 2, EventID: "k1",
		Lines: []store.RemainderLineIn{keep(a.OrderLineID, "10")}})
	sh, err := s.GetShipment(ctx, f.shipment.ID)
	if err != nil || !sh.Lines[0].Planned.Equal(d("10")) {
		t.Fatalf("kept 10 of 10 must cut nothing: %+v, %v", sh, err)
	}
	if _, err := s.ConfirmPick(ctx, sh.ID, sh.Version, []store.PickInput{{ShipmentLineID: a.ID, Picked: d("11")}}, idem("over-kept")); problemCode(err) != store.CodeKeptExceeded {
		t.Fatalf("over-pick beyond the kept quantity: %v, want %s", err, store.CodeKeptExceeded)
	}
	picked, err := s.ConfirmPick(ctx, sh.ID, sh.Version, []store.PickInput{{ShipmentLineID: a.ID, Picked: d("10")}, {ShipmentLineID: b.ID, Picked: d("12")}}, idem("ok"))
	if err != nil || !picked.Lines[1].Picked.Equal(d("12")) {
		t.Fatalf("kept line at plan and an uncut line within tolerance: %+v, %v", picked, err)
	}
}

// A remainder cut that leaves every line shipped completes the shipment even
// when a line shipped above its plan within the tolerance.
func TestOverDeliveryTolerance_RemainderCutCompletesAnOverShippedShipment(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	s := newStore(t, admin, app)
	ctx := ctxFor(87, "")
	f := released(t, s, ctx, 10, 10)
	a, b := f.shipment.Lines[0], f.shipment.Lines[1]
	saveTol(t, s, ctx, nil, nil, "20", 0)
	picked, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, []store.PickInput{{ShipmentLineID: a.ID, Picked: d("12")}, {ShipmentLineID: b.ID, Picked: d("5")}}, idem("pick"))
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	pr, err := s.PackContainer(ctx, picked.ID, picked.Version, "CARTON-C12", []store.PackInput{{ShipmentLineID: a.ID, Qty: d("12")}, {ShipmentLineID: b.ID, Qty: d("5")}}, idem("pack"))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if _, err := s.PostPackingSlip(ctx, picked.ID, pr.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(picked, "2.00")}, idem("post")); err != nil {
		t.Fatalf("post: %v", err)
	}
	if got, _ := s.GetShipment(ctx, picked.ID); got.Status != domain.StatusPartiallyShipped {
		t.Fatalf("after post with a short line: %s", got.Status)
	}
	applyRemainder(t, s, app, 87, store.RemainderCancelledIn{OrderID: f.shipment.OrderID, LegalEntityID: f.legalEntity, Version: 2, EventID: "c1",
		Lines: []store.RemainderLineIn{keep(b.OrderLineID, "5")}})
	got, err := s.GetShipment(ctx, picked.ID)
	if err != nil || got.Status != domain.StatusShipped || !got.Lines[1].Planned.Equal(d("5")) || !got.Lines[0].Shipped.Equal(d("12")) {
		t.Fatalf("after the cut: %+v, %v", got, err)
	}
}
