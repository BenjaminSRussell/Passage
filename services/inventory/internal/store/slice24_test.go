package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// A transfer can always ship against its own booked demand: the open quantity of the order itself is
// left out of the FR-16.4 financial-negative check, while another order's demand still counts.
func TestTransferShipExcludesItsOwnOnOrder(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "6", "2", dims()))
	// 10 planned, only 6 on hand: shipping the 6 leaves 4 open on order and 0 on hand.
	a, _, err := s.CreateTransfer(ctx, "own-a", transferOrder(ctx, p, "10", true))
	if err != nil {
		t.Fatal(err)
	}
	a, err = s.ShipTransfer(ctx, a.ID, a.Version, []store.TransferShipment{{LineID: a.Lines[0].ID, Qty: dec("6")}}, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatalf("a transfer must ship against its own booked demand: %v", err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "4", "0")
	rebuildClean(t, s, ctx)

	// Another order's ship still sees that demand: 3 on hand, 4 (order a) + 3 (itself) on order.
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "3", "2", dims()))
	b, _, err := s.CreateTransfer(ctx, "own-b", transferOrder(ctx, p, "3", true))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.ShipTransfer(ctx, b.ID, b.Version, nil, shipDay, domain.NegativePolicy{})
	if problemCode(err) != domain.CodeNegativeFinancial {
		t.Fatalf("order b ships against order a's demand: %v, want %s", err, domain.CodeNegativeFinancial)
	}
	// The physical negative check still applies to the order itself.
	c, _, err := s.CreateTransfer(ctx, "own-c", transferOrder(ctx, ids.New(), "5", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ShipTransfer(ctx, c.ID, c.Version, nil, shipDay, domain.NegativePolicy{}); problemCode(err) == "" {
		t.Fatal("shipping stock that is not there must still be refused")
	}
}

// The backfill books the pair for orders that were open before the supply existed, once.
func TestBackfillBooksTransferSupplyOfOrdersOpenBeforeIt(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))
	o, _, err := s.CreateTransfer(ctx, "bf-1", transferOrder(ctx, p, "40", true))
	if err != nil {
		t.Fatal(err)
	}
	o, err = s.ShipTransfer(ctx, o.ID, o.Version, []store.TransferShipment{{LineID: o.Lines[0].ID, Qty: dec("15")}}, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	done, _, err := s.CreateTransfer(ctx, "bf-2", transferOrder(ctx, p, "5", true))
	if err != nil {
		t.Fatal(err)
	}
	// A draft and a cancelled order hold nothing and must stay that way.
	if _, err := s.CancelTransfer(ctx, done.ID, done.Version); err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "25", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "40")

	// Put the books back as they were before 0025: no supply rows, no demand, no expected receipt.
	if _, err := admin.Exec(context.Background(), `delete from transfer_supply`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `update on_hand set on_order = 0, ordered = 0 where product_id = $1`, p); err != nil {
		t.Fatal(err)
	}
	rep, err := s.BackfillTransferSupply(ctx, "", 0)
	if err != nil || rep.Checked != 1 || rep.Booked != 1 || rep.More || rep.Next != "" {
		t.Fatalf("backfill: %v %+v", err, rep)
	}
	wantBuckets(t, admin, p, "W1", "S1", "25", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "40")
	rebuildClean(t, s, ctx)
	rep, err = s.BackfillTransferSupply(ctx, "", 0)
	if err != nil || rep.Checked != 0 || rep.Booked != 0 {
		t.Fatalf("a re-run books nothing: %v %+v", err, rep)
	}
	wantBuckets(t, admin, p, "W1", "S1", "25", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "40")
	// The order is whole again: receiving releases the expected receipt as for any order.
	if _, err := s.ReceiveTransfer(ctx, o.ID, o.Version, []store.TransferReceipt{{LineID: o.Lines[0].ID, Qty: dec("15")}}, shipDay, domain.NegativePolicy{}); err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W2", "S1", "0", "25")
	rebuildClean(t, s, ctx)
}

