package store_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
	"github.com/BenjaminSRussell/passage/services/fulfillment/migrations"
)

// The consumer stores the order type per sales order, never lets an older sales order version turn a
// newer type back, and keeps tenants apart.
func TestSalesOrderType_StoredOrderedAndTenantScoped(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app, "0614141", 0)
	ctx := ctxFor(31, "")
	order, other := ids.New(), ids.New()
	apply := func(in store.OrderConfirmedIn) bool {
		t.Helper()
		var changed bool
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			var err error
			changed, err = s.ApplyOrderConfirmed(ctx, tx, in)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return changed
	}
	if !apply(store.OrderConfirmedIn{OrderID: order, OrderType: "ORDER", Version: 1, EventID: "e1"}) {
		t.Fatal("first event not stored")
	}
	if !apply(store.OrderConfirmedIn{OrderID: order, OrderType: "JOURNAL", Version: 2, EventID: "e2"}) {
		t.Fatal("newer version not stored")
	}
	// A redelivered older event changes nothing.
	if apply(store.OrderConfirmedIn{OrderID: order, OrderType: "ORDER", Version: 1, EventID: "e1"}) {
		t.Fatal("older version overwrote a newer one")
	}
	got, err := s.OrderTypes(ctx, []string{order, other})
	if err != nil || len(got) != 1 || got[order] != "JOURNAL" {
		t.Fatalf("OrderTypes = %v, %v; want only the known order as JOURNAL", got, err)
	}
	// Another tenant does not see it.
	if got, err := s.OrderTypes(ctxFor(32, ""), []string{order}); err != nil || len(got) != 0 {
		t.Fatalf("other tenant OrderTypes = %v, %v", got, err)
	}
	if got, err := s.OrderTypes(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("no ids = %v, %v", got, err)
	}
}

// An applied hold blocks the packing slip post with FUL-0001 and the release of the order; releasing it
// (a newer version) lifts both, and a redelivered older applied event does not bring it back.
func TestSalesOrderHold_BlocksPostAndRelease(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app, "0614141", 0)
	ctx := ctxFor(33, "")
	f := releasedDefault(t, s, ctx, 4)
	hold := ids.New()
	apply := func(active bool, version int) {
		t.Helper()
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := s.ApplyOrderHold(ctx, tx, store.OrderHoldIn{OrderID: f.shipment.OrderID, HoldID: hold, HoldType: "CREDIT", Active: active, Version: version, EventID: "e"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	line := f.shipment.Lines[0]
	picked, err := s.ConfirmPick(ctx, f.shipment.ID, f.shipment.Version, []store.PickInput{{ShipmentLineID: line.ID, Picked: line.Planned}}, idem("pick"))
	if err != nil {
		t.Fatal(err)
	}
	packed, err := s.PackContainer(ctx, f.shipment.ID, picked.Version, "CARTON-C12", []store.PackInput{{ShipmentLineID: line.ID, Qty: line.Planned}}, idem("pack"))
	if err != nil {
		t.Fatal(err)
	}
	sh := packed.Shipment
	post := store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(sh, "1")}

	apply(true, 2)
	if held, err := s.HeldOrders(ctx, []string{sh.OrderID}); err != nil || !held[sh.OrderID] {
		t.Fatalf("HeldOrders = %v, %v", held, err)
	}
	if _, err := s.PostPackingSlip(ctx, sh.ID, sh.Version, post, idem("held")); problemCode(err) != store.CodeOrderHeld {
		t.Fatalf("post on a held order = %v, want %s", err, store.CodeOrderHeld)
	}
	apply(false, 3)
	apply(true, 2) // a redelivered older applied event
	if held, _ := s.HeldOrders(ctx, []string{sh.OrderID}); len(held) != 0 {
		t.Fatalf("released hold still held: %v", held)
	}
	if _, err := s.PostPackingSlip(ctx, sh.ID, sh.Version, post, idem("free")); err != nil {
		t.Fatalf("post after the hold was released: %v", err)
	}
	// A released event delivered before its applied event (no row yet) leaves the hold inactive.
	hold = ids.New()
	apply(false, 5)
	apply(true, 4)
	if held, _ := s.HeldOrders(ctx, []string{sh.OrderID}); len(held) != 0 {
		t.Fatalf("applied after its own release re-activated the hold: %v", held)
	}
	// Another tenant's hold on the same order id does not leak.
	if held, err := s.HeldOrders(ctxFor(34, ""), []string{sh.OrderID}); err != nil || len(held) != 0 {
		t.Fatalf("other tenant = %v, %v", held, err)
	}
}

// A shipment released as ORDER before fulfillment heard that sales confirmed the order as JOURNAL is
// refused at post (FUL-0065); a newer ORDER version lets it post.
func TestSalesOrderType_StoredJournalBlocksPost(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := store.New(app, "0614141", 0)
	ctx := ctxFor(35, "")
	f := releasedDefault(t, s, ctx, 2)
	sh := pickAndPack(t, s, ctx, f.shipment).Shipment
	confirm := func(orderType string, version int) {
		t.Helper()
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := s.ApplyOrderConfirmed(ctx, tx, store.OrderConfirmedIn{OrderID: sh.OrderID, OrderType: orderType, Version: version, EventID: "c"})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	post := store.PostInput{PostingDate: today(), Currency: "USD", Costs: costs(sh, "1")}
	confirm("JOURNAL", 2)
	if _, err := s.PostPackingSlip(ctx, sh.ID, sh.Version, post, idem("journal")); problemCode(err) != store.CodeJournalOrder {
		t.Fatalf("post of a stored JOURNAL order = %v, want %s", err, store.CodeJournalOrder)
	}
	confirm("ORDER", 3)
	if _, err := s.PostPackingSlip(ctx, sh.ID, sh.Version, post, idem("order")); err != nil {
		t.Fatalf("post after the order became ORDER: %v", err)
	}
}
