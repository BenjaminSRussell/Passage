package store_test

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// A JOURNAL order never releases (the line is skipped), and a shipment that
// carries the type anyway (stored from an earlier release) refuses pick, pack,
// auto-pack, tracking capture and post with FUL-0065, and stays out of waves.
func TestJournalOrder_NeverReleasesPicksOrShips(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(11, "")
	warehouse, le, order, customer := ids.New(), ids.New(), ids.New(), ids.New()
	journal := domain.ReleaseLine{OrderID: order, OrderLineID: ids.New(), CustomerID: customer, LegalEntityID: le, WarehouseID: warehouse,
		ProductID: ids.New(), Unit: "EA", Qty: decimal.NewFromInt(3), ConfirmedShipDate: today(), ATPOK: true, OrderType: domain.OrderTypeJournal}
	groups, skipped := domain.Release([]domain.ReleaseLine{journal}, domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
	if len(groups) != 0 || len(skipped) != 1 || skipped[0].Reason != domain.SkipJournalOrder {
		t.Fatalf("journal release = %v, %v; want one journal_order skip", groups, skipped)
	}

	// An ORDER shipment released normally stores its type.
	f := releasedDefault(t, s, ctx, 4)
	if f.shipment.OrderType != "ORDER" || f.shipment.Priority != 5 {
		t.Fatalf("order type, priority = %q, %d; want ORDER, 5", f.shipment.OrderType, f.shipment.Priority)
	}
	// Force the type the way a stored JOURNAL shipment would carry it.
	if _, err := admin.Exec(ctx, `update shipment set order_type = 'JOURNAL' where id = $1`, f.shipment.ID); err != nil {
		t.Fatal(err)
	}
	line := f.shipment.Lines[0]
	picks := []store.PickInput{{ShipmentLineID: line.ID, Picked: line.Planned}}
	if _, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, picks, idem("pick")); problemCode(err) != store.CodeJournalOrder {
		t.Fatalf("pick = %v, want %s", err, store.CodeJournalOrder)
	}
	if _, err := s.PackContainer(ctx, f.shipment.ID, f.shipment.Version, "CARTON-C12", []store.PackInput{{ShipmentLineID: line.ID, Qty: line.Planned}}, idem("pack")); problemCode(err) != store.CodeJournalOrder {
		t.Fatalf("pack = %v, want %s", err, store.CodeJournalOrder)
	}
	if _, err := s.AutoPack(ctx, f.shipment.ID, f.shipment.Version, nil, idem("auto")); problemCode(err) != store.CodeJournalOrder {
		t.Fatalf("auto-pack = %v, want %s", err, store.CodeJournalOrder)
	}
	if _, err := s.PostPackingSlip(ctx, f.shipment.ID, f.shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "1")}, idem("post")); problemCode(err) != store.CodeJournalOrder {
		t.Fatalf("post = %v, want %s", err, store.CodeJournalOrder)
	}
	if _, err := s.CaptureTracking(ctx, f.shipment.ID, line.ID, f.shipment.Version, nil, idem("track")); problemCode(err) != store.CodeJournalOrder {
		t.Fatalf("capture tracking = %v, want %s", err, store.CodeJournalOrder)
	}
	plans, err := s.PreviewWaves(ctx, store.WaveRequest{WarehouseID: f.warehouse, GroupBy: "CUSTOMER", Limits: domain.WaveLimits{MaxLines: 200, MaxOrders: 50}})
	if err != nil || len(plans) != 0 {
		t.Fatalf("wave preview = %v, %v; want no wave for a JOURNAL shipment", plans, err)
	}
	if got, err := s.GetShipment(ctx, f.shipment.ID); err != nil || got.Lines[0].Picked.Sign() != 0 || got.Status != domain.StatusReleased {
		t.Fatalf("shipment changed: %+v, %v", got, err)
	}
	// Cancelling is still allowed.
	if _, err := s.CancelShipment(ctx, f.shipment.ID, f.shipment.Version, idem("cancel")); err != nil {
		t.Fatalf("cancel: %v", err)
	}
}