func reserveOrdered(ctx context.Context, s *store.Postgres, product, qty string, policy domain.ReservationPolicy, d domain.Dimensions, key string) (store.ReserveResult, error) {
	return s.Reserve(ctx, store.ReserveInput{LegalEntityID: legalEntity(ctx), ProductID: product, Qty: dec(qty), DemandType: domain.DemandSalesOrder,
		DemandID: "SO-7", Policy: policy, Dims: d, RequiredDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), IdempotencyKey: key, Kind: domain.ReserveKindOrdered})
}

func reservedOrderedAt(t *testing.T, admin *pgxpool.Pool, product string) string {
	t.Helper()
	var q string
	if err := admin.QueryRow(context.Background(), `select coalesce(sum(reserved_ordered), 0)::text from on_hand where product_id = $1`, product).Scan(&q); err != nil {
		t.Fatal(err)
	}
	return q
}

// A MANUAL reservation can hold part of the expected receipt of one dimension set: it moves
// reserved_ordered (not reserved_physical), cannot exceed what is expected and unreserved, replays by
// key, releases back, and the rebuild reproduces it.
func TestReservationAgainstExpectedReceipt(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))
	if _, _, err := s.CreateTransfer(ctx, "ro-1", transferOrder(ctx, p, "40", true)); err != nil {
		t.Fatal(err)
	}
	w2 := dims("warehouse", "W2")
	r, err := reserveOrdered(ctx, s, p, "25", domain.PolicyManual, w2, "ro-a")
	if err != nil || len(r.Reservations) != 1 || r.Reservations[0].Kind != domain.ReserveKindOrdered {
		t.Fatalf("reserve against the expected receipt: %v %+v", err, r)
	}
	if got := reservedOrderedAt(t, admin, p); !dec(got).Equal(dec("25")) {
		t.Fatalf("reserved_ordered %s, want 25", got)
	}
	var phys string
	if err := admin.QueryRow(context.Background(), `select coalesce(sum(reserved_physical), 0)::text from on_hand where product_id = $1`, p).Scan(&phys); err != nil || !dec(phys).IsZero() {
		t.Fatalf("reserved_physical %s (%v), want 0", phys, err)
	}
	// The inquiry shows it at the receiving warehouse (the expected receipt has no currency yet, so it is its own group).
	inq, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: p, WarehouseID: "W2"})
	if err != nil || len(inq.Rows) != 1 || !inq.Rows[0].ReservedOrdered.Equal(dec("25")) || !inq.Rows[0].ReservedPhysical.IsZero() ||
		!inq.Rows[0].AvailOrdered.Equal(dec("15")) {
		t.Fatalf("on-hand inquiry: %v %+v", err, inq.Rows)
	}
	rebuildClean(t, s, ctx)
	// A repeated key replays; a larger request than what is left is INV-RES-001; physical stock is not
	// an expected receipt (W1 expects nothing).
	if again, err := reserveOrdered(ctx, s, p, "25", domain.PolicyManual, w2, "ro-a"); err != nil || !again.Replayed {
		t.Fatalf("replay: %v %+v", err, again)
	}
	if _, err := reserveOrdered(ctx, s, p, "16", domain.PolicyManual, w2, "ro-b"); problemCode(err) != domain.CodeReserveShortfall {
		t.Fatalf("more than the unreserved expected receipt: %v", err)
	}
	if _, err := reserveOrdered(ctx, s, p, "1", domain.PolicyManual, dims(), "ro-c"); problemCode(err) != domain.CodeReserveShortfall {
		t.Fatalf("a row that expects nothing: %v", err)
	}
	if _, err := reserveOrdered(ctx, s, p, "1", domain.PolicyFIFO, w2, "ro-d"); problemCode(err) != domain.CodeInvalid {
		t.Fatalf("policy request against expected receipts: %v", err)
	}
	if _, err := reserveOrdered(ctx, s, p, "15", domain.PolicyManual, w2, "ro-e"); err != nil {
		t.Fatalf("the rest of the expected receipt: %v", err)
	}
	rebuildClean(t, s, ctx)
	// The reserved quantity of an expected receipt does not take physical availability.
	listed, err := s.ListReservations(ctx, store.ReservationFilter{ProductID: p})
	if err != nil || len(listed) != 2 || listed[0].Kind != domain.ReserveKindOrdered {
		t.Fatalf("list: %v %+v", err, listed)
	}
	for _, res := range listed {
		if _, err := s.ReleaseReservation(ctx, res.ID); err != nil {
			t.Fatal(err)
		}
	}
	if got := reservedOrderedAt(t, admin, p); !dec(got).IsZero() {
		t.Fatalf("reserved_ordered %s after release, want 0", got)
	}
	rebuildClean(t, s, ctx)
}

