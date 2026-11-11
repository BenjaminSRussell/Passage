package store

import (
	"strings"
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
	// q finds an order by part of its number, ignoring case.
	if found, err := s.ListOrders(ctx, OrderFilter{Q: strings.ToLower(a2.OrderNumber), Limit: 50}); err != nil || len(found.Items) != 1 || found.Items[0].ID != a2.ID {
		t.Fatalf("search %q: %+v %v", a2.OrderNumber, found.Items, err)
	}
	if none, err := s.ListOrders(ctx, OrderFilter{Q: "no-such-order", Limit: 50}); err != nil || len(none.Items) != 0 {
		t.Fatalf("search for nothing: %+v %v", none.Items, err)
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

// The portal reorders from the customer's own committed orders: the latest price per product and unit, never a draft, another
// customer's order or a cancelled one.
func TestPostgres_PortalProductsAreTheCustomersOwnLatestCommittedLines(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("sales-rep")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(appOf(t, pool))
	le, custA, custB := ids.New(), ids.New(), ids.New()
	pump, valve, site, wh := ids.New(), ids.New(), ids.New(), ids.New()
	create := func(customer, hash, product string, price int64) Order {
		line := Line{LineNo: 1, ProductID: product, Quantity: d(1), Unit: "EA", SiteID: site, WarehouseID: wh, UnitPrice: d(price), DiscPct: d(0), LineNet: d(price), RequestedShipDate: "2027-03-04"}
		o, _, err := s.CreateOrder(ctx, NewOrder{LegalEntityID: le, CustomerID: customer, InvoiceAccountID: customer, OrderType: "ORDER", Currency: "USD", RequestedShipDate: "2027-03-04", Channel: "UI", RequestHash: hash, Lines: []Line{line}})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	confirm := func(o Order) {
		t.Helper()
		if res, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", time.Now()); err != nil || !res.Confirmed {
			t.Fatalf("confirm %s: %v", o.ID, err)
		}
	}
	old, newer := create(custA, "h1", pump, 100), create(custA, "h2", pump, 120)
	confirm(old)
	time.Sleep(10 * time.Millisecond)
	confirm(newer)
	_ = create(custA, "h3", valve, 7) // a draft: not bought yet
	other := create(custB, "h4", valve, 9)
	confirm(other)

	rows, err := s.PortalProducts(ctx, PortalProductFilter{LegalEntityID: le, CustomerID: custA, Limit: 50})
	if err != nil || len(rows) != 1 {
		t.Fatalf("customer A products %+v err %v, want only the pump", rows, err)
	}
	if r := rows[0]; r.ProductID != pump || !r.UnitPrice.Equal(d(120)) || r.SiteID != site || r.WarehouseID != wh || r.Currency != "USD" || r.InvoiceAccountID != custA || r.Unit != "EA" {
		t.Fatalf("row %+v, want the latest pump line", r)
	}
	if rows, err := s.PortalProducts(ctx, PortalProductFilter{LegalEntityID: le, CustomerID: custA, ProductIDs: []string{valve}, Limit: 50}); err != nil || len(rows) != 0 {
		t.Fatalf("A asks for the valve only B bought: %+v %v", rows, err)
	}
	if rows, err := s.PortalProducts(ctx, PortalProductFilter{LegalEntityID: le, CustomerID: custB, ProductIDs: []string{valve, pump}, Limit: 50}); err != nil || len(rows) != 1 || rows[0].ProductID != valve {
		t.Fatalf("B: %+v %v", rows, err)
	}
	if rows, err := s.PortalProducts(ctx, PortalProductFilter{LegalEntityID: ids.New(), CustomerID: custA, Limit: 50}); err != nil || len(rows) != 0 {
		t.Fatalf("another legal entity: %+v %v", rows, err)
	}
}

// A price is offered again only from an approved, recent order: not from a draft, a held order or one outside the window, and only the
// newest eligible line of a product counts (with its unit).
func TestPostgres_PortalProductsOnlyOfferApprovedRecentNewestLines(t *testing.T) {
	pool := testkit.DB(t)
	ctx := principalCtx("sales-rep")
	if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	s := New(appOf(t, pool))
	le, cust := ids.New(), ids.New()
	pump, valve, site, wh := ids.New(), ids.New(), ids.New(), ids.New()
	create := func(hash, product, unit string, price int64) Order {
		line := Line{LineNo: 1, ProductID: product, Quantity: d(1), Unit: unit, SiteID: site, WarehouseID: wh, UnitPrice: d(price), DiscPct: d(0), LineNet: d(price), RequestedShipDate: "2027-03-04"}
		o, _, err := s.CreateOrder(ctx, NewOrder{LegalEntityID: le, CustomerID: cust, InvoiceAccountID: cust, OrderType: "ORDER", Currency: "USD", RequestedShipDate: "2027-03-04", Channel: "UI", RequestHash: hash, Lines: []Line{line}})
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	confirm := func(o Order) Order {
		res, err := s.ConfirmOrder(ctx, o.ID, o.Version, "sales-rep", time.Now())
		if err != nil || !res.Confirmed {
			t.Fatalf("confirm: %v", err)
		}
		return res.Order
	}
	good := confirm(create("g1", pump, "EA", 100))
	time.Sleep(10 * time.Millisecond)
	held := confirm(create("g2", pump, "EA", 1)) // newer, but on hold: its price was not approved
	if _, err := s.ApplyHold(ctx, held.ID, held.Version, HoldRequest{HoldType: "MANUAL", ReasonCode: "REVIEW"}, "sales-rep"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	_ = create("g3", pump, "BOX", 5) // draft: not confirmed
	confirm(create("g4", valve, "EA", 7))
	time.Sleep(10 * time.Millisecond)
	confirm(create("g5", valve, "BOX", 70)) // newest valve line is in BOX

	rows, err := s.PortalProducts(ctx, PortalProductFilter{LegalEntityID: le, CustomerID: cust, Limit: 50})
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows %+v err %v, want one row per product", rows, err)
	}
	by := map[string]PortalProduct{}
	for _, r := range rows {
		by[r.ProductID] = r
	}
	if p := by[pump]; !p.UnitPrice.Equal(d(100)) || p.Unit != "EA" {
		t.Errorf("pump %+v: want the approved order's price, not the held or draft one", p)
	}
	if v := by[valve]; v.Unit != "BOX" || !v.UnitPrice.Equal(d(70)) {
		t.Errorf("valve %+v: want only the newest line, in BOX", v)
	}
	_ = good
	if rows, err := s.PortalProducts(ctx, PortalProductFilter{LegalEntityID: le, CustomerID: cust, Limit: 50, Since: time.Now().Add(time.Hour)}); err != nil || len(rows) != 0 {
		t.Errorf("outside the window: %+v %v", rows, err)
	}
}
