package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func getShipment(t *testing.T, s *store.Postgres, ctx context.Context, id string) store.Shipment {
	t.Helper()
	sh, err := s.GetShipment(ctx, id)
	if err != nil {
		t.Fatalf("GetShipment: %v", err)
	}
	return sh
}

func containerStatuses(sh store.Shipment) map[string]string {
	out := map[string]string{}
	for _, c := range sh.Containers {
		out[c.ContainerType] = c.Status
	}
	return out
}

// AC-28.4 and FR-28.8: a 100-unit line picked 90 posts 90 and leaves the
// shipment PARTIALLY_SHIPPED with 10 to go; the remainder is re-picked, packed
// and posted on a second slip, and the shipment becomes SHIPPED. Cancelling the
// first slip then reverts only its own container and returnable balance.
func TestPartialShipment_RemainderShipsOnASecondSlip(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	saveType(t, s, ctx, store.ContainerType{Code: "EUR-PALLET", TareKg: d("25"), Returnable: true})
	saveType(t, s, ctx, store.ContainerType{Code: "CRATE", TareKg: d("2"), Returnable: true})
	f := released(t, s, ctx, 100)
	line := f.shipment.Lines[0]

	picked, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, []store.PickInput{{ShipmentLineID: line.ID, Picked: d("90")}}, idem("pick-90"))
	if err != nil || picked.Status != domain.StatusPicked {
		t.Fatalf("short pick: %v status %v", err, picked.Status)
	}
	packed, err := s.PackContainer(ctx, f.shipment.ID, picked.Version, "EUR-PALLET", []store.PackInput{{ShipmentLineID: line.ID, Qty: d("90")}}, idem("pack-90"))
	if err != nil || packed.Shipment.Status != domain.StatusPacked {
		t.Fatalf("pack 90: %v status %v", err, packed.Shipment.Status)
	}
	first, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "2"), EmitASN: true}, idem("post-1"))
	if err != nil {
		t.Fatalf("first post: %v", err)
	}
	partial := getShipment(t, s, ctx, f.shipment.ID)
	if partial.Status != domain.StatusPartiallyShipped || partial.ActualShipDate == nil || !partial.Lines[0].Shipped.Equal(d("90")) {
		t.Fatalf("after first post: status %s actual %v shipped %s", partial.Status, partial.ActualShipDate, partial.Lines[0].Shipped)
	}
	if partial.Containers[0].PackingSlipID == nil || *partial.Containers[0].PackingSlipID != first.ID {
		t.Fatalf("container slip = %v, want %s", partial.Containers[0].PackingSlipID, first.ID)
	}
	// The ASN names the container of this slip with its SSCC.
	if got := outboxData(t, admin, fulevents.ASNIssued, "{containers,0,sscc}"); got != partial.Containers[0].SSCC {
		t.Fatalf("ASN sscc = %q, want %s", got, partial.Containers[0].SSCC)
	}
	if got := outboxData(t, admin, fulevents.ASNIssued, "{packing_slip_id}"); got != first.ID {
		t.Fatalf("ASN slip = %q, want %s", got, first.ID)
	}

	// A re-pick below what is packed is refused; the remainder is re-picked from PARTIALLY_SHIPPED.
	if _, err := s.ConfirmPick(ctx, f.shipment.ID, partial.Version, []store.PickInput{{ShipmentLineID: line.ID, Picked: d("80")}}, idem("pick-80")); problemCode(err) != store.CodePickBelowPacked {
		t.Fatalf("pick below packed: %v, want %s", err, store.CodePickBelowPacked)
	}
	repicked, err := s.ConfirmPick(ctx, f.shipment.ID, partial.Version, []store.PickInput{{ShipmentLineID: line.ID, Picked: d("100")}}, idem("pick-100"))
	if err != nil || repicked.Status != domain.StatusPicked || repicked.ActualShipDate == nil {
		t.Fatalf("re-pick: %v status %v actual %v", err, repicked.Status, repicked.ActualShipDate)
	}
	repacked, err := s.PackContainer(ctx, f.shipment.ID, repicked.Version, "CRATE", []store.PackInput{{ShipmentLineID: line.ID, Qty: d("10")}}, idem("pack-10"))
	if err != nil || repacked.Shipment.Status != domain.StatusPacked {
		t.Fatalf("pack 10: %v status %v", err, repacked.Shipment.Status)
	}
	second, err := s.PostPackingSlip(ctx, f.shipment.ID, repacked.Shipment.Version,
		store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "2")}, idem("post-2"))
	if err != nil || !second.Lines[0].Qty.Equal(d("10")) {
		t.Fatalf("second post: %v lines %+v", err, second.Lines)
	}
	done := getShipment(t, s, ctx, f.shipment.ID)
	if done.Status != domain.StatusShipped || !done.Lines[0].Shipped.Equal(d("100")) || !done.ActualShipDate.Equal(*partial.ActualShipDate) {
		t.Fatalf("after second post: status %s shipped %s actual %v (first %v)", done.Status, done.Lines[0].Shipped, done.ActualShipDate, partial.ActualShipDate)
	}
	balances, err := s.ListReturnableBalances(ctx, f.shipment.CustomerID, 10)
	if err != nil || len(balances) != 2 {
		t.Fatalf("balances = %+v, %v", balances, err)
	}

	// Cancelling the first slip takes back its 90 and its pallet only.
	if _, err := s.CancelPackingSlip(ctx, first.ID, idem("cancel-1")); err != nil {
		t.Fatalf("cancel first slip: %v", err)
	}
	after := getShipment(t, s, ctx, f.shipment.ID)
	if after.Status != domain.StatusPacked || !after.Lines[0].Shipped.Equal(d("10")) || after.ActualShipDate == nil {
		t.Fatalf("after cancel: status %s shipped %s actual %v", after.Status, after.Lines[0].Shipped, after.ActualShipDate)
	}
	if st := containerStatuses(after); st["EUR-PALLET"] != "CLOSED" || st["CRATE"] != "SHIPPED" {
		t.Fatalf("container statuses after cancel = %v, want the pallet CLOSED and the crate SHIPPED", st)
	}
	balances, _ = s.ListReturnableBalances(ctx, f.shipment.CustomerID, 10)
	for _, b := range balances {
		want := map[string]string{"EUR-PALLET": "0", "CRATE": "1"}[b.ContainerType]
		if !b.Balance.Equal(d(want)) {
			t.Fatalf("balance %s = %s, want %s", b.ContainerType, b.Balance, want)
		}
	}
	// The second slip is still live; posting again ships the 90 back out on a third slip.
	third, err := s.PostPackingSlip(ctx, f.shipment.ID, after.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "2")}, idem("post-3"))
	if err != nil || !third.Lines[0].Qty.Equal(d("90")) {
		t.Fatalf("re-post: %v lines %+v", err, third.Lines)
	}
	if final := getShipment(t, s, ctx, f.shipment.ID); final.Status != domain.StatusShipped || containerStatuses(final)["EUR-PALLET"] != "SHIPPED" {
		t.Fatalf("after re-post: status %s containers %v", final.Status, containerStatuses(final))
	}
	if n := countEvents(t, admin, fulevents.ASNIssued); n != 1 {
		t.Fatalf("asn.issued events = %d, want 1 (only the first post asked for it)", n)
	}
}

