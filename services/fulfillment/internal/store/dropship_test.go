package store_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/domain"
	fulevents "github.com/BenjaminSRussell/passage/services/fulfillment/internal/events"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

func dropShipInput(le string, qty, cost string) store.DropShipInput {
	return store.DropShipInput{LegalEntityID: le, OrderID: ids.New(), OrderLineID: ids.New(), CustomerID: ids.New(), ProductID: ids.New(), Unit: "EA",
		Qty: decimal.RequireFromString(qty), VendorID: ids.New(), PurchaseOrderID: ids.New(), PurchaseOrderLineID: ids.New(),
		UnitCost: decimal.RequireFromString(cost), Currency: "USD"}
}

// AC-28.5: a confirmed drop-ship books Dr 5000 / Cr 2110 at PO cost, with no
// inventory transaction and no shipment or packing slip.
func TestDropShip_ConfirmBooksCOGSAgainstGRIR_AC28_5(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	le := ids.New()
	d, err := s.CreateDropShip(ctx, dropShipInput(le, "4", "12.50"), idem("ds"))
	if err != nil || d.Status != domain.DropShipOpen || d.Version != 1 || d.Number != "DS-0000001" || len(d.Journal) != 0 {
		t.Fatalf("create = %+v, %v", d, err)
	}
	if n := countEvents(t, admin, fulevents.DropShipCreated); n != 1 {
		t.Fatalf("dropship.created events = %d", n)
	}
	if _, err := s.ConfirmDropShip(ctx, d.ID, d.Version+1, store.DropShipConfirmInput{ShippedOn: today()}, idem("stale")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale confirm: %v, want %s", err, store.CodeVersion)
	}
	shipped := today().AddDate(0, 0, -1)
	in := store.DropShipConfirmInput{ShippedOn: shipped, VendorShipmentRef: "VS-77"}
	key := idem("confirm")
	got, err := s.ConfirmDropShip(ctx, d.ID, d.Version, in, key)
	if err != nil || got.Status != domain.DropShipConfirmed || got.Version != 2 || got.VendorShipmentRef == nil || *got.VendorShipmentRef != "VS-77" {
		t.Fatalf("confirm = %+v, %v", got, err)
	}
	if got.PostingDate == nil || !got.PostingDate.Equal(shipped) || got.ShippedOn == nil || !got.ShippedOn.Equal(shipped) {
		t.Fatalf("dates = %v %v, want the ship date %v", got.ShippedOn, got.PostingDate, shipped)
	}
	if len(got.Journal) != 2 || got.Journal[0].Account != "5000" || !got.Journal[0].Amount.Equal(decimal.RequireFromString("50")) ||
		got.Journal[1].Account != "2110" || !got.Journal[1].Amount.Equal(decimal.RequireFromString("-50")) {
		t.Fatalf("journal = %+v, want Dr 5000 50 / Cr 2110 50", got.Journal)
	}
	if again, err := s.ConfirmDropShip(ctx, d.ID, d.Version, in, key); err != nil || again.Version != 2 || len(again.Journal) != 2 {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if n := countEvents(t, admin, fulevents.DropShipConfirmed); n != 1 {
		t.Fatalf("dropship.confirmed events = %d, want 1 (the replay writes none)", n)
	}
	if v := outboxData(t, admin, fulevents.DropShipConfirmed, "{journal,0,account}"); v != "5000" {
		t.Fatalf("event journal debit = %q", v)
	}
	if v := outboxData(t, admin, fulevents.DropShipConfirmed, "{posting_date}"); v != shipped.Format("2006-01-02") {
		t.Fatalf("event posting_date = %q", v)
	}
	// A whole confirmation is the one event consumers of slice 15 saw: the first and
	// only confirmation, of the ordered quantity, with status CONFIRMED.
	for path, want := range map[string]string{"{confirmation_seq}": "1", "{confirmed_qty}": "4", "{qty}": "4", "{qty_confirmed}": "4", "{status}": domain.DropShipConfirmed, "{journal,0,amount}": "50"} {
		if v := outboxData(t, admin, fulevents.DropShipConfirmed, path); v != want {
			t.Fatalf("event %s = %q, want %q", path, v, want)
		}
	}
	// A second confirmation and a cancel of a confirmed drop-ship are refused.
	if _, err := s.ConfirmDropShip(ctx, d.ID, 2, in, idem("again")); problemCode(err) != store.CodeStatus {
		t.Fatalf("second confirm: %v, want %s", err, store.CodeStatus)
	}
	if _, err := s.CancelDropShip(ctx, d.ID, 2, idem("late")); problemCode(err) != store.CodeStatus {
		t.Fatalf("cancel confirmed: %v, want %s", err, store.CodeStatus)
	}
	// No shipment, slip or inventory-side row exists: the delivered-not-invoiced
	// balance (account 1405) is untouched.
	var shipments, slips int
	if err := admin.QueryRow(context.Background(), `select (select count(*) from shipment), (select count(*) from packing_slip)`).Scan(&shipments, &slips); err != nil || shipments != 0 || slips != 0 {
		t.Fatalf("shipments %d slips %d, %v", shipments, slips, err)
	}
}

// A posting date other than the ship date is honoured, and numbers are gap-free.
func TestDropShip_PostingDateAndNumbers(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	le := ids.New()
	d, _ := s.CreateDropShip(ctx, dropShipInput(le, "2", "3"), idem("a"))
	pd := today().AddDate(0, 0, 1)
	got, err := s.ConfirmDropShip(ctx, d.ID, 1, store.DropShipConfirmInput{ShippedOn: today(), PostingDate: &pd}, idem("a1"))
	if err != nil || got.PostingDate == nil || !got.PostingDate.Equal(pd) {
		t.Fatalf("posting date = %+v, %v", got, err)
	}
	// A refused create (the order line already has a drop-ship) uses no number.
	if _, err := s.CreateDropShip(ctx, store.DropShipInput{LegalEntityID: le, OrderID: d.OrderID, OrderLineID: d.OrderLineID, CustomerID: d.CustomerID, ProductID: d.ProductID,
		Unit: "EA", Qty: d.Qty, VendorID: d.VendorID, PurchaseOrderID: d.PurchaseOrderID, PurchaseOrderLineID: d.PurchaseOrderLineID, UnitCost: d.UnitCost, Currency: "USD"}, idem("dup")); problemCode(err) != store.CodeDropShipExists {
		t.Fatalf("duplicate: %v", err)
	}
	next, _ := s.CreateDropShip(ctx, dropShipInput(le, "2", "1"), idem("b"))
	if next.Number != "DS-0000002" {
		t.Fatalf("second number = %s, want gap-free DS-0000002", next.Number)
	}
	if other, _ := s.CreateDropShip(ctx, dropShipInput(ids.New(), "1", "1"), idem("c")); other.Number != "DS-0000001" {
		t.Fatalf("another legal entity's first number = %s, want DS-0000001", other.Number)
	}
}

// An order line ships from stock or from the vendor, never both: a line on a
// warehouse shipment cannot be drop-shipped (FUL-0062), and release skips a
// line that has an active drop-ship.
func TestDropShip_ExclusiveWithWarehouseShipments(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	f := releasedDefault(t, s, ctx, 3)
	in := dropShipInput(f.legalEntity, "3", "2")
	in.OrderLineID = f.shipment.Lines[0].OrderLineID
	if _, err := s.CreateDropShip(ctx, in, idem("on-shipment")); problemCode(err) != store.CodeDropShipOnShipment {
		t.Fatalf("line on a shipment: %v, want %s", err, store.CodeDropShipOnShipment)
	}
	if _, err := s.CancelShipment(ctx, f.shipment.ID, f.shipment.Version, idem("cancel")); err != nil {
		t.Fatal(err)
	}
	ds, err := s.CreateDropShip(ctx, in, idem("after-cancel"))
	if err != nil {
		t.Fatalf("line of a cancelled shipment: %v", err)
	}
	line := domain.ReleaseLine{OrderID: ids.New(), OrderLineID: ds.OrderLineID, CustomerID: ids.New(), LegalEntityID: f.legalEntity, WarehouseID: f.warehouse,
		ProductID: ids.New(), Unit: "EA", Qty: decimal.NewFromInt(3), ConfirmedShipDate: today(), ATPOK: true}
	groups, _ := domain.Release([]domain.ReleaseLine{line}, domain.ReleaseRule{Today: today(), HorizonDays: 2, RequireFullATP: true})
	res, err := s.RunRelease(ctx, f.warehouse, groups, idem("rel-ds"))
	if err != nil || len(res.Shipments) != 0 || len(res.Skipped) != 1 || res.Skipped[0].Reason != domain.SkipDropShip {
		t.Fatalf("release of a drop-shipped line = %+v, %v; want it skipped as drop_ship", res, err)
	}
	if _, err := s.CancelDropShip(ctx, ds.ID, ds.Version, idem("ds-cancel")); err != nil {
		t.Fatal(err)
	}
	if res, err := s.RunRelease(ctx, f.warehouse, groups, idem("rel-again")); err != nil || len(res.Shipments) != 1 {
		t.Fatalf("release after the drop-ship is cancelled = %+v, %v", res, err)
	}
}

// A confirmed drop-ship is final: cancel is refused with a message that says why.
func TestDropShip_CancelAfterConfirmIsRefusedClearly(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	d, _ := s.CreateDropShip(ctx, dropShipInput(ids.New(), "1", "5"), idem("a"))
	c, err := s.ConfirmDropShip(ctx, d.ID, 1, store.DropShipConfirmInput{ShippedOn: today()}, idem("b"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CancelDropShip(ctx, d.ID, c.Version, idem("c"))
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != store.CodeStatus || p.Status != http.StatusUnprocessableEntity || !strings.Contains(p.Detail, "cannot be cancelled") {
		t.Fatalf("cancel confirmed: %v, want FUL-0001 saying it cannot be cancelled", err)
	}
	if got, _ := s.GetDropShip(ctx, d.ID); got.Status != domain.DropShipConfirmed || len(got.Journal) != 2 {
		t.Fatalf("after the refused cancel = %+v", got)
	}
}

// An order line has one active drop-ship; cancelling frees it for another.
func TestDropShip_OneActivePerOrderLineAndCancel(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	in := dropShipInput(ids.New(), "1", "5")
	d, err := s.CreateDropShip(ctx, in, idem("one"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateDropShip(ctx, in, idem("two")); problemCode(err) != store.CodeDropShipExists {
		t.Fatalf("second active drop-ship: %v, want %s", err, store.CodeDropShipExists)
	}
	if _, err := s.CancelDropShip(ctx, d.ID, 5, idem("c0")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale cancel: %v", err)
	}
	c, err := s.CancelDropShip(ctx, d.ID, 1, idem("c1"))
	if err != nil || c.Status != domain.DropShipCancelled || c.Version != 2 {
		t.Fatalf("cancel = %+v, %v", c, err)
	}
	if n := countEvents(t, admin, fulevents.DropShipCancelled); n != 1 {
		t.Fatalf("dropship.cancelled events = %d", n)
	}
	if _, err := s.ConfirmDropShip(ctx, d.ID, 2, store.DropShipConfirmInput{ShippedOn: today()}, idem("c2")); problemCode(err) != store.CodeStatus {
		t.Fatalf("confirm cancelled: %v, want %s", err, store.CodeStatus)
	}
	if _, err := s.CreateDropShip(ctx, in, idem("three")); err != nil {
		t.Fatalf("after cancel: %v", err)
	}
}

func TestDropShip_ListFilterGetAndIsolation(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	le, other := ids.New(), ids.New()
	a, _ := s.CreateDropShip(ctx, dropShipInput(le, "1", "1"), idem("a"))
	b, _ := s.CreateDropShip(ctx, dropShipInput(other, "1", "1"), idem("b"))
	if _, err := s.ConfirmDropShip(ctx, b.ID, 1, store.DropShipConfirmInput{ShippedOn: today()}, idem("bc")); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListDropShips(ctx, store.DropShipFilter{Limit: 10})
	if err != nil || len(all) != 2 || all[0].ID != b.ID {
		t.Fatalf("list = %+v, %v; want both, newest first", all, err)
	}
	if open, _ := s.ListDropShips(ctx, store.DropShipFilter{Status: "OPEN", Limit: 10}); len(open) != 1 || open[0].ID != a.ID {
		t.Fatalf("open = %+v", open)
	}
	if byVendor, _ := s.ListDropShips(ctx, store.DropShipFilter{VendorID: a.VendorID, Limit: 10}); len(byVendor) != 1 || byVendor[0].ID != a.ID {
		t.Fatalf("by vendor = %+v", byVendor)
	}
	if byOrder, _ := s.ListDropShips(ctx, store.DropShipFilter{OrderID: b.OrderID, Limit: 10}); len(byOrder) != 1 || byOrder[0].ID != b.ID {
		t.Fatalf("by order = %+v", byOrder)
	}
	if got, err := s.GetDropShip(ctx, a.ID); err != nil || got.ID != a.ID || got.Qty.String() != "1" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	// Another tenant sees nothing.
	if _, err := s.GetDropShip(ctxFor(2, ""), a.ID); problemCode(err) != store.CodeDropShipNotFound {
		t.Fatalf("other tenant get: %v, want %s", err, store.CodeDropShipNotFound)
	}
	if l, _ := s.ListDropShips(ctxFor(2, ""), store.DropShipFilter{Limit: 10}); len(l) != 0 {
		t.Fatalf("other tenant list = %+v", l)
	}
	// A principal scoped to one legal entity sees only its drop-ships and cannot create under another.
	scoped := ctxFor(1, le)
	if l, _ := s.ListDropShips(scoped, store.DropShipFilter{Limit: 10}); len(l) != 1 || l[0].ID != a.ID {
		t.Fatalf("scoped list = %+v", l)
	}
	if _, err := s.GetDropShip(scoped, b.ID); problemCode(err) != store.CodeDropShipNotFound {
		t.Fatalf("scoped get of another legal entity: %v", err)
	}
	if _, err := s.CreateDropShip(scoped, dropShipInput(other, "1", "1"), idem("sneak")); problemCode(err) != store.CodeLegalEntityScope {
		t.Fatalf("scoped create under another legal entity: %v, want %s", err, store.CodeLegalEntityScope)
	}
}

func dec(s string) decimal.Decimal { return decimal.RequireFromString(s) }

// A vendor that ships in parts: each confirmation books its own quantity at the
// PO cost, the drop-ship is PARTIALLY_CONFIRMED until none is open, and the
// confirmations and the cumulative journal add up.
func TestDropShip_PartialConfirmationsBookEachShipment(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	d, err := s.CreateDropShip(ctx, dropShipInput(ids.New(), "10", "12.50"), idem("ds"))
	if err != nil || !d.QtyOpen.Equal(dec("10")) || d.QtyConfirmed.Sign() != 0 {
		t.Fatalf("create = %+v, %v", d, err)
	}
	q4 := dec("4")
	first, err := s.ConfirmDropShip(ctx, d.ID, 1, store.DropShipConfirmInput{Qty: &q4, ShippedOn: today(), VendorShipmentRef: "VS-1"}, idem("c1"))
	if err != nil || first.Status != domain.DropShipPartial || !first.QtyConfirmed.Equal(dec("4")) || !first.QtyOpen.Equal(dec("6")) || first.Version != 2 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	if len(first.Journal) != 2 || !first.Journal[0].Amount.Equal(dec("50")) {
		t.Fatalf("first journal = %+v, want Dr 5000 50", first.Journal)
	}
	if v := outboxData(t, admin, fulevents.DropShipConfirmed, "{confirmed_qty}"); v != "4" {
		t.Fatalf("event confirmed_qty = %q", v)
	}
	// Too much, a zero quantity and a reused shipment reference are refused; nothing is booked.
	q7, q0 := dec("7"), dec("0")
	if _, err := s.ConfirmDropShip(ctx, d.ID, 2, store.DropShipConfirmInput{Qty: &q7, ShippedOn: today()}, idem("big")); problemCode(err) != store.CodeDropShipQty {
		t.Fatalf("over the open quantity: %v, want %s", err, store.CodeDropShipQty)
	}
	if _, err := s.ConfirmDropShip(ctx, d.ID, 2, store.DropShipConfirmInput{Qty: &q0, ShippedOn: today()}, idem("zero")); problemCode(err) != store.CodeDropShipQty {
		t.Fatalf("zero quantity: %v, want %s", err, store.CodeDropShipQty)
	}
	q3 := dec("3")
	if _, err := s.ConfirmDropShip(ctx, d.ID, 2, store.DropShipConfirmInput{Qty: &q3, ShippedOn: today(), VendorShipmentRef: "VS-1"}, idem("ref")); problemCode(err) != store.CodeDropShipShipmentRef {
		t.Fatalf("reused reference: %v, want %s", err, store.CodeDropShipShipmentRef)
	}
	if _, err := s.CancelDropShip(ctx, d.ID, 2, idem("cancel")); problemCode(err) != store.CodeStatus {
		t.Fatalf("cancel a partially confirmed drop-ship: %v, want %s", err, store.CodeStatus)
	}
	second, err := s.ConfirmDropShip(ctx, d.ID, 2, store.DropShipConfirmInput{Qty: &q3, ShippedOn: today(), VendorShipmentRef: "VS-2"}, idem("c2"))
	if err != nil || second.Status != domain.DropShipPartial || !second.QtyOpen.Equal(dec("3")) || len(second.Journal) != 4 {
		t.Fatalf("second = %+v, %v", second, err)
	}
	// No quantity confirms everything still open and completes the drop-ship.
	last, err := s.ConfirmDropShip(ctx, d.ID, 3, store.DropShipConfirmInput{ShippedOn: today(), VendorShipmentRef: "VS-3"}, idem("c3"))
	if err != nil || last.Status != domain.DropShipConfirmed || !last.QtyConfirmed.Equal(dec("10")) || last.QtyOpen.Sign() != 0 || len(last.Journal) != 6 {
		t.Fatalf("last = %+v, %v", last, err)
	}
	got, err := s.GetDropShip(ctx, d.ID)
	if err != nil || len(got.Confirmations) != 3 || got.Confirmations[0].Seq != 1 || !got.Confirmations[2].Qty.Equal(dec("3")) ||
		len(got.Confirmations[1].Journal) != 2 || !got.Confirmations[1].Journal[0].Amount.Equal(dec("37.50")) {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if n := countEvents(t, admin, fulevents.DropShipConfirmed); n != 3 {
		t.Fatalf("dropship.confirmed events = %d, want 3", n)
	}
	// Booked in total: the three journals equal ten units at 12.50.
	var total decimal.Decimal
	for _, c := range got.Confirmations {
		total = total.Add(c.Journal[0].Amount)
	}
	if !total.Equal(dec("125")) {
		t.Fatalf("booked %s, want 125", total)
	}
	if _, err := s.ConfirmDropShip(ctx, d.ID, 4, store.DropShipConfirmInput{ShippedOn: today()}, idem("c4")); problemCode(err) != store.CodeStatus {
		t.Fatalf("confirm a CONFIRMED drop-ship: %v, want %s", err, store.CodeStatus)
	}
}

// Replays of a partial confirmation write one confirmation; two confirmations of
// the same version give one success and one 412.
func TestDropShip_PartialConfirmReplayAndConcurrency(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	d, _ := s.CreateDropShip(ctx, dropShipInput(ids.New(), "5", "2"), idem("ds"))
	q2 := dec("2")
	key := idem("once")
	in := store.DropShipConfirmInput{Qty: &q2, ShippedOn: today()}
	if _, err := s.ConfirmDropShip(ctx, d.ID, 1, in, key); err != nil {
		t.Fatal(err)
	}
	if again, err := s.ConfirmDropShip(ctx, d.ID, 1, in, key); err != nil || again.Version != 2 {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from drop_ship_confirmation`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("confirmations = %d, %v", n, err)
	}
	// Two callers read version 2 and confirm the 3 still open at the same moment: the
	// drop-ship row lock serialises them, one books and the other is stale, so the
	// open quantity is never booked twice. The admin holds the row until both wait
	// on it, so the two really overlap.
	q3 := dec("3")
	bg := context.Background()
	hold, err := admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(bg) }()
	if _, err := hold.Exec(bg, `select 1 from drop_ship where id = $1 for update`, d.ID); err != nil {
		t.Fatal(err)
	}
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = s.ConfirmDropShip(ctx, d.ID, 2, store.DropShipConfirmInput{Qty: &q3, ShippedOn: today()}, idem("race"))
		}(i)
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		var waiting int
		if err := admin.QueryRow(bg, `select count(*) from pg_stat_activity where wait_event_type = 'Lock' and query like '%from drop_ship where id%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d confirmations wait on the drop-ship row, want 2", waiting)
		}
	}
	if err := hold.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	ok, stale := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case problemCode(err) == store.CodeVersion:
			stale++
		default:
			t.Fatalf("concurrent confirm: %v", err)
		}
	}
	if ok != 1 || stale != 1 {
		t.Fatalf("concurrent confirms: %d booked, %d stale, want 1 and 1", ok, stale)
	}
	got, err := s.GetDropShip(ctx, d.ID)
	if err != nil || got.Status != domain.DropShipConfirmed || !got.QtyConfirmed.Equal(dec("5")) || len(got.Confirmations) != 2 {
		t.Fatalf("after the race = %+v, %v", got, err)
	}
}

// A cost with more decimals than the currency: three single units at 0.125 book
// 0.13, 0.12 and 0.13, so the vendor's parts add up to the 0.38 of the whole line.
func TestDropShip_PartialConfirmationsAddUpToTheWholeLine(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	d, err := s.CreateDropShip(ctx, dropShipInput(ids.New(), "3", "0.125"), idem("ds"))
	if err != nil {
		t.Fatal(err)
	}
	one := dec("1")
	for v, want := range []string{"0.13", "0.12"} {
		got, err := s.ConfirmDropShip(ctx, d.ID, v+1, store.DropShipConfirmInput{Qty: &one, ShippedOn: today()}, idem("part"))
		if err != nil || !got.Journal[len(got.Journal)-2].Amount.Equal(dec(want)) {
			t.Fatalf("part %d = %+v, %v, want %s", v+1, got.Journal, err, want)
		}
	}
	last, err := s.ConfirmDropShip(ctx, d.ID, 3, store.DropShipConfirmInput{ShippedOn: today()}, idem("rest"))
	if err != nil || last.Status != domain.DropShipConfirmed {
		t.Fatalf("rest = %+v, %v", last, err)
	}
	var debit, credit decimal.Decimal
	for _, e := range last.Journal {
		if e.Account == "5000" {
			debit = debit.Add(e.Amount)
		} else {
			credit = credit.Add(e.Amount)
		}
	}
	if !debit.Equal(dec("0.38")) || !credit.Equal(dec("-0.38")) {
		t.Fatalf("booked Dr %s / Cr %s, want the whole-line 0.38", debit, credit)
	}
}

// The vendor ships part and will not ship the rest: closing the remainder ends
// the drop-ship without booking the closed quantity.
func TestDropShip_CloseRemainder(t *testing.T) {
	s, admin := setup(t)
	ctx := ctxFor(1, "")
	d, _ := s.CreateDropShip(ctx, dropShipInput(ids.New(), "10", "3"), idem("ds"))
	if _, err := s.CloseDropShipRemainder(ctx, d.ID, 1, idem("nothing")); problemCode(err) != store.CodeStatus {
		t.Fatalf("close with nothing confirmed: %v, want %s", err, store.CodeStatus)
	}
	q6 := dec("6")
	p, err := s.ConfirmDropShip(ctx, d.ID, 1, store.DropShipConfirmInput{Qty: &q6, ShippedOn: today()}, idem("c1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CloseDropShipRemainder(ctx, d.ID, 1, idem("stale")); problemCode(err) != store.CodeVersion {
		t.Fatalf("stale close: %v, want %s", err, store.CodeVersion)
	}
	key := idem("close")
	c, err := s.CloseDropShipRemainder(ctx, d.ID, p.Version, key)
	if err != nil || c.Status != domain.DropShipConfirmed || !c.QtyClosed.Equal(dec("4")) || c.QtyOpen.Sign() != 0 || !c.QtyConfirmed.Equal(dec("6")) || len(c.Journal) != 2 {
		t.Fatalf("close = %+v, %v", c, err)
	}
	if again, err := s.CloseDropShipRemainder(ctx, d.ID, p.Version, key); err != nil || again.Version != c.Version {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if n := countEvents(t, admin, fulevents.DropShipClosed); n != 1 {
		t.Fatalf("dropship.closed events = %d, want 1", n)
	}
	if v := outboxData(t, admin, fulevents.DropShipClosed, "{qty_closed}"); v != "4" {
		t.Fatalf("event qty_closed = %q", v)
	}
	if _, err := s.CloseDropShipRemainder(ctx, d.ID, c.Version, idem("again")); problemCode(err) != store.CodeStatus {
		t.Fatalf("close a CONFIRMED drop-ship: %v, want %s", err, store.CodeStatus)
	}
	if _, err := s.ConfirmDropShip(ctx, d.ID, c.Version, store.DropShipConfirmInput{ShippedOn: today()}, idem("late")); problemCode(err) != store.CodeStatus {
		t.Fatalf("confirm after close: %v, want %s", err, store.CodeStatus)
	}
}

// Confirmations follow the drop-ship's tenant and legal-entity isolation.
func TestDropShip_ConfirmationsIsolation(t *testing.T) {
	s, _ := setup(t)
	ctx := ctxFor(1, "")
	le := ids.New()
	d, _ := s.CreateDropShip(ctx, dropShipInput(le, "4", "1"), idem("ds"))
	q := dec("1")
	if _, err := s.ConfirmDropShip(ctx, d.ID, 1, store.DropShipConfirmInput{Qty: &q, ShippedOn: today()}, idem("c")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetDropShip(ctxFor(1, le), d.ID); err != nil || len(got.Confirmations) != 1 {
		t.Fatalf("scoped get = %+v, %v", got, err)
	}
	if _, err := s.GetDropShip(ctxFor(1, ids.New()), d.ID); problemCode(err) != store.CodeDropShipNotFound {
		t.Fatalf("other legal entity: %v", err)
	}
	if _, err := s.ConfirmDropShip(ctxFor(1, ids.New()), d.ID, 2, store.DropShipConfirmInput{Qty: &q, ShippedOn: today()}, idem("x")); problemCode(err) != store.CodeDropShipNotFound {
		t.Fatalf("confirm from another legal entity: %v", err)
	}
	if _, err := s.GetDropShip(ctxFor(2, ""), d.ID); problemCode(err) != store.CodeDropShipNotFound {
		t.Fatalf("other tenant: %v", err)
	}
}
