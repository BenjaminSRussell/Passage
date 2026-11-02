package store_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

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