// Cancelling the first slip of a partially shipped shipment whose remainder is
// already picked keeps it PICKED; packing the rest makes it PACKED and one post
// ships everything.
func TestPartialShipment_CancelWhileTheRemainderIsPicked(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 10, 5)
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
	partial := getShipment(t, s, ctx, f.shipment.ID)
	// Picking only the line that has a remainder: the other line stays fully shipped.
	repicked, err := s.ConfirmPick(ctx, f.shipment.ID, partial.Version, []store.PickInput{{ShipmentLineID: l1.ID, Picked: d("10")}}, idem("p2"))
	if err != nil || repicked.Status != domain.StatusPicked {
		t.Fatalf("re-pick: %v status %v", err, repicked.Status)
	}
	if _, err := s.CancelPackingSlip(ctx, slip.ID, idem("c1")); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	after := getShipment(t, s, ctx, f.shipment.ID)
	if after.Status != domain.StatusPicked || !after.Lines[0].Shipped.IsZero() || after.ActualShipDate != nil {
		t.Fatalf("after cancel: status %s shipped %s actual %v", after.Status, after.Lines[0].Shipped, after.ActualShipDate)
	}
	repacked, err := s.PackContainer(ctx, f.shipment.ID, after.Version, "BOX", []store.PackInput{{ShipmentLineID: l1.ID, Qty: d("4")}}, idem("k2"))
	if err != nil || repacked.Shipment.Status != domain.StatusPacked {
		t.Fatalf("pack remainder: %v status %v", err, repacked.Shipment.Status)
	}
	all, err := s.PostPackingSlip(ctx, f.shipment.ID, repacked.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "1")}, idem("s2"))
	if err != nil || len(all.Lines) != 2 || !all.TotalCost.Equal(d("15")) {
		t.Fatalf("post all: %v total %s lines %d", err, all.TotalCost, len(all.Lines))
	}
	if done := getShipment(t, s, ctx, f.shipment.ID); done.Status != domain.StatusShipped {
		t.Fatalf("final status %s", done.Status)
	}
}

