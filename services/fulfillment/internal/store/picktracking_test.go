package store_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func dq(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// releasedTracked releases one order whose lines are batch or serial tracked.
func releasedTracked(t *testing.T, s *store.Postgres, ctx context.Context, qtys ...int64) store.Shipment {
	t.Helper()
	order, customer, le, wh := ids.New(), ids.New(), ids.New(), ids.New()
	var lines []domain.ReleaseLine
	for _, q := range qtys {
		lines = append(lines, domain.ReleaseLine{OrderID: order, OrderLineID: ids.New(), CustomerID: customer, LegalEntityID: le,
			WarehouseID: wh, ProductID: ids.New(), Unit: "EA", Qty: decimal.NewFromInt(q), ConfirmedShipDate: today(), ATPOK: true, TrackingRequired: true})
	}
	groups, _ := domain.Release(lines, domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
	res, err := s.RunRelease(ctx, wh, groups, idem("rel"))
	if err != nil || len(res.Shipments) != 1 {
		t.Fatalf("RunRelease: %+v, %v", res, err)
	}
	if !res.Shipments[0].Lines[0].TrackingRequired {
		t.Fatal("trackingRequired was not stored")
	}
	return res.Shipments[0]
}

func capture(t *testing.T, s *store.Postgres, ctx context.Context, sh store.Shipment, line int, entries ...domain.TrackingEntry) (store.Shipment, error) {
	t.Helper()
	return s.CaptureTracking(ctx, sh.ID, sh.Lines[line].ID, sh.Version, entries, idem("cap"))
}

// BR-28.10: a tracked line cannot post without batch or serial entries; with
// them the slip posts, the entries are stamped with the slip and the posted
// event carries them; cancelling the slip puts them back on the pick.
func TestTracking_RequiredAtPostAndCarriedInTheEvent(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	sh := releasedTracked(t, s, ctx, 10)
	if _, err := capture(t, s, ctx, sh, 0, domain.TrackingEntry{BatchNo: "L-1", Qty: dq("4")}); problemCode(err) != store.CodeStatus {
		t.Fatalf("capture before any pick: %v, want FUL-0001", err)
	}
	packed := pickAndPack(t, s, ctx, sh)
	cur := packed.Shipment

	in := store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(sh, "2.00")}
	if _, err := s.PostPackingSlip(ctx, cur.ID, cur.Version, in, idem("post")); problemCode(err) != store.CodeTrackingRequired {
		t.Fatalf("post with no tracking: %v, want FUL-0006", err)
	}
	if n := countEvents(t, admin, fulevents.PackingSlipPosted); n != 0 {
		t.Fatalf("a refused post wrote %d events", n)
	}
	// Short coverage is refused too.
	cur, err := capture(t, s, ctx, cur, 0, domain.TrackingEntry{BatchNo: "L-1", LocationCode: "A-01", Qty: dq("6")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostPackingSlip(ctx, cur.ID, cur.Version, in, idem("post")); problemCode(err) != store.CodeTrackingRequired {
		t.Fatalf("post with 6 of 10 tracked: %v, want FUL-0006", err)
	}
	// Over the picked quantity is refused.
	if _, err := capture(t, s, ctx, cur, 0, domain.TrackingEntry{BatchNo: "L-1", Qty: dq("11")}); problemCode(err) != store.CodeTrackingQty {
		t.Fatalf("11 tracked of 10 picked: %v, want FUL-0035", err)
	}
	// Replacing the set works and bumps the version.
	v := cur.Version
	cur, err = capture(t, s, ctx, cur, 0,
		domain.TrackingEntry{BatchNo: "L-1", LocationCode: "A-01", Qty: dq("6")}, domain.TrackingEntry{BatchNo: "L-2", LocationCode: "A-02", Qty: dq("4")})
	if err != nil || cur.Version != v+1 || len(cur.Lines[0].Tracking) != 2 {
		t.Fatalf("replace: %+v %v", cur.Lines[0].Tracking, err)
	}
	if _, err := capture(t, s, ctx, store.Shipment{ID: cur.ID, Version: v, Lines: cur.Lines}, 0, domain.TrackingEntry{BatchNo: "L-3", Qty: dq("1")}); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale version: %v, want 412", err)
	}
	slip, err := s.PostPackingSlip(ctx, cur.ID, cur.Version, in, idem("post"))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	var entries []fulevents.TrackingEntry
	if err := json.Unmarshal([]byte(outboxData(t, admin, fulevents.PackingSlipPosted, "{lines,0,tracking}")), &entries); err != nil || len(entries) != 2 {
		t.Fatalf("posted tracking = %+v, %v", entries, err)
	}
	if got := outboxData(t, admin, fulevents.PackingSlipPosted, "{lines,0,product_id}"); got != sh.Lines[0].ProductID {
		t.Fatalf("posted product_id = %q", got)
	}
	shipped := getShipment(t, s, ctx, sh.ID)
	for _, e := range shipped.Lines[0].Tracking {
		if e.PackingSlipID == nil || *e.PackingSlipID != slip.ID {
			t.Fatalf("entry not stamped with the slip: %+v", e)
		}
	}
	// Shipped entries cannot be replaced once the shipment is SHIPPED.
	if _, err := capture(t, s, ctx, shipped, 0); problemCode(err) != store.CodeStatus {
		t.Fatalf("capture on a shipped shipment: %v, want FUL-0001", err)
	}
	// Cancelling the slip puts the entries back on the pick.
	if _, err := s.CancelPackingSlip(ctx, slip.ID, idem("cancel")); err != nil {
		t.Fatal(err)
	}
	back := getShipment(t, s, ctx, sh.ID)
	for _, e := range back.Lines[0].Tracking {
		if e.PackingSlipID != nil {
			t.Fatalf("entry still stamped after cancel: %+v", e)
		}
	}
	if len(back.Lines[0].Tracking) != 2 {
		t.Fatalf("tracking after cancel = %d entries, want 2", len(back.Lines[0].Tracking))
	}
}

// A serial number belongs to one live line of its product; cancelling the
// shipment frees it. Serials of another product do not clash.
func TestTracking_SerialUniquePerProduct(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(2, "")
	a := releasedTracked(t, s, ctx, 2)
	// Same product on a second shipment: release it with the first one's product id.
	order, wh := ids.New(), ids.New()
	groups, _ := domain.Release([]domain.ReleaseLine{{OrderID: order, OrderLineID: ids.New(), CustomerID: ids.New(), LegalEntityID: ids.New(),
		WarehouseID: wh, ProductID: a.Lines[0].ProductID, Unit: "EA", Qty: decimal.NewFromInt(1), ConfirmedShipDate: today(), ATPOK: true, TrackingRequired: true}},
		domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
	res, err := s.RunRelease(ctx, wh, groups, idem("rel2"))
	if err != nil {
		t.Fatal(err)
	}
	b := res.Shipments[0]

	pick := func(sh store.Shipment) store.Shipment {
		out, err := s.ConfirmPick(ctx, sh.ID, sh.Version, []store.PickInput{{ShipmentLineID: sh.Lines[0].ID, Picked: sh.Lines[0].Planned}}, idem("p"))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	a, b = pick(a), pick(b)
	a, err = capture(t, s, ctx, a, 0, domain.TrackingEntry{SerialNo: "SN-100", Qty: dq("1")}, domain.TrackingEntry{SerialNo: "SN-101", Qty: dq("1")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capture(t, s, ctx, b, 0, domain.TrackingEntry{SerialNo: "SN-101", Qty: dq("1")}); problemCode(err) != store.CodeSerialTaken {
		t.Fatalf("duplicate serial: %v, want FUL-0036", err)
	}
	// Recapturing the same serials on the same line is fine.
	if a, err = capture(t, s, ctx, a, 0, domain.TrackingEntry{SerialNo: "SN-101", Qty: dq("1")}); err != nil {
		t.Fatalf("recapture on the same line: %v", err)
	}
	if _, err := s.CancelShipment(ctx, a.ID, a.Version, idem("c")); err != nil {
		t.Fatal(err)
	}
	if _, err := capture(t, s, ctx, b, 0, domain.TrackingEntry{SerialNo: "SN-101", Qty: dq("1")}); err != nil {
		t.Fatalf("serial freed by the cancel: %v", err)
	}
}

// Partial shipment: entries shipped on the first slip stay; the remainder is
// tracked and shipped on the second slip.
func TestTracking_PartialShipmentKeepsShippedEntries(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(3, "")
	sh := releasedTracked(t, s, ctx, 10)
	line := sh.Lines[0].ID
	cur, err := s.ConfirmPick(ctx, sh.ID, sh.Version, []store.PickInput{{ShipmentLineID: line, Picked: dq("6"), Reason: "OUT_OF_STOCK"}}, idem("p"))
	if err != nil {
		t.Fatal(err)
	}
	if cur, err = capture(t, s, ctx, cur, 0, domain.TrackingEntry{BatchNo: "L-1", Qty: dq("6")}); err != nil {
		t.Fatal(err)
	}
	pr, err := s.PackContainer(ctx, cur.ID, cur.Version, "CARTON-C12", []store.PackInput{{ShipmentLineID: line, Qty: dq("6")}}, idem("pack"))
	if err != nil {
		t.Fatal(err)
	}
	in := store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(sh, "1.00")}
	if _, err := s.PostPackingSlip(ctx, pr.Shipment.ID, pr.Shipment.Version, in, idem("post1")); err != nil {
		t.Fatal(err)
	}
	cur = getShipment(t, s, ctx, sh.ID)
	if cur.Status != domain.StatusPartiallyShipped {
		t.Fatalf("status = %s", cur.Status)
	}
	// Shipped 6 + new 5 would exceed the 10 planned only after picking; pick the rest first.
	if _, err := capture(t, s, ctx, cur, 0, domain.TrackingEntry{BatchNo: "L-2", Qty: dq("1")}); problemCode(err) != store.CodeTrackingQty {
		t.Fatalf("tracking beyond the picked quantity: %v, want FUL-0035", err)
	}
	if cur, err = s.ConfirmPick(ctx, cur.ID, cur.Version, []store.PickInput{{ShipmentLineID: line, Picked: dq("10")}}, idem("p2")); err != nil {
		t.Fatal(err)
	}
	if cur, err = capture(t, s, ctx, cur, 0, domain.TrackingEntry{BatchNo: "L-2", Qty: dq("4")}); err != nil {
		t.Fatal(err)
	}
	if len(cur.Lines[0].Tracking) != 2 || cur.Lines[0].Tracking[0].PackingSlipID == nil || cur.Lines[0].Tracking[1].PackingSlipID != nil {
		t.Fatalf("tracking after partial: %+v", cur.Lines[0].Tracking)
	}
	pr, err = s.PackContainer(ctx, cur.ID, cur.Version, "CARTON-C12", []store.PackInput{{ShipmentLineID: line, Qty: dq("4")}}, idem("pack2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostPackingSlip(ctx, pr.Shipment.ID, pr.Shipment.Version, in, idem("post2")); err != nil {
		t.Fatalf("second slip: %v", err)
	}
}

// A re-pick cannot drop below the captured entries (FUL-0035), and closing the
// remainder drops the entries that never shipped so their serials are free.
func TestTracking_RepickBelowEntriesAndCloseRemainder(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(5, "")

	// While a shipment is still PICKING a line can be re-picked, but not below
	// the units its entries already cover.
	two := releasedTracked(t, s, ctx, 3, 3)
	picking, err := s.ConfirmPick(ctx, two.ID, two.Version, []store.PickInput{{ShipmentLineID: two.Lines[0].ID, Picked: dq("3")}}, idem("two-p1"))
	if err != nil || picking.Status != domain.StatusPicking {
		t.Fatalf("first pick: %v %v", picking.Status, err)
	}
	if picking, err = capture(t, s, ctx, picking, 0, domain.TrackingEntry{BatchNo: "L-1", Qty: dq("3")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConfirmPick(ctx, picking.ID, picking.Version, []store.PickInput{{ShipmentLineID: two.Lines[0].ID, Picked: dq("2"), Reason: "DAMAGED"}}, idem("two-p2")); problemCode(err) != store.CodeTrackingQty {
		t.Fatalf("re-pick below the entries: %v, want FUL-0035", err)
	}

	sh := releasedTracked(t, s, ctx, 4)
	line := sh.Lines[0].ID
	cur, err := s.ConfirmPick(ctx, sh.ID, sh.Version, []store.PickInput{{ShipmentLineID: line, Picked: dq("1"), Reason: "OUT_OF_STOCK"}}, idem("p1"))
	if err != nil {
		t.Fatal(err)
	}
	if cur, err = capture(t, s, ctx, cur, 0, domain.TrackingEntry{SerialNo: "SN-1", Qty: dq("1")}); err != nil {
		t.Fatal(err)
	}
	pr, err := s.PackContainer(ctx, cur.ID, cur.Version, "CARTON-C12", []store.PackInput{{ShipmentLineID: line, Qty: dq("1")}}, idem("pack"))
	if err != nil {
		t.Fatal(err)
	}
	in := store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(sh, "1.00")}
	if _, err := s.PostPackingSlip(ctx, pr.Shipment.ID, pr.Shipment.Version, in, idem("post")); err != nil {
		t.Fatal(err)
	}
	cur = getShipment(t, s, ctx, sh.ID)
	if cur, err = s.ConfirmPick(ctx, cur.ID, cur.Version, []store.PickInput{{ShipmentLineID: line, Picked: dq("3")}}, idem("p2")); err != nil {
		t.Fatal(err)
	}
	if cur, err = capture(t, s, ctx, cur, 0, domain.TrackingEntry{SerialNo: "SN-2", Qty: dq("1")}, domain.TrackingEntry{SerialNo: "SN-3", Qty: dq("1")}); err != nil {
		t.Fatal(err)
	}
	closed, err := s.CloseRemainder(ctx, cur.ID, cur.Version, idem("close"))
	if err != nil {
		t.Fatal(err)
	}
	if got := closed.Lines[0].Tracking; len(got) != 1 || got[0].SerialNo == nil || *got[0].SerialNo != "SN-1" {
		t.Fatalf("tracking after close = %+v, want the shipped SN-1 only", got)
	}
	var open int
	if err := admin.QueryRow(context.Background(), `select count(*) from pick_tracking where shipment_id = $1 and packing_slip_id is null`, sh.ID).Scan(&open); err != nil || open != 0 {
		t.Fatalf("open entries after close = %d, %v", open, err)
	}
	if back := getShipment(t, s, ctx, sh.ID); len(back.Lines[0].Tracking) != 1 {
		t.Fatalf("stored tracking after close = %+v", back.Lines[0].Tracking)
	}
}

func TestTracking_TenantAndLegalEntityIsolation(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(4, "")
	sh := releasedTracked(t, s, ctx, 3)
	cur, err := s.ConfirmPick(ctx, sh.ID, sh.Version, []store.PickInput{{ShipmentLineID: sh.Lines[0].ID, Picked: dq("3")}}, idem("p"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := capture(t, s, ctxFor(5, ""), cur, 0, domain.TrackingEntry{BatchNo: "L-1", Qty: dq("3")}); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("other tenant: %v", err)
	}
	if _, err := capture(t, s, ctxFor(4, ids.New()), cur, 0, domain.TrackingEntry{BatchNo: "L-1", Qty: dq("3")}); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("other legal entity: %v", err)
	}
	if _, err := capture(t, s, ctx, cur, 0, domain.TrackingEntry{Qty: dq("3")}); problemCode(err) != store.CodeValidation {
		t.Fatalf("entry without batch or serial: %v", err)
	}
	cur, err = capture(t, s, ctx, cur, 0, domain.TrackingEntry{BatchNo: "L-1", Qty: dq("3")})
	if err != nil {
		t.Fatal(err)
	}
	if got := outboxData(t, admin, fulevents.PickTracked, "{line,entries,0,batch_no}"); got != "L-1" {
		t.Fatalf("pick.tracked batch = %q", got)
	}
	if other, err := s.GetShipment(ctxFor(5, ""), cur.ID); err == nil {
		t.Fatalf("other tenant read the shipment: %+v", other)
	}
}
