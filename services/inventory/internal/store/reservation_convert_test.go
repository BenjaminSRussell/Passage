package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

const reservationConvertedType = "dev.passage.inventory.reservation.converted.v1"

// reservedBuckets reads reserved_physical and reserved_ordered of a product at one warehouse.
func reservedBuckets(t *testing.T, admin *pgxpool.Pool, product, warehouse string) (string, string) {
	t.Helper()
	var phys, ord string
	if err := admin.QueryRow(context.Background(), `select coalesce(sum(o.reserved_physical), 0)::text, coalesce(sum(o.reserved_ordered), 0)::text
		from on_hand o join dimension_set d on d.tenant_id = o.tenant_id and d.hash = o.dimension_set_hash
		where o.product_id = $1 and d.dims->>'warehouse' = $2`, product, warehouse).Scan(&phys, &ord); err != nil {
		t.Fatal(err)
	}
	return phys, ord
}

func wantReserved(t *testing.T, admin *pgxpool.Pool, product, warehouse, physical, ordered string) {
	t.Helper()
	p, o := reservedBuckets(t, admin, product, warehouse)
	if !dec(p).Equal(dec(physical)) || !dec(o).Equal(dec(ordered)) {
		t.Fatalf("%s: reserved physical %s ordered %s, want %s and %s", warehouse, p, o, physical, ordered)
	}
}

// reservationQtyByKind sums the ACTIVE reservations of a product by kind.
func reservationQtyByKind(t *testing.T, admin *pgxpool.Pool, product, kind string) string {
	t.Helper()
	var q string
	if err := admin.QueryRow(context.Background(), `select coalesce(sum(qty), 0)::text from inventory_reservation
		where product_id = $1 and status = 'ACTIVE' and kind = $2`, product, kind).Scan(&q); err != nil {
		t.Fatal(err)
	}
	return q
}