// BR-28.6: a shipment cancels before anything ships and while it holds no
// container; its order lines can then be released again.
func TestCancelShipment(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 5)
	if _, err := s.CancelShipment(ctx, f.shipment.ID, f.shipment.Version+1, idem("stale")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale cancel: %v, want %s", err, store.CodeVersion)
	}
	key := idem("cancel")
	cancelled, err := s.CancelShipment(ctx, f.shipment.ID, f.shipment.Version, key)
	if err != nil || cancelled.Status != domain.StatusCancelled || cancelled.Version != f.shipment.Version+1 {
		t.Fatalf("cancel: %v status %v version %d", err, cancelled.Status, cancelled.Version)
	}
	if replay, err := s.CancelShipment(ctx, f.shipment.ID, f.shipment.Version, key); err != nil || replay.Version != cancelled.Version {
		t.Fatalf("replayed cancel: %v version %d", err, replay.Version)
	}
	if _, err := s.CancelShipment(ctx, f.shipment.ID, cancelled.Version, idem("again")); problemCode(err) != store.CodeInvalidMove {
		t.Fatalf("cancel twice: %v, want %s", err, store.CodeInvalidMove)
	}
	if n := countEvents(t, admin, fulevents.ShipmentCancelled); n != 1 {
		t.Fatalf("shipment.cancelled events = %d, want 1", n)
	}
	if got := outboxData(t, admin, fulevents.ShipmentCancelled, "{lines,0,order_line_id}"); got != f.shipment.Lines[0].OrderLineID {
		t.Fatalf("event line = %q, want %s", got, f.shipment.Lines[0].OrderLineID)
	}
	// The cancelled shipment no longer blocks its order line.
	l := f.shipment.Lines[0]
	group := domain.ReleaseGroup{OrderID: f.shipment.OrderID, WarehouseID: f.warehouse, ShipDate: today(), Lines: []domain.ReleaseLine{{
		OrderID: f.shipment.OrderID, OrderLineID: l.OrderLineID, CustomerID: f.shipment.CustomerID, LegalEntityID: f.legalEntity,
		WarehouseID: f.warehouse, ProductID: l.ProductID, Unit: l.Unit, Qty: l.Planned, ConfirmedShipDate: today(), ATPOK: true}}}
	res, err := s.RunRelease(ctx, f.warehouse, []domain.ReleaseGroup{group}, idem("re-release"))
	if err != nil || len(res.Shipments) != 1 || len(res.Skipped) != 0 {
		t.Fatalf("re-release: %+v, %v", res, err)
	}

	// A packed shipment holds a container and cannot cancel.
	g := released(t, s, ctx, 3)
	packed := pickAndPack(t, s, ctx, g.shipment)
	if _, err := s.CancelShipment(ctx, g.shipment.ID, packed.Shipment.Version, idem("packed")); problemCode(err) != store.CodeInvalidMove {
		t.Fatalf("cancel packed: %v, want %s", err, store.CodeInvalidMove)
	}
}

