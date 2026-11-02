package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// shipPart picks 6 of 10 and all 5 of the second line, packs and posts them:
// the shipment is PARTIALLY_SHIPPED with 4 of line 1 owed.
func shipPart(t *testing.T, s *store.Postgres, ctx context.Context, f fixture) store.PackingSlip {
	t.Helper()
	l1, l2 := f.shipment.Lines[0], f.shipment.Lines[1]
	picked, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, []store.PickInput{{ShipmentLineID: l1.ID, Picked: d("6")}, {ShipmentLineID: l2.ID, Picked: d("5")}}, idem("p1"))
	if err != nil {
		t.Fatal(err)
	}
	packed, err := s.PackContainer(ctx, f.shipment.ID, picked.Version, "BOX", []store.PackInput{{ShipmentLineID: l1.ID, Qty: d("6")}, {ShipmentLineID: l2.ID, Qty: d("5")}}, idem("k1"))
	if err != nil {
		t.Fatal(err)
	}
	slip, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "1")}, idem("s1"))
	if err != nil {
		t.Fatal(err)
	}
	return slip
}

func TestWarehouseSetting_DefaultSaveVersionAndIsolation(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := releasedDefault(t, s, ctx, 10)

	list, err := s.ListWarehouseSettings(ctx)
	if err != nil || len(list) != 1 || list[0].WarehouseID != f.warehouse || list[0].LegalEntityID == nil || *list[0].LegalEntityID != f.legalEntity || list[0].ShortPickAction != domain.DefaultShortPickAction || list[0].Version != 0 {
		t.Fatalf("list = %+v, %v; want the shipment's warehouse at REMAINDER_BACKORDER version 0", list, err)
	}
	if other, err := s.ListWarehouseSettings(ctxFor(2, "")); err != nil || len(other) != 0 {
		t.Fatalf("other tenant list = %+v, %v", other, err)
	}
	if _, err := s.SaveWarehouseSetting(ctx, f.warehouse, f.legalEntity, 1, domain.ActionBackorder, idem("early")); problemCode(err) != store.CodeSettingVersion {
		t.Fatalf("save at version 1 before any: %v", err)
	}
	key := idem("save")
	saved, err := s.SaveWarehouseSetting(ctx, f.warehouse, f.legalEntity, 0, domain.ActionBackorder, key)
	if err != nil || saved.Version != 1 || saved.ShortPickAction != domain.ActionBackorder || saved.UpdatedAt == nil {
		t.Fatalf("save = %+v, %v", saved, err)
	}
	if replay, err := s.SaveWarehouseSetting(ctx, f.warehouse, f.legalEntity, 0, domain.ActionBackorder, key); err != nil || replay.Version != 1 {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if n := countEvents(t, admin, fulevents.WarehouseSettingChanged); n != 1 {
		t.Fatalf("warehousesetting.changed events = %d, want 1 (replay writes none)", n)
	}
	if _, err := s.SaveWarehouseSetting(ctx, f.warehouse, f.legalEntity, 0, domain.ActionCancelRemainder, idem("stale")); problemCode(err) != store.CodeSettingVersion {
		t.Fatalf("stale save: %v", err)
	}
	next, err := s.SaveWarehouseSetting(ctx, f.warehouse, f.legalEntity, 1, domain.ActionCancelRemainder, idem("next"))
	if err != nil || next.Version != 2 {
		t.Fatalf("second save = %+v, %v", next, err)
	}
	if got := outboxData(t, admin, fulevents.WarehouseSettingChanged, "{previous_short_pick_action}"); got != "REMAINDER_BACKORDER" {
		t.Fatalf("previous action in event = %q, want REMAINDER_BACKORDER", got)
	}
	if got, err := s.GetWarehouseSetting(ctx, f.warehouse); err != nil || got.ShortPickAction != domain.ActionCancelRemainder || got.Version != 2 {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if got, err := s.GetWarehouseSetting(ctxFor(2, ""), f.warehouse); err != nil || got.Version != 0 || got.ShortPickAction != domain.DefaultShortPickAction {
		t.Fatalf("other tenant get = %+v, %v; want the default", got, err)
	}
	// The other tenant saves its own row for the same warehouse id.
	if got, err := s.SaveWarehouseSetting(ctxFor(2, ""), f.warehouse, f.legalEntity, 0, domain.ActionBackorder, idem("t2")); err != nil || got.Version != 1 {
		t.Fatalf("other tenant save = %+v, %v", got, err)
	}
	if got, _ := s.GetWarehouseSetting(ctx, f.warehouse); got.ShortPickAction != domain.ActionCancelRemainder {
		t.Fatalf("tenant 1 setting changed by tenant 2: %+v", got)
	}
}

// A warehouse belongs to one legal entity (org): its setting carries it, is
// scoped by it, and cannot be saved under another one.
func TestWarehouseSetting_LegalEntityScope(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	f := releasedDefault(t, s, ctx, 10, 5)
	other := ids.New()

	if _, err := s.SaveWarehouseSetting(ctx, f.warehouse, other, 0, domain.ActionCancelRemainder, idem("wrong")); problemCode(err) != store.CodeWarehouseLegalEntity {
		t.Fatalf("save under a legal entity the warehouse's shipments do not name: %v, want %s", err, store.CodeWarehouseLegalEntity)
	}
	if got, err := s.GetWarehouseSetting(ctx, f.warehouse); err != nil || got.Version != 0 || got.LegalEntityID == nil || *got.LegalEntityID != f.legalEntity {
		t.Fatalf("default read = %+v, %v; want the shipments' legal entity", got, err)
	}
	if _, err := s.SaveWarehouseSetting(ctx, f.warehouse, f.legalEntity, 0, domain.ActionBackorder, idem("ok")); err != nil {
		t.Fatal(err)
	}

	// A principal scoped to another legal entity sees neither the warehouse nor
	// its setting, and cannot overwrite it.
	scoped := ctxFor(1, other)
	if list, err := s.ListWarehouseSettings(scoped); err != nil || len(list) != 0 {
		t.Fatalf("other legal entity list = %+v, %v", list, err)
	}
	if got, err := s.GetWarehouseSetting(scoped, f.warehouse); err != nil || got.Version != 0 || got.LegalEntityID != nil || got.ShortPickAction != domain.DefaultShortPickAction {
		t.Fatalf("other legal entity get = %+v, %v", got, err)
	}
	if _, err := s.SaveWarehouseSetting(scoped, f.warehouse, other, 0, domain.ActionKeepOpen, idem("sneak")); problemCode(err) != store.CodeWarehouseLegalEntity {
		t.Fatalf("other legal entity overwrite: %v, want %s", err, store.CodeWarehouseLegalEntity)
	}
	own := ctxFor(1, f.legalEntity)
	if got, err := s.GetWarehouseSetting(own, f.warehouse); err != nil || got.ShortPickAction != domain.ActionBackorder || got.Version != 1 {
		t.Fatalf("own legal entity get = %+v, %v", got, err)
	}

	// A warehouse without shipments keeps the legal entity of its first save.
	bare, le := ids.New(), ids.New()
	if _, err := s.SaveWarehouseSetting(ctx, bare, le, 0, domain.ActionCancelRemainder, idem("bare")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveWarehouseSetting(ctx, bare, other, 1, domain.ActionKeepOpen, idem("move")); problemCode(err) != store.CodeWarehouseLegalEntity {
		t.Fatalf("moving the setting to another legal entity: %v", err)
	}
	if list, _ := s.ListWarehouseSettings(ctxFor(1, le)); len(list) != 1 || list[0].WarehouseID != bare || *list[0].LegalEntityID != le {
		t.Fatalf("list scoped to the bare warehouse's legal entity = %+v", list)
	}
}

// FR-28.8 backorder shipment: the remainder closes on the original and moves to
// a new RELEASED shipment that ships through the normal flow.
func TestBackorderRemainder_MovesTheRemainderToANewShipment(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 10, 5)
	l1 := f.shipment.Lines[0]

	if _, err := s.BackorderRemainder(ctx, f.shipment.ID, f.shipment.Version, nil, idem("early")); problemCode(err) != store.CodeInvalidMove {
		t.Fatalf("backorder before shipping: %v, want %s", err, store.CodeInvalidMove)
	}
	shipPart(t, s, ctx, f)
	partial := getShipment(t, s, ctx, f.shipment.ID)
	if partial.Status != domain.StatusPartiallyShipped {
		t.Fatalf("status = %s, want PARTIALLY_SHIPPED", partial.Status)
	}
	if _, err := s.BackorderRemainder(ctx, f.shipment.ID, partial.Version+3, nil, idem("stale")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale: %v", err)
	}
	if _, err := s.BackorderRemainder(ctxFor(2, ""), f.shipment.ID, partial.Version, nil, idem("t2")); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("other tenant: %v", err)
	}
	if _, err := s.BackorderRemainder(ctxFor(1, ids.New()), f.shipment.ID, partial.Version, nil, idem("le")); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("other legal entity: %v", err)
	}
	if n := countEvents(t, admin, fulevents.ShipmentClosed); n != 0 {
		t.Fatalf("refused backorders wrote %d shipment.closed events", n)
	}

	date := today().AddDate(0, 0, 5)
	key := idem("backorder")
	res, err := s.BackorderRemainder(ctx, f.shipment.ID, partial.Version, &date, key)
	if err != nil {
		t.Fatal(err)
	}
	if res.Shipment.Status != domain.StatusShipped || res.Shipment.BackorderID == nil || *res.Shipment.BackorderID != res.Backorder.ID {
		t.Fatalf("original = %+v", res.Shipment)
	}
	b := res.Backorder
	if b.Status != domain.StatusReleased || b.BackorderOfID == nil || *b.BackorderOfID != f.shipment.ID || b.ShipmentNumber == f.shipment.ShipmentNumber ||
		b.OrderID != f.shipment.OrderID || b.WarehouseID != f.warehouse || b.CustomerID != f.shipment.CustomerID || !b.PlannedShipDate.Equal(date) {
		t.Fatalf("backorder shipment = %+v", b)
	}
	if len(b.Lines) != 1 || b.Lines[0].OrderLineID != l1.OrderLineID || b.Lines[0].ProductID != l1.ProductID || !b.Lines[0].Planned.Equal(d("4")) {
		t.Fatalf("backorder lines = %+v, want line 1 for 4", b.Lines)
	}
	if replay, err := s.BackorderRemainder(ctx, f.shipment.ID, partial.Version, &date, key); err != nil || replay.Backorder.ID != b.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	got := getShipment(t, s, ctx, f.shipment.ID)
	if got.Status != domain.StatusShipped || !got.Lines[0].Planned.Equal(d("6")) || !got.Lines[0].Closed.Equal(d("4")) || got.BackorderID == nil || *got.BackorderID != b.ID {
		t.Fatalf("original after = %+v", got)
	}
	if again := getShipment(t, s, ctx, b.ID); again.BackorderOfID == nil || *again.BackorderOfID != f.shipment.ID || again.BackorderID != nil {
		t.Fatalf("backorder reread = %+v", again)
	}
	if n := countEvents(t, admin, fulevents.ShipmentClosed); n != 1 {
		t.Fatalf("shipment.closed events = %d, want 1", n)
	}
	if id := outboxData(t, admin, fulevents.ShipmentClosed, "{backorder_shipment_id}"); id != b.ID {
		t.Fatalf("closed event backorder_shipment_id = %q, want %s", id, b.ID)
	}
	if n := countEvents(t, admin, fulevents.ShipmentReleased); n != 2 {
		t.Fatalf("shipment.released events = %d, want 2 (original and backorder)", n)
	}
	if of := outboxData(t, admin, fulevents.ShipmentReleased, "{backorder_of}"); of != f.shipment.ID {
		t.Fatalf("backorder shipment.released backorder_of = %q, want %s", of, f.shipment.ID)
	}
	// A second backorder is refused: no remainder is left on the original.
	if _, err := s.BackorderRemainder(ctx, f.shipment.ID, got.Version, nil, idem("twice")); problemCode(err) != store.CodeInvalidMove {
		t.Fatalf("second backorder: %v, want %s", err, store.CodeInvalidMove)
	}
	// The backorder ships through the normal flow; the order's line 1 has now shipped 6 + 4.
	packed := pickAndPack(t, s, ctx, getShipment(t, s, ctx, b.ID))
	if _, err := s.PostPackingSlip(ctx, b.ID, packed.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(packed.Shipment, "1")}, idem("s2")); err != nil {
		t.Fatal(err)
	}
	if done := getShipment(t, s, ctx, b.ID); done.Status != domain.StatusShipped {
		t.Fatalf("backorder status = %s, want SHIPPED", done.Status)
	}
}