func convertedEvents(t *testing.T, admin *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, reservationConvertedType).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// Receiving a transfer converts the reservations made against its receipt, oldest first: one that
// arrived whole changes kind, one that arrived in part is split, and the rest stays ORDERED until the
// rest arrives. reserved_ordered falls and reserved_physical rises by the converted quantity, the
// rebuild agrees, and a replayed reserve request still answers as the original.
func TestTransferReceiptConvertsReservationsAgainstIt(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))
	o, _, err := s.CreateTransfer(ctx, "conv-1", transferOrder(ctx, p, "40", true))
	if err != nil {
		t.Fatal(err)
	}
	w2 := dims("warehouse", "W2")
	if _, err := reserveOrdered(ctx, s, p, "25", domain.PolicyManual, w2, "conv-a"); err != nil {
		t.Fatal(err)
	}
	if _, err := reserveOrdered(ctx, s, p, "10", domain.PolicyManual, w2, "conv-b"); err != nil {
		t.Fatal(err)
	}
	wantReserved(t, admin, p, "W2", "0", "35")
	o, err = s.ShipTransfer(ctx, o.ID, o.Version, nil, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	wantReserved(t, admin, p, "W2", "0", "35") // shipped, not yet arrived

	o, err = s.ReceiveTransfer(ctx, o.ID, o.Version, []store.TransferReceipt{{LineID: o.Lines[0].ID, Qty: dec("30")}}, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	wantReserved(t, admin, p, "W2", "30", "5")
	if got := reservationQtyByKind(t, admin, p, "PHYSICAL"); !dec(got).Equal(dec("30")) {
		t.Fatalf("physical reservations %s, want 30 (25 whole and 5 split off)", got)
	}
	if got := reservationQtyByKind(t, admin, p, "ORDERED"); !dec(got).Equal(dec("5")) {
		t.Fatalf("ordered reservations %s, want 5 left", got)
	}
	if n := convertedEvents(t, admin); n != 2 {
		t.Fatalf("reservation.converted events %d, want 2", n)
	}
	rebuildClean(t, s, ctx)

	// A retried request for the converted reservation is a replay, not a different request.
	r, err := reserveOrdered(ctx, s, p, "25", domain.PolicyManual, w2, "conv-a")
	if err != nil || !r.Replayed || len(r.Reservations) != 1 {
		t.Fatalf("replay after conversion: %v %+v", err, r)
	}
	// The split reservation's request replays with both allocations and no shortfall.
	r, err = reserveOrdered(ctx, s, p, "10", domain.PolicyManual, w2, "conv-b")
	if err != nil || !r.Replayed || len(r.Reservations) != 2 || !r.Shortfall.IsZero() {
		t.Fatalf("replay of the split request: %v %+v", err, r)
	}

	// The rest arrives: the remainder converts and nothing stays against the receipt.
	if _, err := s.ReceiveTransfer(ctx, o.ID, o.Version, []store.TransferReceipt{{LineID: o.Lines[0].ID, Qty: dec("10")}}, shipDay, domain.NegativePolicy{}); err != nil {
		t.Fatal(err)
	}
	wantReserved(t, admin, p, "W2", "35", "0")
	if n := convertedEvents(t, admin); n != 3 {
		t.Fatalf("reservation.converted events %d, want 3", n)
	}
	rebuildClean(t, s, ctx)

	// A converted reservation is an ordinary physical one: releasing it gives the stock back.
	rs, err := s.ListReservations(ctx, store.ReservationFilter{ProductID: p})
	if err != nil || len(rs) != 3 {
		t.Fatalf("list: %v %d", err, len(rs))
	}
	for _, r := range rs {
		if r.Kind != domain.ReserveKindPhysical || r.ConvertedAt == nil {
			t.Fatalf("reservation %s kind %s converted %v", r.ID, r.Kind, r.ConvertedAt)
		}
		if _, err := s.ReleaseReservation(ctx, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	wantReserved(t, admin, p, "W2", "0", "0")
	rebuildClean(t, s, ctx)
}

// Reservations on a different dimension set than the receiving one are not touched, and a receipt
// larger than the reserved quantity converts only what is reserved.
func TestReceiptConvertsOnlyReservedQuantityAtTheReceivingSet(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))
	o, _, err := s.CreateTransfer(ctx, "conv-2", transferOrder(ctx, p, "40", true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reserveOrdered(ctx, s, p, "4", domain.PolicyManual, dims("warehouse", "W2"), "conv-c"); err != nil {
		t.Fatal(err)
	}
	o, err = s.ShipTransfer(ctx, o.ID, o.Version, nil, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReceiveTransfer(ctx, o.ID, o.Version, []store.TransferReceipt{{LineID: o.Lines[0].ID, Qty: dec("40")}}, shipDay, domain.NegativePolicy{}); err != nil {
		t.Fatal(err)
	}
	wantReserved(t, admin, p, "W2", "4", "0")
	wantReserved(t, admin, p, "W1", "0", "0")
	rebuildClean(t, s, ctx)
}

// A cancel, delete or shortening that would leave more reserved against the receipt than the
// order still expects is refused with INV-RES-004 and changes nothing; releasing the reservation
// (or leaving enough expected) lets it through.
func TestTransferChangeRefusedWhileReservedAgainstItsReceipt(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))
	o, _, err := s.CreateTransfer(ctx, "cover-1", transferOrder(ctx, p, "40", true))
	if err != nil {
		t.Fatal(err)
	}
	r, err := reserveOrdered(ctx, s, p, "25", domain.PolicyManual, dims("warehouse", "W2"), "cover-a")
	if err != nil {
		t.Fatal(err)
	}
	q20, q30 := dec("20"), dec("30")
	line0 := o.Lines[0].ID
	if _, err := s.EditTransfer(ctx, o.ID, o.Version, domain.TransferEdit{Update: []domain.LineEdit{{LineID: line0, Qty: &q20}}}); problemCode(err) != domain.CodeReservedOrderedCover {
		t.Fatalf("shorten below the reserved quantity: %v, want %s", err, domain.CodeReservedOrderedCover)
	}
	if _, err := s.EditTransfer(ctx, o.ID, o.Version, domain.TransferEdit{Remove: []string{line0}}); problemCode(err) == "" {
		t.Fatal("removing the only line must be refused")
	}
	if _, err := s.CancelTransfer(ctx, o.ID, o.Version); problemCode(err) != domain.CodeReservedOrderedCover {
		t.Fatalf("cancel: %v, want %s", err, domain.CodeReservedOrderedCover)
	}
	if err := s.DeleteTransfer(ctx, o.ID, o.Version); problemCode(err) != domain.CodeReservedOrderedCover {
		t.Fatalf("delete: %v, want %s", err, domain.CodeReservedOrderedCover)
	}
	wantBuckets(t, admin, p, "W2", "S1", "0", "40") // nothing moved
	wantReserved(t, admin, p, "W2", "0", "25")

	// Shortening to a quantity that still covers the reservation is fine.
	o, err = s.EditTransfer(ctx, o.ID, o.Version, domain.TransferEdit{Update: []domain.LineEdit{{LineID: line0, Qty: &q30}}})
	if err != nil {
		t.Fatalf("shorten to 30 still covers 25: %v", err)
	}
	wantBuckets(t, admin, p, "W2", "S1", "0", "30")

	if _, err := s.ReleaseReservation(ctx, r.Reservations[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CancelTransfer(ctx, o.ID, o.Version); err != nil {
		t.Fatalf("cancel once nothing is reserved against it: %v", err)
	}
	wantBuckets(t, admin, p, "W2", "S1", "0", "0")
	rebuildClean(t, s, ctx)
}

// A purchase receipt stepping from ordered or arrived to registered converts the reservations
// against it; the step from ordered to arrived does not (the stock is not there yet).
func TestStatusStepToPhysicalConvertsReservations(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	c, _, err := s.CreateTransaction(ctx, store.CreateTransactionInput{LegalEntityID: legalEntity(ctx), SourceType: "PURCH_LINE",
		SourceID: ids.New(), ProductID: p, Dims: dims(), Qty: dec("8"), EnteredQty: dec("8"), EnteredUnit: "EA",
		Status: domain.StatusOrdered, Currency: "USD", CostPrice: dec("3"), Date: shipDay, IdempotencyKey: ids.New()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reserveOrdered(ctx, s, p, "5", domain.PolicyManual, dims(), "step-a"); err != nil {
		t.Fatal(err)
	}
	stepOn(t, ctx, s, shipDay, c.TransactionID, domain.CommandStep, domain.StatusOrdered, domain.StatusArrived)
	wantReserved(t, admin, p, "W1", "0", "5")
	stepOn(t, ctx, s, shipDay, c.TransactionID, domain.CommandStep, domain.StatusArrived, domain.StatusRegistered)
	wantReserved(t, admin, p, "W1", "5", "0")
	if n := convertedEvents(t, admin); n != 1 {
		t.Fatalf("reservation.converted events %d, want 1", n)
	}
	var seq int64
	if err := admin.QueryRow(context.Background(), `select last_txn_seq from on_hand where product_id = $1`, p).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	// Two steps after the registration: the conversion must not roll the row's sequence back.
	if seq != 3 {
		t.Fatalf("last_txn_seq %d, want 3", seq)
	}
	rebuildClean(t, s, ctx)
}

// A transfer order without a transit warehouse receives as it ships, so the shipment converts the
// reservations against its receipt: a partial shipment splits, the rest converts with the rest.
func TestDirectTransferShipmentConvertsReservations(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))
	o, _, err := s.CreateTransfer(ctx, "direct-1", transferOrder(ctx, p, "40", false))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reserveOrdered(ctx, s, p, "25", domain.PolicyManual, dims("warehouse", "W2"), "direct-a"); err != nil {
		t.Fatal(err)
	}
	o, err = s.ShipTransfer(ctx, o.ID, o.Version, []store.TransferShipment{{LineID: o.Lines[0].ID, Qty: dec("10")}}, shipDay, domain.NegativePolicy{})
	if err != nil {
		t.Fatal(err)
	}
	wantReserved(t, admin, p, "W2", "10", "15")
	wantBuckets(t, admin, p, "W2", "S1", "0", "30")
	if _, err := s.ShipTransfer(ctx, o.ID, o.Version, nil, shipDay, domain.NegativePolicy{}); err != nil {
		t.Fatal(err)
	}
	wantReserved(t, admin, p, "W2", "25", "0")
	if n := convertedEvents(t, admin); n != 2 {
		t.Fatalf("reservation.converted events %d, want 2", n)
	}
	rebuildClean(t, s, ctx)
}

// Closing a receipt short gives up the rest of the expected receipt: when reservations still rely
// on it after what arrived converted, the receipt is refused with INV-RES-004 and nothing moves.
func TestReceiptClosedShortRefusedWhileReservedAgainstTheRest(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))
	o, _, err := s.CreateTransfer(ctx, "short-1", transferOrder(ctx, p, "40", true))
	if err != nil {
		t.Fatal(err)
	}
	w2 := dims("warehouse", "W2")
	if _, err := reserveOrdered(ctx, s, p, "30", domain.PolicyManual, w2, "short-a"); err != nil {
		t.Fatal(err)
	}
	b, err := reserveOrdered(ctx, s, p, "8", domain.PolicyManual, w2, "short-b")
	if err != nil {
		t.Fatal(err)
	}
	if o, err = s.ShipTransfer(ctx, o.ID, o.Version, nil, shipDay, domain.NegativePolicy{}); err != nil {
		t.Fatal(err)
	}
	short := []store.TransferReceipt{{LineID: o.Lines[0].ID, Qty: dec("36"), CloseShort: true}}
	if _, err := s.ReceiveTransfer(ctx, o.ID, o.Version, short, shipDay, domain.NegativePolicy{}); problemCode(err) != domain.CodeReservedOrderedCover {
		t.Fatalf("close short with 38 reserved and 36 arriving: %v, want %s", err, domain.CodeReservedOrderedCover)
	}
	wantReserved(t, admin, p, "W2", "0", "38") // nothing converted
	wantBuckets(t, admin, p, "W2", "S1", "0", "40")
	if n := convertedEvents(t, admin); n != 0 {
		t.Fatalf("reservation.converted events %d after a refused receipt", n)
	}

	if _, err := s.ReleaseReservation(ctx, b.Reservations[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReceiveTransfer(ctx, o.ID, o.Version, short, shipDay, domain.NegativePolicy{}); err != nil {
		t.Fatalf("close short once the rest is not relied on: %v", err)
	}
	wantReserved(t, admin, p, "W2", "30", "0")
	wantBuckets(t, admin, p, "W2", "S1", "0", "0")
	rebuildClean(t, s, ctx)
}

// Releasing reservations while a receipt converts the reservations of the same row neither
// deadlocks nor double counts: both take the on-hand row before the reservation rows.
func TestReleaseRacingReceiptConversion(t *testing.T) {
	s, _, admin := setup(t)
	ctx := entityCtx(1, ids.New())
	p := ids.New()
	post(t, ctx, s, domain.JournalOpeningBalance, line(p, "100", "2", dims()))
	o, _, err := s.CreateTransfer(ctx, "race-1", transferOrder(ctx, p, "40", true))
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	var resIDs []string
	for i := 0; i < n; i++ {
		r, err := reserveOrdered(ctx, s, p, "4", domain.PolicyManual, dims("warehouse", "W2"), fmt.Sprintf("race-%d", i))
		if err != nil {
			t.Fatal(err)
		}
		resIDs = append(resIDs, r.Reservations[0].ID)
	}
	if o, err = s.ShipTransfer(ctx, o.ID, o.Version, nil, shipDay, domain.NegativePolicy{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, n+1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		cur := o
		for i := 0; i < 4; i++ {
			next, err := s.ReceiveTransfer(ctx, cur.ID, cur.Version, []store.TransferReceipt{{LineID: cur.Lines[0].ID, Qty: dec("10")}}, shipDay, domain.NegativePolicy{})
			if err != nil {
				errs <- fmt.Errorf("receipt %d: %w", i, err)
				return
			}
			cur = next
		}
	}()
	for _, id := range resIDs[:n/2] {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := s.ReleaseReservation(ctx, id); err != nil {
				errs <- fmt.Errorf("release %s: %w", id, err)
			}
		}(id)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// All 40 arrived, so nothing is left ORDERED. Releases are per allocation (DELETE
	// /reservations/{id}): a receipt of 10 against 4-unit reservations splits one, and when the
	// release of that reservation lands after the split it frees only the ORDERED remainder, so
	// the split-off PHYSICAL allocation of the same request stays held. Which one splits depends
	// on the interleaving; what must hold for every interleaving is that each request still
	// accounts for exactly its 4 units (released + active), the kept requests are wholly
	// physical, and the on-hand row agrees with the active allocations.
	var released, kept, splitHeld = dec("0"), dec("0"), dec("0")
	for i := 0; i < n; i++ {
		allocs := allocations(t, admin, p, fmt.Sprintf("race-%d", i))
		total := dec("0")
		for _, a := range allocs {
			total = total.Add(dec(a.qty))
			switch {
			case a.status == "ACTIVE" && a.kind != "PHYSICAL":
				t.Fatalf("race-%d allocation %d is %s %s after all 40 arrived: %+v", i, a.no, a.status, a.kind, allocs)
			case i >= n/2 && a.status != "ACTIVE":
				t.Fatalf("race-%d was never released, allocation %d is %s: %+v", i, a.no, a.status, allocs)
			case i < n/2 && a.no == 1 && a.status != "RELEASED":
				t.Fatalf("race-%d was released, allocation 1 is %s: %+v", i, a.status, allocs)
			case i < n/2 && a.no > 1 && (a.status != "ACTIVE" || !a.converted):
				t.Fatalf("race-%d allocation %d should be a split-off still held: %+v", i, a.no, allocs)
			case i < n/2 && a.status == "RELEASED":
				released = released.Add(dec(a.qty))
			case i < n/2:
				splitHeld = splitHeld.Add(dec(a.qty))
			default:
				kept = kept.Add(dec(a.qty))
			}
		}
		if !total.Equal(dec("4")) {
			t.Fatalf("race-%d allocations total %s, want 4: %+v", i, total, allocs)
		}
	}
	if !kept.Equal(dec("16")) || !released.Add(splitHeld).Equal(dec("16")) {
		t.Fatalf("kept %s, released %s + split-off %s, want 16 and 16", kept, released, splitHeld)
	}
	wantReserved(t, admin, p, "W2", kept.Add(splitHeld).String(), "0")
	rebuildClean(t, s, ctx)
	t.Logf("released %s, split-off still held %s", released, splitHeld)
}

type allocation struct {
	no        int
	status    string
	kind      string
	qty       string
	converted bool
}

// allocations lists every allocation of one reserve request of a product, in allocation order.
func allocations(t *testing.T, admin *pgxpool.Pool, product, key string) []allocation {
	t.Helper()
	rows, err := admin.Query(context.Background(), `select allocation_no, status, kind, qty::text, converted_at is not null
		from inventory_reservation where product_id = $1 and idempotency_key = $2 order by allocation_no`, product, key)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []allocation
	for rows.Next() {
		var a allocation
		if err := rows.Scan(&a.no, &a.status, &a.kind, &a.qty, &a.converted); err != nil {
			t.Fatal(err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