// releaseMany releases one shipment per entry of lines (each a line count), each
// for its own order, into one warehouse with carrier UPS. Entries with the same
// customers[i] share a customer; zone gives the pick zone of shipment i.
func releaseMany(t *testing.T, s *store.Postgres, ctx context.Context, warehouse, legalEntity string, lines []int, customers []string, zone func(i int) string) []store.Shipment {
	t.Helper()
	var groups []domain.ReleaseGroup
	for i, n := range lines {
		order, customer := ids.New(), customers[i]
		g := domain.ReleaseGroup{OrderID: order, WarehouseID: warehouse, ShipDate: today()}
		for j := 0; j < n; j++ {
			g.Lines = append(g.Lines, domain.ReleaseLine{OrderID: order, OrderLineID: ids.New(), CustomerID: customer, LegalEntityID: legalEntity,
				WarehouseID: warehouse, ProductID: ids.New(), Unit: "EA", Qty: decimal.NewFromInt(1), ConfirmedShipDate: today(), ATPOK: true,
				CarrierCode: "UPS", Zone: zone(i)})
		}
		groups = append(groups, g)
	}
	res, err := s.RunRelease(ctx, warehouse, groups, idem("rel-many"))
	if err != nil || len(res.Shipments) != len(lines) {
		t.Fatalf("RunRelease: %d shipments, %v", len(res.Shipments), err)
	}
	return res.Shipments
}

// AC-28.10: 60 released lines for carrier UPS across 12 customers, grouped with
// WAVE_MAX_LINES 20: the preview shows waves of at most 20 lines without
// splitting a shipment, and one action creates all of them.
func TestWorkbench_AC28_10(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	warehouse, le := ids.New(), ids.New()
	// 12 customers with two orders each, of 3 and 2 lines: 24 shipments, 60 lines.
	var counts []int
	var customers []string
	for c := 0; c < 12; c++ {
		customer := ids.New()
		counts = append(counts, 3, 2)
		customers = append(customers, customer, customer)
	}
	shipments := releaseMany(t, s, ctx, warehouse, le, counts, customers, func(i int) string { return fmt.Sprintf("Z%d", i%3) })
	if shipments[0].CarrierCode == nil || *shipments[0].CarrierCode != "UPS" || shipments[0].Lines[0].Zone == nil {
		t.Fatalf("released shipment carrier %v zone %v", shipments[0].CarrierCode, shipments[0].Lines[0].Zone)
	}
	req := store.WaveRequest{WarehouseID: warehouse, Carrier: "UPS", GroupBy: domain.GroupCarrier, Limits: domain.WaveLimits{MaxLines: 20, MaxOrders: 50}}
	plans, err := s.PreviewWaves(ctx, req)
	if err != nil {
		t.Fatalf("PreviewWaves: %v", err)
	}
	total, seen := 0, map[string]bool{}
	for _, p := range plans {
		if p.LineCount > 20 || p.GroupKey != "UPS" {
			t.Fatalf("plan %+v breaks the limit or the group", p)
		}
		for _, sh := range p.Shipments {
			if seen[sh.ID] || sh.ShipmentNumber == "" {
				t.Fatalf("shipment %+v planned twice or without a number", sh)
			}
			seen[sh.ID] = true
		}
		total += p.LineCount
	}
	if len(plans) != 3 || total != 60 || len(seen) != 24 {
		t.Fatalf("preview: %d waves, %d lines, %d shipments; want 3, 60, 24", len(plans), total, len(seen))
	}
	byCustomer, err := s.PreviewWaves(ctx, store.WaveRequest{WarehouseID: warehouse, GroupBy: domain.GroupCustomer, Limits: req.Limits})
	if err != nil || len(byCustomer) != 12 || len(byCustomer[0].Shipments) != 2 || byCustomer[0].LineCount != 5 {
		t.Fatalf("by customer: %d waves, %v; want 12 of 2 shipments and 5 lines", len(byCustomer), err)
	}
	// A zone filter selects the shipments with a line in that zone.
	zoned, err := s.PreviewWaves(ctx, store.WaveRequest{WarehouseID: warehouse, Zone: "Z0", GroupBy: domain.GroupZone, Limits: req.Limits})
	if err != nil || len(zoned) == 0 || zoned[0].GroupKey != "Z0" {
		t.Fatalf("zone preview: %+v, %v", zoned, err)
	}

	key := idem("waves")
	waves, err := s.CreateWaves(ctx, req, key)
	if err != nil || len(waves) != 3 {
		t.Fatalf("CreateWaves: %d waves, %v", len(waves), err)
	}
	if waves[0].WaveNumber != "WV-0000001" || waves[2].WaveNumber != "WV-0000003" || waves[0].Shipments[0].Status != domain.StatusPicking {
		t.Fatalf("waves %s..%s, first shipment %s", waves[0].WaveNumber, waves[2].WaveNumber, waves[0].Shipments[0].Status)
	}
	if replay, err := s.CreateWaves(ctx, req, key); err != nil || len(replay) != 3 || replay[0].ID != waves[0].ID {
		t.Fatalf("replayed create: %d waves, %v", len(replay), err)
	}
	if n := countEvents(t, admin, fulevents.WaveCreated); n != 3 {
		t.Fatalf("wave.created events = %d, want 3", n)
	}
	// Nothing is RELEASED any more, so a second run creates no wave.
	if again, err := s.CreateWaves(ctx, req, idem("waves-2")); err != nil || len(again) != 0 {
		t.Fatalf("second create: %d waves, %v", len(again), err)
	}
	got, err := s.GetWave(ctx, waves[1].ID)
	if err != nil || len(got.Shipments) != len(waves[1].Shipments) || got.LineCount != waves[1].LineCount {
		t.Fatalf("GetWave: %+v, %v", got, err)
	}
	lineSum := 0
	for _, sh := range got.Shipments {
		lineSum += sh.LineCount
	}
	if lineSum != got.LineCount {
		t.Fatalf("wave shipments carry %d lines, wave says %d", lineSum, got.LineCount)
	}
	list, err := s.ListWaves(ctx, store.WaveFilter{WarehouseID: warehouse, Limit: 10})
	if err != nil || len(list) != 3 {
		t.Fatalf("ListWaves: %d, %v", len(list), err)
	}
	// Picking a waved shipment works from PICKING.
	first := getShipment(t, s, ctx, waves[0].Shipments[0].ID)
	if picked := pickAll(t, s, ctx, first); picked.Status != domain.StatusPicked {
		t.Fatalf("pick waved shipment: %s", picked.Status)
	}
	// Another tenant and another legal entity do not see the wave.
	if _, err := s.GetWave(ctxFor(2, ""), waves[0].ID); problemCode(err) != store.CodeWaveNotFound {
		t.Fatalf("other tenant: %v, want %s", err, store.CodeWaveNotFound)
	}
	if _, err := s.GetWave(ctxFor(1, ids.New()), waves[0].ID); problemCode(err) != store.CodeWaveNotFound {
		t.Fatalf("other legal entity: %v, want %s", err, store.CodeWaveNotFound)
	}
}

