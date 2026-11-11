package store

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/sales/migrations"
)

// The customer portal lists with the customer from its service token and ExcludeDraft: in the database, one customer never sees
// another's orders.
func TestPostgres_ListOrdersForOneCustomer(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("sales-rep")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(appOf(t, pool))
	le, custA, custB := ids.New(), ids.New(), ids.New()
	create := func(customer, hash string) Order {
		line := Line{LineNo: 1, ProductID: ids.New(), Quantity: d(1), Unit: "EA", SiteID: ids.New(), WarehouseID: ids.New(), UnitPrice: d(10), DiscPct: d(0), LineNet: d(10), RequestedShipDate: "2027-03-04"}
		o, _, err := s.CreateOrder(ctx, NewOrder{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, OrderType: "ORDER", Currency: "USD", RequestedShipDate: "2027-03-04", Channel: "UI", RequestHash: hash, Lines: []Line{line}})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	a1, a2, b1, draft := create(custA, "a1"), create(custA, "a2"), create(custB, "b1"), create(custA, "a3")
	for _, o := range []Order{a1, a2, b1} {
		if res, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", time.Now()); err != nil || !res.Confirmed {
			t.Fatalf("confirm %s: %v", o.ID, err)
		}
	}
	page, err := s.ListOrders(ctx, OrderFilter{CustomerID: custA, ExcludeDraft: true, Limit: 50})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("customer A orders %+v err %v, want two", page.Items, err)
	}
	for _, o := range page.Items {
		if o.CustomerID != custA || o.ID == b1.ID || o.ID == draft.ID || (o.ID != a1.ID && o.ID != a2.ID) {
			t.Fatalf("leaked %+v", o)
		}
	}
	if all, err := s.ListOrders(ctx, OrderFilter{CustomerID: custA, Limit: 50}); err != nil || len(all.Items) != 3 {
		t.Fatalf("without ExcludeDraft the draft is listed: %d %v", len(all.Items), err)
	}
	if own, err := s.ListOrders(ctx, OrderFilter{CustomerID: custB, ExcludeDraft: true, Limit: 50}); err != nil || len(own.Items) != 1 || own.Items[0].ID != b1.ID {
		t.Fatalf("customer B orders %+v err %v", own.Items, err)
	}
	if none, err := s.ListOrders(ctx, OrderFilter{CustomerID: ids.New(), ExcludeDraft: true, Limit: 50}); err != nil || len(none.Items) != 0 {
		t.Fatalf("a third customer sees %+v err %v", none.Items, err)
	}
}
