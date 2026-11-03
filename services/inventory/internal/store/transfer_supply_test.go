package store_test

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// buckets returns the ON_ORDER and ORDERED quantities of a product at one warehouse (and site).
func buckets(t *testing.T, admin *pgxpool.Pool, product, warehouse, site string) (string, string) {
	t.Helper()
	var onOrder, ordered string
	if err := admin.QueryRow(context.Background(), `select coalesce(sum(o.on_order), 0)::text, coalesce(sum(o.ordered), 0)::text
		from on_hand o join dimension_set d on d.tenant_id = o.tenant_id and d.hash = o.dimension_set_hash
		where o.product_id = $1 and d.dims->>'warehouse' = $2 and d.dims->>'site' = $3`, product, warehouse, site).Scan(&onOrder, &ordered); err != nil {
		t.Fatal(err)
	}
	return onOrder, ordered
}

func wantBuckets(t *testing.T, admin *pgxpool.Pool, product, warehouse, site, onOrder, ordered string) {
	t.Helper()
	oo, od := buckets(t, admin, product, warehouse, site)
	if !dec(oo).Equal(dec(onOrder)) || !dec(od).Equal(dec(ordered)) {
		t.Fatalf("%s/%s: on_order %s ordered %s, want %s and %s", warehouse, site, oo, od, onOrder, ordered)
	}
}

func rebuildClean(t *testing.T, s *store.Postgres, ctx context.Context) {
	t.Helper()
	rep, err := s.RebuildOnHand(ctx, false)
	if err != nil || rep.DiffCount != 0 {
		t.Fatalf("rebuild: %v %+v", err, rep)
	}
}

func eventCount(t *testing.T, admin *pgxpool.Pool, typ, aggregate string) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = $1 and aggregate_id = $2`, typ, aggregate).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const transferChangedType = "dev.passage.inventory.transfer.changed.v1"

// Creating a CREATED order books demand at the shipping warehouse and the expected receipt at the
// receiving one and announces the order; a draft books and announces nothing until confirmed.
func TestTransferBooksOnOrderAndOrderedAcrossItsLife(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))

	o, _, err := s.CreateTransfer(ctx, "sup-1", transferOrder(ctx, p, "40", true))
	if err != nil {
		t.Fatal(err)
	}
	if eventCount(t, admin, transferChangedType, o.ID) != 1 {
		t.Fatal("a direct create must publish transfer.changed")
	}
	if _, replayed, err := s.CreateTransfer(ctx, "sup-1", transferOrder(ctx, p, "40", true)); err != nil || !replayed ||
		eventCount(t, admin, transferChangedType, o.ID) != 1 {
		t.Fatalf("replay must not announce or book again: %v %v", err, replayed)
	}
	wantBuckets(t, admin, p, "W1", "S1", "40", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "40")
	rebuildClean(t, s, ctx)
	// The on-hand inquiry sees the demand: 100 physical less 40 on order.
	res, err := s.ListOnHand(ctx, store.OnHandFilter{ProductID: p, WarehouseID: "W1", GroupBy: []string{"product"}})
	if err != nil || !res.Rows[0].AvailOrdered.Equal(dec("60")) {
		t.Fatalf("avail ordered %v %v", err, res.Rows)
	}

	// An edit moves both legs: a smaller quantity, and a new receiving site moves the ORDERED receipt.
	q, site := dec("30"), "S2"
	e, err := s.EditTransfer(ctx, o.ID, o.Version, domain.TransferEdit{ToSite: &site, Update: []domain.LineEdit{{LineID: o.Lines[0].ID, Qty: &q}}})
	if err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "30", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "0")
	wantBuckets(t, admin, p, "W2", "S2", "0", "30")
	// A removed line releases its pair; an added line books one.
	p2 := ids.New()
	e, err = s.EditTransfer(ctx, o.ID, e.Version, domain.TransferEdit{Remove: []string{e.Lines[0].ID},
		Add: []domain.TransferLine{{ProductID: p2, Dims: dims(), Unit: "EA", Qty: dec("7")}}})
	if err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "0", "0")
	wantBuckets(t, admin, p, "W2", "S2", "0", "0")
	wantBuckets(t, admin, p2, "W1", "S1", "7", "0")
	wantBuckets(t, admin, p2, "W2", "S2", "0", "7")
	rebuildClean(t, s, ctx)

	// Cancelling releases everything.
	if _, err := s.CancelTransfer(ctx, o.ID, e.Version); err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p2, "W1", "S1", "0", "0")
	wantBuckets(t, admin, p2, "W2", "S2", "0", "0")
	rebuildClean(t, s, ctx)

	// A draft books nothing; confirming books the pair; deleting the CREATED order releases it.
	in := transferOrder(ctx, p, "10", false)
	in.Status = domain.TransferDraft
	d, _, err := s.CreateTransfer(ctx, "sup-draft", in)
	if err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "0", "0")
	c, err := s.ConfirmTransfer(ctx, d.ID, d.Version)
	if err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "10", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "10")
	if err := s.DeleteTransfer(ctx, c.ID, c.Version); err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "0", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "0")
	rebuildClean(t, s, ctx)
}

// Shipping releases the demand for what shipped, receiving the expected receipt for what arrived,
// so shipping all the available stock is not refused for counting the quantity twice.
func TestTransferSupplyFollowsShipAndReceive(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))
	o, _, err := s.CreateTransfer(ctx, "sup-ship", transferOrder(ctx, p, "100", true))
	if err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "100", "0")
	shipped, err := s.ShipTransfer(ctx, o.ID, o.Version, []store.TransferShipment{{LineID: o.Lines[0].ID, Qty: dec("60")}}, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatalf("ship 60 of 100 with exactly 100 available: %v", err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "40", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "100")
	rebuildClean(t, s, ctx)
	shipped, err = s.ShipTransfer(ctx, o.ID, shipped.Version, nil, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatalf("ship the rest: %v", err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "0", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "100")
	rec, err := s.ReceiveTransfer(ctx, o.ID, shipped.Version, []store.TransferReceipt{{LineID: o.Lines[0].ID, Qty: dec("70")}}, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W2", "S1", "0", "30")
	rebuildClean(t, s, ctx)
	// Closing the line short expects nothing more.
	rec, err = s.ReceiveTransfer(ctx, o.ID, rec.Version, []store.TransferReceipt{{LineID: o.Lines[0].ID, Qty: dec("25"), CloseShort: true}}, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W2", "S1", "0", "0")
	rebuildClean(t, s, ctx)
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from transfer_supply where transfer_order_id = $1`, rec.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("supply rows left on a finished order: %d (%v)", n, err)
	}

	// One-step transfers (no transit) leave nothing booked once shipped.
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "10", "2", dims()))
	o2, _, err := s.CreateTransfer(ctx, "sup-onestep", transferOrder(ctx, p, "5", false))
	if err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W2", "S1", "0", "5")
	if _, err := s.ShipTransfer(ctx, o2.ID, o2.Version, nil, shipDay, domain.NegativePolicy{}); err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W2", "S1", "0", "0")
	rebuildClean(t, s, ctx)
}