func TestWorkbench_ShipmentLargerThanAWaveIsRefused(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	warehouse := ids.New()
	releaseMany(t, s, ctx, warehouse, ids.New(), []int{3}, []string{ids.New()}, func(int) string { return "" })
	_, err := s.PreviewWaves(ctx, store.WaveRequest{WarehouseID: warehouse, GroupBy: domain.GroupCustomer, Limits: domain.WaveLimits{MaxLines: 2, MaxOrders: 5}})
	if problemCode(err) != store.CodeWaveTooLarge {
		t.Fatalf("preview: %v, want %s", err, store.CodeWaveTooLarge)
	}
}

// FR-28.10: a carrier event is stored once per provider event key and published
// as tracking.updated; DELIVERED moves a SHIPPED shipment to DELIVERED.
func TestTracking_RecordOnceAndDeliver(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 4)
	packed := pickAndPack(t, s, ctx, f.shipment)
	if _, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "1")}, idem("post")); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)
	ev := store.TrackingEvent{Provider: "easypost", EventKey: "evt_1", TrackingNumber: "1Z999", Status: "IN_TRANSIT", Description: "Departed hub", EventTime: at}
	if _, created, err := s.RecordTracking(ctx, f.shipment.ID, ev); err != nil || !created {
		t.Fatalf("first event: created %v, %v", created, err)
	}
	dup := ev
	dup.Description = "changed on redelivery"
	stored, created, err := s.RecordTracking(ctx, f.shipment.ID, dup)
	if err != nil || created || stored.Description != "Departed hub" {
		t.Fatalf("redelivery: created %v stored %+v, %v", created, stored, err)
	}
	if n := countEvents(t, admin, fulevents.TrackingUpdated); n != 1 {
		t.Fatalf("tracking.updated events = %d, want 1", n)
	}
	delivered := store.TrackingEvent{Provider: "easypost", EventKey: "evt_2", TrackingNumber: "1Z999", Status: "DELIVERED", EventTime: at.Add(26 * time.Hour)}
	if _, created, err := s.RecordTracking(ctx, f.shipment.ID, delivered); err != nil || !created {
		t.Fatalf("delivered: created %v, %v", created, err)
	}
	if sh := getShipment(t, s, ctx, f.shipment.ID); sh.Status != domain.StatusDelivered {
		t.Fatalf("status after DELIVERED = %s", sh.Status)
	}
	if got := outboxData(t, admin, fulevents.TrackingUpdated, "{order_id}"); got != f.shipment.OrderID {
		t.Fatalf("event order = %q, want %s", got, f.shipment.OrderID)
	}
	events, err := s.ListTracking(ctx, f.shipment.ID)
	if err != nil || len(events) != 2 || events[0].Status != "DELIVERED" {
		t.Fatalf("ListTracking = %+v, %v; want newest (DELIVERED) first", events, err)
	}
	if _, err := s.ListTracking(ctxFor(2, ""), f.shipment.ID); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("other tenant list: %v", err)
	}
	if _, _, err := s.RecordTracking(ctxFor(2, ""), f.shipment.ID, ev); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("other tenant record: %v", err)
	}
}