// The warehouse's short pick action runs when a post leaves a remainder open.
func TestPostPackingSlip_AppliesTheShortPickAction(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")

	keep := released(t, s, ctx, 10, 5)
	if slip := shipPart(t, s, ctx, keep); slip.RemainderAction != "" || slip.BackorderShipmentID != "" {
		t.Fatalf("KEEP_OPEN slip = %+v", slip)
	}
	if sh := getShipment(t, s, ctx, keep.shipment.ID); sh.Status != domain.StatusPartiallyShipped {
		t.Fatalf("KEEP_OPEN status = %s, want PARTIALLY_SHIPPED", sh.Status)
	}

	cancel := releasedDefault(t, s, ctx, 10, 5)
	if _, err := s.SaveWarehouseSetting(ctx, cancel.warehouse, cancel.legalEntity, 0, domain.ActionCancelRemainder, idem("c")); err != nil {
		t.Fatal(err)
	}
	slip := shipPart(t, s, ctx, cancel)
	if slip.RemainderAction != "CANCEL_REMAINDER" || slip.BackorderShipmentID != "" {
		t.Fatalf("CANCEL_REMAINDER slip = %+v", slip)
	}
	sh := getShipment(t, s, ctx, cancel.shipment.ID)
	if sh.Status != domain.StatusShipped || !sh.Lines[0].Closed.Equal(d("4")) || sh.BackorderID != nil {
		t.Fatalf("CANCEL_REMAINDER shipment = %+v", sh)
	}

	back := releasedDefault(t, s, ctx, 10, 5)
	if _, err := s.SaveWarehouseSetting(ctx, back.warehouse, back.legalEntity, 0, domain.ActionBackorder, idem("b")); err != nil {
		t.Fatal(err)
	}
	slip = shipPart(t, s, ctx, back)
	if slip.RemainderAction != "REMAINDER_BACKORDER" || slip.BackorderShipmentID == "" {
		t.Fatalf("REMAINDER_BACKORDER slip = %+v", slip)
	}
	sh = getShipment(t, s, ctx, back.shipment.ID)
	nb := getShipment(t, s, ctx, slip.BackorderShipmentID)
	if sh.Status != domain.StatusShipped || sh.BackorderID == nil || *sh.BackorderID != nb.ID || nb.Status != domain.StatusReleased ||
		len(nb.Lines) != 1 || !nb.Lines[0].Planned.Equal(d("4")) {
		t.Fatalf("REMAINDER_BACKORDER original %+v backorder %+v", sh, nb)
	}

	// A fully shipped post triggers nothing, whatever the setting.
	full := releasedDefault(t, s, ctx, 3)
	if _, err := s.SaveWarehouseSetting(ctx, full.warehouse, full.legalEntity, 0, domain.ActionBackorder, idem("f")); err != nil {
		t.Fatal(err)
	}
	packedFull := pickAndPack(t, s, ctx, full.shipment)
	slip, err := s.PostPackingSlip(ctx, full.shipment.ID, packedFull.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(full.shipment, "1")}, idem("s9"))
	if err != nil || slip.RemainderAction != "" {
		t.Fatalf("full post = %+v, %v", slip, err)
	}
	if n := countEvents(t, admin, fulevents.ShipmentClosed); n != 2 {
		t.Fatalf("shipment.closed events = %d, want 2 (cancel and backorder)", n)
	}
}

