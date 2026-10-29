package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

func slot(h, m int) time.Time { return time.Date(2026, 10, 27, h, m, 0, 0, time.UTC) }

func door(t *testing.T, st *Store, ctx context.Context, wh, code string) domain.DockDoor {
	t.Helper()
	d, _, err := st.CreateDoor(ctx, key("door-"+ids.New()), domain.DockDoor{ID: ids.New(), WarehouseID: wh, Code: code})
	if err != nil {
		t.Fatalf("CreateDoor: %v", err)
	}
	return d
}

func booking(wh, doorID, ref string, start, end time.Time) domain.Appointment {
	return domain.Appointment{ID: ids.New(), WarehouseID: wh, DoorID: doorID, Direction: domain.Inbound, Reference: ref, Start: start, End: end}
}

// AC-31.8 through the store: an overlapping booking on D1 is WMS-0108, a
// touching one and one on another door are accepted, and a door of another
// warehouse is not found.
func TestStore_DockAppointments(t *testing.T) {
	st, ctx, wh := setup(t)
	d1, d2 := door(t, st, ctx, wh, "D1"), door(t, st, ctx, wh, "D2")
	if _, _, err := st.CreateDoor(ctx, key("dup"), domain.DockDoor{ID: ids.New(), WarehouseID: wh, Code: "D1"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate door code: err = %v", err)
	}
	first, replayed, err := st.CreateAppointment(ctx, key("a1"), booking(wh, d1.ID, "ASN-1", slot(9, 0), slot(9, 45)))
	if err != nil || replayed || first.DoorCode != "D1" || first.CreatedAt.IsZero() {
		t.Fatalf("first appointment: %+v replayed=%v err=%v", first, replayed, err)
	}
	if again, replayed, err := st.CreateAppointment(ctx, key("a1"), booking(wh, d1.ID, "ASN-1", slot(9, 0), slot(9, 45))); err != nil || !replayed || again.ID != first.ID {
		t.Fatalf("replay: %+v replayed=%v err=%v", again, replayed, err)
	}
	_, _, err = st.CreateAppointment(ctx, key("a2"), booking(wh, d1.ID, "ASN-2", slot(9, 30), slot(10, 0)))
	assertCode(t, err, domain.CodeDoorOverlap)
	if _, _, err := st.CreateAppointment(ctx, key("a3"), booking(wh, d1.ID, "ASN-3", slot(9, 45), slot(10, 0))); err != nil {
		t.Fatalf("touching appointment: %v", err)
	}
	if _, _, err := st.CreateAppointment(ctx, key("a4"), booking(wh, d2.ID, "ASN-2", slot(9, 30), slot(10, 0))); err != nil {
		t.Fatalf("other door: %v", err)
	}
	if _, _, err := st.CreateAppointment(ctx, key("a5"), booking(ids.New(), d1.ID, "ASN-9", slot(11, 0), slot(11, 15))); !errors.Is(err, ErrNotFound) {
		t.Fatalf("door of another warehouse: err = %v, want ErrNotFound", err)
	}

	list, err := st.ListAppointments(ctx, AppointmentFilter{WarehouseID: wh, From: slot(9, 0), To: slot(9, 45), Limit: 50})
	if err != nil || len(list) != 2 || list[0].Reference != "ASN-1" || list[1].DoorCode != "D2" {
		t.Fatalf("window 09:00–09:45 = %+v err=%v", list, err)
	}
	list, err = st.ListAppointments(ctx, AppointmentFilter{WarehouseID: wh, DoorID: d1.ID, Limit: 50})
	if err != nil || len(list) != 2 || list[1].Reference != "ASN-3" {
		t.Fatalf("door D1 = %+v err=%v", list, err)
	}
	doors, err := st.ListDoors(ctx, wh)
	if err != nil || len(doors) != 2 || doors[0].Code != "D1" {
		t.Fatalf("doors = %+v err=%v", doors, err)
	}
}

// Concurrent bookings of the same slot on one door: exactly one wins, every
// other caller gets WMS-0108, because the door row is locked first.
func TestStore_ConcurrentBookingsOfOneDoor(t *testing.T) {
	st, ctx, wh := setup(t)
	d := door(t, st, ctx, wh, "D1")
	const callers = 8
	var wg sync.WaitGroup
	errs := make([]error, callers)
	for i := range callers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, errs[i] = st.CreateAppointment(ctx, key("c-"+ids.New()), booking(wh, d.ID, "ASN", slot(14, 0), slot(15, 0)))
		}(i)
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		var de *domain.Error
		switch {
		case err == nil:
			won++
		case errors.As(err, &de) && de.Code == domain.CodeDoorOverlap:
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if won != 1 {
		t.Fatalf("%d bookings of one slot succeeded, want 1", won)
	}
}

func asn(wh string) domain.Receipt {
	line := func(n int, lp string) domain.ReceiptLine {
		return domain.ReceiptLine{LineNo: n, ItemID: "BOLT", LPID: lp, POLineRef: "PO-7/1", ExpectedQty: dq("100"), Weight: dq("40")}
	}
	return domain.Receipt{ID: ids.New(), WarehouseID: wh, ASNRef: "ASN-3", TolerancePct: domain.DefaultASNTolerance(),
		Lines: []domain.ReceiptLine{line(1, "LP-1"), line(2, "LP-2"), line(3, "LP-3")}}
}

// AC-31.7: an ASN with 3 pallets; pallet 2 received at 95 of 100 records a
// −5 (−5%) discrepancy and creates put-away work for pallet 2 only, which puts
// the 95 at a location once confirmed. receipt.registered is emitted once.
func TestStore_ReceiveASNPallet(t *testing.T) {
	st, ctx, wh := setup(t)
	mustLocations(t, st, ctx, wh, loc("A-01", "A", 1, domain.Capacity{AllowMixedItems: true}))
	mustDirective(t, st, ctx, directive(wh, "PURCHASE_PUTAWAY", domain.DirectionPut, domain.StrategyConsolidate, "A"))

	r, replayed, err := st.RegisterASN(ctx, key("asn-3"), asn(wh))
	if err != nil || replayed || r.Status != domain.ReceiptOpen || r.Version != 1 || len(r.Lines) != 3 || r.Lines[0].Status != domain.PalletExpected {
		t.Fatalf("RegisterASN: %+v replayed=%v err=%v", r, replayed, err)
	}
	if _, _, err := st.RegisterASN(ctx, key("asn-3-again"), asn(wh)); !errors.Is(err, ErrConflict) {
		t.Fatalf("same ASN under another key: err = %v, want ErrConflict", err)
	}

	in := ReceiveInput{ReceiptID: r.ID, LineNo: 2, Qty: dq("95"), User: "clerk", IfMatch: 1, Now: time.Now()}
	got, replayed, err := st.ReceiveLine(ctx, key("recv-2"), in)
	if err != nil || replayed {
		t.Fatalf("ReceiveLine: replayed=%v err=%v", replayed, err)
	}
	p2 := got.Receipt.Lines[1]
	if !p2.VarianceQty.Equal(dq("-5")) || !p2.VariancePct.Equal(dq("-5")) || !p2.Discrepancy || p2.WorkID != got.Work.ID {
		t.Fatalf("pallet 2 = %+v", p2)
	}
	if len(got.Warnings) != 1 || got.Warnings[0].Code != domain.CodeVariance {
		t.Fatalf("warnings = %+v", got.Warnings)
	}
	if got.Receipt.Status != domain.ReceiptPartial || got.Receipt.Version != 2 ||
		got.Receipt.Lines[0].Status != domain.PalletExpected || got.Receipt.Lines[2].Status != domain.PalletExpected {
		t.Fatalf("receipt = %s v%d", got.Receipt.Status, got.Receipt.Version)
	}
	w := got.Work
	if w.WorkType != "PURCHASE_PUTAWAY" || w.Status != domain.WorkOpen || len(w.Lines) != 1 || !w.Lines[0].Qty.Equal(dq("95")) ||
		!w.Lines[0].Weight.Equal(dq("38")) || w.Lines[0].Attributes["lpId"] != "LP-2" || w.Lines[0].ToLocationID == "" {
		t.Fatalf("put-away work = %+v", w)
	}

	if again, replayed, err := st.ReceiveLine(ctx, key("recv-2"), in); err != nil || !replayed || again.Work.ID != w.ID {
		t.Fatalf("replay: replayed=%v err=%v", replayed, err)
	}
	in.IfMatch = 0
	_, _, err = st.ReceiveLine(ctx, key("recv-2b"), in)
	assertCode(t, err, domain.CodeLineReceived)
	in.LineNo, in.IfMatch = 1, 1
	if _, _, err := st.ReceiveLine(ctx, key("recv-1-stale"), in); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale If-Match: err = %v", err)
	}
	in.LineNo, in.IfMatch = 9, 0
	if _, _, err := st.ReceiveLine(ctx, key("recv-9"), in); !errors.Is(err, domain.ErrPalletNotFound) {
		t.Fatalf("unknown pallet: err = %v", err)
	}
	if _, _, err := st.ReceiveLine(ctx, key("recv-x"), ReceiveInput{ReceiptID: ids.New(), LineNo: 1, Qty: dq("1"), User: "clerk", Now: time.Now()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown receipt: err = %v", err)
	}
	if n := outboxCount(t, adminOf(st), "dev.passage.wms.receipt.registered.v1"); n != 1 {
		t.Fatalf("receipt.registered events = %d, want 1", n)
	}

	// The 95 reach A-01 only when the put-away is confirmed; nothing else does.
	if q := stockAt(t, st, ctx, wh, "A-01", "BOLT"); !q.IsZero() {
		t.Fatalf("stock before put-away = %s", q)
	}
	assigned := assign(t, st, ctx, wh, "putter", time.Now())
	if assigned == nil || assigned.ID != w.ID {
		t.Fatalf("assigned %+v, want the put-away work", assigned)
	}
	confirm(t, st, ctx, *assigned, "putter")
	if q := stockAt(t, st, ctx, wh, "A-01", "BOLT"); !q.Equal(dq("95")) {
		t.Fatalf("stock after put-away = %s, want 95", q)
	}

	stored, err := st.GetReceipt(ctx, r.ID)
	if err != nil || stored.Lines[1].ReceivedBy != "clerk" || stored.Lines[1].ReceivedAt == nil || stored.Lines[1].POLineRef != "PO-7/1" {
		t.Fatalf("GetReceipt = %+v err=%v", stored, err)
	}
	sums, err := st.ListReceipts(ctx, ReceiptFilter{WarehouseID: wh, Limit: 10})
	if err != nil || len(sums) != 1 || sums[0].Lines != 3 || sums[0].Received != 1 || sums[0].Discrepancies != 1 || sums[0].Status != domain.ReceiptPartial {
		t.Fatalf("ListReceipts = %+v err=%v", sums, err)
	}
	if open, err := st.ListReceipts(ctx, ReceiptFilter{WarehouseID: wh, Status: "OPEN", Limit: 10}); err != nil || len(open) != 0 {
		t.Fatalf("OPEN receipts = %+v err=%v", open, err)
	}
}

// A pallet no directive can place still posts: its put-away work is
// BLOCKED_NO_LOCATION (BR-31.10 "receipt still posts actual qty").
func TestStore_ReceiveWithoutDirectiveBlocksPutAway(t *testing.T) {
	st, ctx, wh := setup(t)
	r, _, err := st.RegisterASN(ctx, key("asn"), asn(wh))
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := st.ReceiveLine(ctx, key("recv"), ReceiveInput{ReceiptID: r.ID, LineNo: 1, Qty: dq("100"), User: "clerk", Now: time.Now()})
	if err != nil || got.Work.Status != domain.WorkBlockedNoLocation || got.Receipt.Lines[0].Discrepancy || len(got.Warnings) != 0 {
		t.Fatalf("receive: %+v err=%v", got, err)
	}
}