// Higher priority orders (1 first) get their shipments, and so their numbers, first.
func TestRelease_PriorityOrdersShipments(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(12, "")
	warehouse, le, customer := ids.New(), ids.New(), ids.New()
	mk := func(priority int) domain.ReleaseLine {
		return domain.ReleaseLine{OrderID: ids.New(), OrderLineID: ids.New(), CustomerID: customer, LegalEntityID: le, WarehouseID: warehouse,
			ProductID: ids.New(), Unit: "EA", Qty: decimal.NewFromInt(1), ConfirmedShipDate: today(), ATPOK: true, Priority: priority}
	}
	low, urgent, def := mk(9), mk(1), mk(0)
	groups, _ := domain.Release([]domain.ReleaseLine{low, def, urgent}, domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
	res, err := s.RunRelease(ctx, warehouse, groups, idem("prio"))
	if err != nil || len(res.Shipments) != 3 {
		t.Fatalf("release = %+v, %v", res, err)
	}
	want := []struct {
		order string
		prio  int
	}{{urgent.OrderID, 1}, {def.OrderID, 5}, {low.OrderID, 9}}
	for i, w := range want {
		if res.Shipments[i].OrderID != w.order || res.Shipments[i].Priority != w.prio {
			t.Fatalf("shipment %d = order %s priority %d, want %s priority %d", i, res.Shipments[i].OrderID, res.Shipments[i].Priority, w.order, w.prio)
		}
	}
	if !(res.Shipments[0].ShipmentNumber < res.Shipments[1].ShipmentNumber && res.Shipments[1].ShipmentNumber < res.Shipments[2].ShipmentNumber) {
		t.Fatalf("numbers not in priority order: %s %s %s", res.Shipments[0].ShipmentNumber, res.Shipments[1].ShipmentNumber, res.Shipments[2].ShipmentNumber)
	}
}

// With full ATP not required, a line releases for the ATP it has and a later
// run releases what is still open; a whole line stays already_released.
func TestRelease_PartialByATPThenRemainder(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(13, "")
	warehouse, le, order, customer, lineID, product := ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	atp := func(n int64) *decimal.Decimal { d := decimal.NewFromInt(n); return &d }
	offer := func(atpQty *decimal.Decimal) []domain.ReleaseGroup {
		g, skipped := domain.Release([]domain.ReleaseLine{{OrderID: order, OrderLineID: lineID, CustomerID: customer, LegalEntityID: le, WarehouseID: warehouse,
			ProductID: product, Unit: "EA", Qty: decimal.NewFromInt(10), ConfirmedShipDate: today(), ATPQty: atpQty}},
			domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: false})
		if len(skipped) != 0 {
			t.Fatalf("skipped %v", skipped)
		}
		return g
	}
	first, err := s.RunRelease(ctx, warehouse, offer(atp(4)), idem("p1"))
	if err != nil || len(first.Shipments) != 1 || !first.Shipments[0].Lines[0].Planned.Equal(decimal.NewFromInt(4)) || !first.Shipments[0].Lines[0].Partial {
		t.Fatalf("first run = %+v, %v; want 4 of 10 flagged partial", first, err)
	}
	// Offered again with the same ATP: nothing more is available beyond what is open (6), ATP 4 releases 4.
	second, err := s.RunRelease(ctx, warehouse, offer(atp(4)), idem("p2"))
	if err != nil || len(second.Shipments) != 1 || !second.Shipments[0].Lines[0].Planned.Equal(decimal.NewFromInt(4)) || !second.Shipments[0].Lines[0].Partial {
		t.Fatalf("second run = %+v, %v; want 4 more, still partial", second, err)
	}
	// ATP of 50 releases only the 2 left open, and that completes the line.
	third, err := s.RunRelease(ctx, warehouse, offer(atp(50)), idem("p3"))
	if err != nil || len(third.Shipments) != 1 || !third.Shipments[0].Lines[0].Planned.Equal(decimal.NewFromInt(2)) || third.Shipments[0].Lines[0].Partial {
		t.Fatalf("third run = %+v, %v; want the last 2, not partial", third, err)
	}
	fourth, err := s.RunRelease(ctx, warehouse, offer(atp(50)), idem("p4"))
	if err != nil || len(fourth.Shipments) != 0 || len(fourth.Skipped) != 1 || fourth.Skipped[0].Reason != domain.SkipAlreadyReleased {
		t.Fatalf("fourth run = %+v, %v; want already_released", fourth, err)
	}
}