// The rebuild-and-compare finds and repairs a drifted ON_ORDER / ORDERED bucket.
func TestRebuildRepairsDriftedTransferSupply(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	if _, _, err := s.CreateTransfer(ctx, "sup-drift", transferOrder(ctx, p, "9", true)); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `update on_hand set on_order = 1, ordered = 2 where product_id = $1`, p); err != nil {
		t.Fatal(err)
	}
	rep, err := s.RebuildOnHand(ctx, true)
	if err != nil || rep.DiffCount != 2 {
		t.Fatalf("rebuild: %v %+v", err, rep)
	}
	wantBuckets(t, admin, p, "W1", "S1", "9", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "9")
	rebuildClean(t, s, ctx)
}

// Decimal quantities through a partial ship, a partial receipt, the rest shipped and a short close:
// each step leaves exactly ordered less shipped / received booked, and the finished order leaves
// both buckets at exactly zero and no transfer_supply row.
func TestTransferSupplyDecimalPartialsReturnToZero(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "20", "2", dims()))
	o, _, err := s.CreateTransfer(ctx, "sup-dec", transferOrder(ctx, p, "12.345", true))
	if err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "12.345", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "12.345")
	o, err = s.ShipTransfer(ctx, o.ID, o.Version, []store.TransferShipment{{LineID: o.Lines[0].ID, Qty: dec("4.5")}}, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "7.845", "0")
	o, err = s.ReceiveTransfer(ctx, o.ID, o.Version, []store.TransferReceipt{{LineID: o.Lines[0].ID, Qty: dec("4.5")}}, shipDay, domain.NegativePolicy{})
	if err != nil || o.Status != domain.TransferPartial {
		t.Fatalf("partial receipt: %v %s", err, o.Status)
	}
	wantBuckets(t, admin, p, "W1", "S1", "7.845", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "7.845")
	rebuildClean(t, s, ctx)
	o, err = s.ShipTransfer(ctx, o.ID, o.Version, nil, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "0", "0")
	// 7.8 of 7.845 and closed short: 0.045 is within the 10 % underdelivery tolerance.
	o, err = s.ReceiveTransfer(ctx, o.ID, o.Version, []store.TransferReceipt{{LineID: o.Lines[0].ID, Qty: dec("7.8"), CloseShort: true}}, shipDay, domain.NegativePolicy{})
	if err != nil || o.Status != domain.TransferReceived {
		t.Fatalf("closing receipt: %v %s", err, o.Status)
	}
	wantBuckets(t, admin, p, "W1", "S1", "0", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "0")
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from transfer_supply where transfer_order_id = $1`, o.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("supply rows left: %d (%v)", n, err)
	}
	rebuildClean(t, s, ctx)
}

// Every onhand.changed of a receipt carries the row's final availability: releasing the expected
// receipt and booking the arrived stock happen in one transaction, so no event may show the state
// in between (here availOrdered -50 at the receiving warehouse, where another order's demand waits).
func TestTransferSupplyEventsCarryFinalAvailability(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))
	o, _, err := s.CreateTransfer(ctx, "sup-ev-1", transferOrder(ctx, p, "100", true))
	if err != nil {
		t.Fatal(err)
	}
	back := transferOrder(ctx, p, "50", false)
	back.FromWarehouse, back.ToWarehouse = "W2", "W1"
	back.Lines[0].Dims = domain.Dimensions{domain.DimSite: "S1", domain.DimWarehouse: "W2"}
	if _, _, err := s.CreateTransfer(ctx, "sup-ev-2", back); err != nil {
		t.Fatal(err)
	}
	if o, err = s.ShipTransfer(ctx, o.ID, o.Version, nil, shipDay, domain.NegativePolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReceiveTransfer(ctx, o.ID, o.Version, []store.TransferReceipt{{LineID: o.Lines[0].ID, Qty: dec("100")}}, shipDay, domain.NegativePolicy{}); err != nil {
		t.Fatal(err)
	}
	rows, err := admin.Query(context.Background(), `select payload->'data'->>'deltaBucket', payload->'data'->>'deltaQty', payload->'data'->>'availOrdered'
		from outbox where type = 'dev.passage.inventory.onhand.changed.v1' and payload->'data'->>'productId' = $1
		  and payload->'data'->'dimensionSet'->>'warehouse' = 'W2'`, p)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	released := ""
	for rows.Next() {
		var bucket, qty, avail string
		if err := rows.Scan(&bucket, &qty, &avail); err != nil {
			t.Fatal(err)
		}
		if dec(avail).IsNegative() {
			t.Fatalf("onhand.changed %s %s at W2 shows availOrdered %s", bucket, qty, avail)
		}
		if bucket == "ORDERED" && dec(qty).Equal(dec("-100")) {
			released = avail
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	// The receipt's ORDERED release shows 100 received less 50 on order, not the -50 in between.
	if released == "" || !dec(released).Equal(dec("50")) {
		t.Fatalf("receipt ORDERED event at W2: availOrdered %q, want 50", released)
	}
}

// A ship takes its projection row locks in one ascending pass (BR-16.4), the supply rows with the
// stock-move rows. Here the transit row sorts before the shipping row; another transaction holds
// the transit row and then asks for the shipping row. A ship that locked its shipping row for the
// ON_ORDER release before the transit row for the move would close a cycle (40P01); in key order it
// waits on the transit row without holding the shipping row.
func TestTransferShipLocksSupplyAndMoveRowsInOneOrder(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	src, err := domain.WithWarehouse(dims(), "W1").Normalize()
	if err != nil {
		t.Fatal(err)
	}
	transit := ""
	for i := 1; i < 64 && transit == ""; i++ {
		n, err := domain.WithWarehouse(dims(), fmt.Sprintf("T%d", i)).Normalize()
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Compare(n.Hash(), src.Hash()) < 0 {
			transit = fmt.Sprintf("T%d", i)
		}
	}
	if transit == "" {
		t.Fatal("no transit warehouse code sorts before W1")
	}
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "10", "2", dims()), line(p, "1", "2", dims(domain.DimWarehouse, transit)))
	in := transferOrder(ctx, p, "4", true)
	in.TransitWarehouse = transit
	o, _, err := s.CreateTransfer(ctx, "sup-lock", in)
	if err != nil {
		t.Fatal(err)
	}

	bg := context.Background()
	other, err := admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Rollback(bg)
	lockAt := func(warehouse string) error {
		_, err := other.Exec(bg, `select 1 from on_hand o join dimension_set d on d.tenant_id = o.tenant_id and d.hash = o.dimension_set_hash
			where o.product_id = $1 and d.dims->>'warehouse' = $2 for update of o`, p, warehouse)
		return err
	}
	if _, err := other.Exec(bg, `set local lock_timeout = '3s'`); err != nil {
		t.Fatal(err)
	}
	if err := lockAt(transit); err != nil {
		t.Fatal(err)
	}
	var pid int
	if err := other.QueryRow(bg, `select pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.ShipTransfer(ctx, o.ID, o.Version, nil, shipDay, domain.NegativePolicy{})
		done <- err
	}()
	for waited := 0; ; waited++ {
		var n int
		if err := admin.QueryRow(bg, `select count(*) from pg_stat_activity where $1 = any(pg_blocking_pids(pid))`, pid).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			break
		}
		if waited == 100 {
			t.Fatal("the ship never waited on the transit row")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := lockAt("W1"); err != nil {
		t.Fatalf("the waiting ship holds the shipping row: %v", err)
	}
	if err := other.Commit(bg); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("ship: %v", err)
	}
	wantBuckets(t, admin, p, "W1", "S1", "0", "0")
	rebuildClean(t, s, ctx)
}