// The backfill works through a tenant in bounded batches by order id, and never reaches another
// tenant's orders.
func TestBackfillPagesByOrderIDWithinTheTenant(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))
	for _, k := range []string{"pg-1", "pg-2", "pg-3"} {
		if _, _, err := s.CreateTransfer(ctx, k, transferOrder(ctx, p, "5", true)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := admin.Exec(context.Background(), `delete from transfer_supply`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `update on_hand set on_order = 0, ordered = 0 where product_id = $1`, p); err != nil {
		t.Fatal(err)
	}
	if rep, err := s.BackfillTransferSupply(entityCtx(2, ids.New()), "", 0); err != nil || rep.Checked != 0 {
		t.Fatalf("another tenant sees these orders: %v %+v", err, rep)
	}
	first, err := s.BackfillTransferSupply(ctx, "", 2)
	if err != nil || first.Checked != 2 || first.Booked != 2 || !first.More || first.Next == "" {
		t.Fatalf("first batch: %v %+v", err, first)
	}
	rest, err := s.BackfillTransferSupply(ctx, first.Next, 2)
	if err != nil || rest.Checked != 1 || rest.Booked != 1 || rest.More || rest.Next != "" {
		t.Fatalf("second batch: %v %+v", err, rest)
	}
	wantBuckets(t, admin, p, "W1", "S1", "15", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "15")
	rebuildClean(t, s, ctx)
}

// An ORDERED reservation that lapses gives its quantity back to reserved_ordered through the expiry run.
func TestExpiredReservationAgainstExpectedReceiptGivesItBack(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))
	if _, _, err := s.CreateTransfer(ctx, "ex-1", transferOrder(ctx, p, "40", true)); err != nil {
		t.Fatal(err)
	}
	r, err := s.Reserve(ctx, store.ReserveInput{LegalEntityID: legalEntity(ctx), ProductID: p, Qty: dec("12.5"), DemandType: domain.DemandSalesOrder,
		DemandID: "SO-8", Policy: domain.PolicyManual, Dims: dims("warehouse", "W2"), RequiredDate: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		IdempotencyKey: "ex-r", Kind: domain.ReserveKindOrdered, TTLHours: 1})
	if err != nil || len(r.Reservations) != 1 {
		t.Fatalf("reserve: %v %+v", err, r)
	}
	if got := reservedOrderedAt(t, admin, p); !dec(got).Equal(dec("12.5")) {
		t.Fatalf("reserved_ordered %s, want 12.5", got)
	}
	if _, err := admin.Exec(context.Background(), `update inventory_reservation set expires_at = now() - interval '1 minute' where id = $1`, r.Reservations[0].ID); err != nil {
		t.Fatal(err)
	}
	out, err := s.ExpireReservations(ctx, 0)
	if err != nil || len(out.Released) != 1 || out.Released[0].Kind != domain.ReserveKindOrdered {
		t.Fatalf("expire: %v %+v", err, out)
	}
	if got := reservedOrderedAt(t, admin, p); !dec(got).IsZero() {
		t.Fatalf("reserved_ordered %s after expiry, want 0", got)
	}
	var given int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = 'dev.passage.inventory.onhand.changed.v1'
		and payload->'data'->>'deltaBucket' = 'RESERVED_ORDERED' and payload->'data'->>'deltaQty' = '-12.5'`).Scan(&given); err != nil || given != 1 {
		t.Fatalf("onhand.changed giving back RESERVED_ORDERED: %d (%v), want 1", given, err)
	}
	rebuildClean(t, s, ctx)
}