// The wave.created payload carries the shipments of the wave.
func TestWaveCreatedPayload(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	warehouse := ids.New()
	shipments := releaseMany(t, s, ctx, warehouse, ids.New(), []int{2}, []string{ids.New()}, func(int) string { return "" })
	waves, err := s.CreateWaves(ctx, store.WaveRequest{WarehouseID: warehouse, GroupBy: domain.GroupCustomer,
		Limits: domain.WaveLimits{MaxLines: 200, MaxOrders: 50}}, idem("w"))
	if err != nil || len(waves) != 1 {
		t.Fatalf("CreateWaves: %v", err)
	}
	raw := outboxData(t, admin, fulevents.WaveCreated, "{shipment_ids}")
	var got []string
	if err := json.Unmarshal([]byte(raw), &got); err != nil || len(got) != 1 || got[0] != shipments[0].ID {
		t.Fatalf("shipment_ids = %s, want [%s]", raw, shipments[0].ID)
	}
	if aggregate := outboxAggregate(t, admin, fulevents.WaveCreated); aggregate != "wave" {
		t.Fatalf("aggregate type = %q, want wave", aggregate)
	}
}

func outboxAggregate(t *testing.T, admin *pgxpool.Pool, eventType string) string {
	t.Helper()
	var v string
	if err := admin.QueryRow(context.Background(), `select aggregate_type from outbox where type = $1 limit 1`, eventType).Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// FR-28.8 CANCEL_REMAINDER: a partially shipped shipment is closed at what
// shipped; the lines shrink, the shipment is SHIPPED, the backorder closes and
// shipment.closed carries the closed and picked-but-unpacked quantities.
func TestCloseRemainder(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	f := released(t, s, ctx, 10, 5)
	l1, l2 := f.shipment.Lines[0], f.shipment.Lines[1]

	// Nothing shipped yet: closing is refused, the shipment is cancelled instead.
	if _, err := s.CloseRemainder(ctx, f.shipment.ID, f.shipment.Version, idem("early")); problemCode(err) != store.CodeInvalidMove {
		t.Fatalf("close before shipping: %v, want %s", err, store.CodeInvalidMove)
	}
	picked, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, []store.PickInput{{ShipmentLineID: l1.ID, Picked: d("6")}, {ShipmentLineID: l2.ID, Picked: d("5")}}, idem("p1"))
	if err != nil {
		t.Fatal(err)
	}
	packed, err := s.PackContainer(ctx, f.shipment.ID, picked.Version, "BOX", []store.PackInput{{ShipmentLineID: l1.ID, Qty: d("6")}, {ShipmentLineID: l2.ID, Qty: d("5")}}, idem("k1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PostPackingSlip(ctx, f.shipment.ID, packed.Shipment.Version, store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(f.shipment, "1")}, idem("s1")); err != nil {
		t.Fatal(err)
	}
	partial := getShipment(t, s, ctx, f.shipment.ID)
	if partial.Status != domain.StatusPartiallyShipped {
		t.Fatalf("status = %s, want PARTIALLY_SHIPPED", partial.Status)
	}
	// The remainder is re-picked 2 of 4 and not packed.
	repicked, err := s.ConfirmPick(ctx, f.shipment.ID, partial.Version, []store.PickInput{{ShipmentLineID: l1.ID, Picked: d("8")}}, idem("p2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseRemainder(ctx, f.shipment.ID, repicked.Version+5, idem("stale")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale close: %v, want %s", err, store.CodeVersion)
	}
	// Another tenant, and a principal scoped to another legal entity, do not see it.
	if _, err := s.CloseRemainder(ctxFor(2, ""), f.shipment.ID, repicked.Version, idem("other-tenant")); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("other tenant close: %v, want %s", err, store.CodeShipmentNotFound)
	}
	if _, err := s.CloseRemainder(ctxFor(1, ids.New()), f.shipment.ID, repicked.Version, idem("other-entity")); problemCode(err) != store.CodeShipmentNotFound {
		t.Fatalf("other legal entity close: %v, want %s", err, store.CodeShipmentNotFound)
	}
	if n := countEvents(t, admin, fulevents.ShipmentClosed); n != 0 {
		t.Fatalf("refused closes wrote %d shipment.closed events", n)
	}
	key := idem("close")
	closed, err := s.CloseRemainder(ctx, f.shipment.ID, repicked.Version, key)
	if err != nil || closed.Status != domain.StatusShipped {
		t.Fatalf("close: %v status %v", err, closed.Status)
	}
	if replay, err := s.CloseRemainder(ctx, f.shipment.ID, repicked.Version, key); err != nil || replay.Version != closed.Version {
		t.Fatalf("replayed close: %v", err)
	}
	got := getShipment(t, s, ctx, f.shipment.ID)
	g1, g2 := got.Lines[0], got.Lines[1]
	if !g1.Planned.Equal(d("6")) || !g1.Picked.Equal(d("6")) || !g1.Closed.Equal(d("4")) || !g2.Closed.IsZero() || !g2.Planned.Equal(d("5")) {
		t.Fatalf("lines after close: l1 %+v l2 %+v", g1, g2)
	}
	if n := countEvents(t, admin, fulevents.ShipmentClosed); n != 1 {
		t.Fatalf("shipment.closed events = %d, want 1", n)
	}
	if q := outboxData(t, admin, fulevents.ShipmentClosed, "{lines,0,qty}"); q != "4" {
		t.Fatalf("event closed qty = %q, want 4", q)
	}
	if q := outboxData(t, admin, fulevents.ShipmentClosed, "{lines,0,picked_unpacked}"); q != "2" {
		t.Fatalf("event picked_unpacked qty = %q, want 2", q)
	}
	// The short-picked line leaves the open backorders and keeps the closed quantity in the report.
	for _, r := range backorders(t, s, ctx, store.BackorderFilter{WarehouseID: f.warehouse}) {
		if r.ShipmentLineID == l1.ID {
			t.Fatalf("closed line still an open backorder: %+v", r)
		}
	}
	all := backorders(t, s, ctx, store.BackorderFilter{WarehouseID: f.warehouse, IncludeClosed: true})
	if len(all) != 1 || all[0].ShipmentLineID != l1.ID || !all[0].Closed.Equal(d("4")) || !all[0].Planned.Equal(d("6")) || all[0].Status != domain.StatusShipped {
		t.Fatalf("closed backorders = %+v, want l1 closed 4 planned 6 SHIPPED", all)
	}
	// A closed shipment has no remainder left to close.
	if _, err := s.CloseRemainder(ctx, f.shipment.ID, closed.Version, idem("twice")); problemCode(err) != store.CodeInvalidMove {
		t.Fatalf("close twice: %v, want %s", err, store.CodeInvalidMove)
	}
}