// Cancelling the partial shipment after the remainder was released frees its
// quantity: the next run releases it again instead of skipping the line as
// already_released. A remainder short-picked to a backorder stays dealt with.
func TestRelease_PartialCancelledAndBackorderedRemainder(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(14, "")
	warehouse, le, order, customer, lineID, product := ids.New(), ids.New(), ids.New(), ids.New(), ids.New(), ids.New()
	atp := func(n int64) *decimal.Decimal { d := decimal.NewFromInt(n); return &d }
	offer := func(atpQty *decimal.Decimal) []domain.ReleaseGroup {
		g, _ := domain.Release([]domain.ReleaseLine{{OrderID: order, OrderLineID: lineID, CustomerID: customer, LegalEntityID: le, WarehouseID: warehouse,
			ProductID: product, Unit: "EA", Qty: decimal.NewFromInt(10), ConfirmedShipDate: today(), ATPQty: atpQty}},
			domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: false})
		return g
	}
	first, err := s.RunRelease(ctx, warehouse, offer(atp(4)), idem("c1"))
	if err != nil || len(first.Shipments) != 1 {
		t.Fatalf("first run = %+v, %v", first, err)
	}
	second, err := s.RunRelease(ctx, warehouse, offer(atp(50)), idem("c2"))
	if err != nil || len(second.Shipments) != 1 || !second.Shipments[0].Lines[0].Planned.Equal(decimal.NewFromInt(6)) {
		t.Fatalf("second run = %+v, %v; want the 6 left open", second, err)
	}
	sh := first.Shipments[0]
	if _, err := s.CancelShipment(ctx, sh.ID, sh.Version, idem("c-cancel")); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	third, err := s.RunRelease(ctx, warehouse, offer(atp(50)), idem("c3"))
	if err != nil || len(third.Shipments) != 1 || !third.Shipments[0].Lines[0].Planned.Equal(decimal.NewFromInt(4)) {
		t.Fatalf("third run = %+v, %v; want the 4 of the cancelled partial shipment again", third, err)
	}

	// Short-pick the 4 to 1 and post: the unsaved warehouse backorders the 3, so
	// the line is fully dealt with (1 shipped + 3 on the backorder + 6) and a
	// further run releases nothing.
	re := third.Shipments[0]
	line := re.Lines[0]
	picked, err := s.ConfirmPick(ctx, re.ID, re.Version, []store.PickInput{{ShipmentLineID: line.ID, Picked: decimal.NewFromInt(1), Reason: "DAMAGED"}}, idem("c-pick"))
	if err != nil {
		t.Fatalf("pick: %v", err)
	}
	packed, err := s.PackContainer(ctx, re.ID, picked.Version, "BOX", []store.PackInput{{ShipmentLineID: line.ID, Qty: decimal.NewFromInt(1)}}, idem("c-pack"))
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	if _, err := s.PostPackingSlip(ctx, re.ID, packed.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(re, "1")}, idem("c-post")); err != nil {
		t.Fatalf("post: %v", err)
	}
	if got, err := s.GetShipment(ctx, re.ID); err != nil || got.BackorderID == nil {
		t.Fatalf("posted shipment = %+v, %v; want a backorder shipment", got, err)
	}
	fourth, err := s.RunRelease(ctx, warehouse, offer(atp(50)), idem("c4"))
	if err != nil || len(fourth.Shipments) != 0 || len(fourth.Skipped) != 1 || fourth.Skipped[0].Reason != domain.SkipAlreadyReleased {
		t.Fatalf("fourth run = %+v, %v; want already_released", fourth, err)
	}
}