// A warehouse with no saved setting backorders the remainder (the PRD default):
// the post names REMAINDER_BACKORDER and the quantity moves to a new shipment.
func TestPostPackingSlip_UnsavedWarehouseBackordersTheRemainder(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	f := releasedDefault(t, s, ctx, 10, 5)
	if got, err := s.GetWarehouseSetting(ctx, f.warehouse); err != nil || got.Version != 0 || got.ShortPickAction != domain.ActionBackorder {
		t.Fatalf("unsaved setting = %+v, %v", got, err)
	}
	slip := shipPart(t, s, ctx, f)
	if slip.RemainderAction != "REMAINDER_BACKORDER" || slip.BackorderShipmentID == "" {
		t.Fatalf("slip = %+v, want the remainder backordered", slip)
	}
	sh := getShipment(t, s, ctx, f.shipment.ID)
	nb := getShipment(t, s, ctx, slip.BackorderShipmentID)
	if sh.Status != domain.StatusShipped || nb.Status != domain.StatusReleased || nb.BackorderOfID == nil || *nb.BackorderOfID != sh.ID {
		t.Fatalf("original %+v backorder %+v", sh, nb)
	}
}

// The default applies to every shipment of an unsaved warehouse, also when its
// other shipments belong to another legal entity; a setting saved for one legal
// entity applies to its shipments only and leaves the others' remainder open.
func TestPostPackingSlip_DefaultAppliesWhateverTheOtherShipmentsLegalEntity(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	first := releasedDefault(t, s, ctx, 3)
	second := releasedIn(t, s, ctx, first.warehouse, ids.New(), 10, 5)
	if slip := shipPart(t, s, ctx, second); slip.RemainderAction != "REMAINDER_BACKORDER" || slip.BackorderShipmentID == "" {
		t.Fatalf("unsaved warehouse, second legal entity: slip = %+v, want the remainder backordered", slip)
	}
	saved := releasedDefault(t, s, ctx, 3)
	keepOpen(t, s, ctx, saved.warehouse, saved.legalEntity)
	foreign := releasedIn(t, s, ctx, saved.warehouse, ids.New(), 10, 5)
	if slip := shipPart(t, s, ctx, foreign); slip.RemainderAction != "" {
		t.Fatalf("setting saved for another legal entity: slip = %+v, want no action", slip)
	}
	own := releasedIn(t, s, ctx, saved.warehouse, saved.legalEntity, 10, 5)
	if slip := shipPart(t, s, ctx, own); slip.RemainderAction != "" || getShipment(t, s, ctx, own.shipment.ID).Status != domain.StatusPartiallyShipped {
		t.Fatalf("saved KEEP_OPEN: slip = %+v, want the remainder kept open", slip)
	}
}
