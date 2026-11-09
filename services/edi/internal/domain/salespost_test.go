package domain

import (
	"reflect"
	"strings"
	"testing"
)

const (
	spLE        = "00000000-0000-0000-0000-0000000000e1"
	spCustomer  = "00000000-0000-0000-0000-0000000000c1"
	spInvoice   = "00000000-0000-0000-0000-0000000000c2"
	spProductA  = "00000000-0000-0000-0000-0000000000a1"
	spProductB  = "00000000-0000-0000-0000-0000000000a2"
	spSite      = "00000000-0000-0000-0000-0000000000f1"
	spWarehouse = "00000000-0000-0000-0000-0000000000f2"
	spPartner   = "00000000-0000-0000-0000-0000000000b1"
)

func po850(purpose, orderType string, extra ...Segment) Transaction {
	body := []Segment{{ID: "BEG", Elements: []string{purpose, orderType, "PO1234", "", "20261021"}}}
	body = append(body, extra...)
	body = append(body,
		Segment{ID: "PO1", Elements: []string{"1", "10", "EA", "2.50", "", "BP", "ABC-1"}},
		Segment{ID: "PO1", Elements: []string{"2", "4", "CA", "11", "", "UP", "012345678905"}},
		Segment{ID: "CTT", Elements: []string{"2"}})
	return Transaction{SetID: "850", Control: "0001", Body: body}
}

func spXrefs() map[ItemRef]ItemXref {
	return map[ItemRef]ItemXref{
		{Qualifier: "BP", Value: "ABC-1"}:        {Qualifier: "BP", PartnerItem: "ABC-1", ProductID: spProductA, SiteID: spSite, WarehouseID: spWarehouse},
		{Qualifier: "UP", Value: "012345678905"}: {Qualifier: "UP", PartnerItem: "012345678905", ProductID: spProductB, SiteID: spSite, WarehouseID: spWarehouse},
	}
}

var spUnits = map[string]string{"EA": "EACH", "CA": "CASE"}

func spBuild(t *testing.T, tr Transaction, xrefs map[ItemRef]ItemXref) (SalesOrderRequest, *Park) {
	t.Helper()
	order, issues := Map850(tr)
	if len(issues) != 0 {
		t.Fatalf("fixture must map cleanly: %v", issues)
	}
	return BuildSalesOrder(spPartner, order, TradeProfile{LegalEntityID: spLE, CustomerID: spCustomer},
		SalesDefaults{InvoiceAccountID: spInvoice, Currency: "EUR", UnitMap: spUnits}, xrefs)
}

func TestBuildSalesOrder_MapsPOOntoTheEDIChannelOrder(t *testing.T) {
	got, park := spBuild(t, po850("00", "SA", Segment{ID: "CUR", Elements: []string{"BY", "USD"}}, Segment{ID: "DTM", Elements: []string{"010", "20261105"}}), spXrefs())
	if park != nil {
		t.Fatalf("parked: %v", park)
	}
	want := SalesOrderRequest{LegalEntityID: spLE, CustomerID: spCustomer, InvoiceAccountID: spInvoice, Currency: "USD",
		RequestedShipDate: "2026-11-05", Channel: "EDI", ExternalRef: spPartner + ":PO1234", Lines: []SalesOrderLine{
			{ProductID: spProductA, Quantity: "10", Unit: "EACH", SiteID: spSite, WarehouseID: spWarehouse, UnitPrice: "2.5", RequestedShipDate: "2026-11-05"},
			{ProductID: spProductB, Quantity: "4", Unit: "CASE", SiteID: spSite, WarehouseID: spWarehouse, UnitPrice: "11", RequestedShipDate: "2026-11-05"},
		}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("request\n got %+v\nwant %+v", got, want)
	}
}

func TestBuildSalesOrder_DefaultsTheCurrencyAndFallsBackToTheDeliveryDate(t *testing.T) {
	got, park := spBuild(t, po850("00", "SA", Segment{ID: "DTM", Elements: []string{"002", "20261110"}}), spXrefs())
	if park != nil || got.Currency != "EUR" || got.RequestedShipDate != "2026-11-10" {
		t.Fatalf("currency from the partner defaults, date from DTM 002: %+v %v", got, park)
	}
}

func TestBuildSalesOrder_ParksWhatItCannotMap(t *testing.T) {
	dated := Segment{ID: "DTM", Elements: []string{"010", "20261105"}}
	partial := spXrefs()
	delete(partial, ItemRef{Qualifier: "UP", Value: "012345678905"})
	cases := []struct {
		name   string
		tr     Transaction
		xrefs  map[ItemRef]ItemXref
		code   string
		detail string
	}{
		{"unmapped item", po850("00", "SA", dated), partial, CodeNoItemRef, "UP 012345678905"},
		{"no cross-references at all", po850("00", "SA", dated), nil, CodeNoItemRef, "ABC-1"},
		{"change order", po850("04", "SA", dated), spXrefs(), CodeOrderUnpostable, "purpose 04"},
		{"blanket order", po850("00", "BK", dated), spXrefs(), CodeOrderUnpostable, "blanket"},
		{"no date", po850("00", "SA"), spXrefs(), CodeOrderUnpostable, "no requested ship"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, park := spBuild(t, tc.tr, tc.xrefs)
			if park == nil || park.Code != tc.code || !strings.Contains(park.Detail, tc.detail) || len(req.Lines) != 0 {
				t.Fatalf("want park %s containing %q, got %+v %v", tc.code, tc.detail, req, park)
			}
		})
	}
}

func TestBuildSalesOrder_ParksAUnitOfMeasureTheMapDoesNotKnow(t *testing.T) {
	order, issues := Map850(po850("00", "SA", Segment{ID: "DTM", Elements: []string{"010", "20261105"}}))
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	partial := SalesDefaults{InvoiceAccountID: spInvoice, Currency: "EUR", UnitMap: map[string]string{"EA": "EACH"}}
	req, park := BuildSalesOrder(spPartner, order, TradeProfile{LegalEntityID: spLE, CustomerID: spCustomer}, partial, spXrefs())
	if park == nil || park.Code != CodeUnitUnmapped || !strings.Contains(park.Detail, `line 2 ("CA")`) || strings.Contains(park.Detail, "line 1") || len(req.Lines) != 0 {
		t.Fatalf("CA has no mapping: %+v %v", req, park)
	}
	none := SalesDefaults{InvoiceAccountID: spInvoice, Currency: "EUR"}
	if _, park := BuildSalesOrder(spPartner, order, TradeProfile{}, none, spXrefs()); park == nil || park.Code != CodeUnitUnmapped {
		t.Fatalf("no unit map at all parks: %v", park)
	}
}

func TestBuildSalesOrder_ALineWithoutAnItemReferenceIsParked(t *testing.T) {
	order := Order{Purpose: "00", OrderType: "SA", PONumber: "P1", RequestedShip: "20261105",
		Lines: []OrderLine{{LineNo: "1", Quantity: "1", UOM: "EA", UnitPrice: "1"}}}
	if _, park := BuildSalesOrder(spPartner, order, TradeProfile{}, SalesDefaults{Currency: "USD"}, spXrefs()); park == nil || park.Code != CodeNoItemRef {
		t.Fatalf("line 1 has no PO106/PO107 pair: %v", park)
	}
}

func TestItemXrefAndSalesDefaults_Validate(t *testing.T) {
	ok := ItemXref{Qualifier: "BP", PartnerItem: "ABC-1", ProductID: spProductA, SiteID: spSite, WarehouseID: spWarehouse}
	if f := ok.Validate(); len(f) != 0 {
		t.Fatalf("valid xref: %v", f)
	}
	bad := ItemXref{Qualifier: "XX", PartnerItem: " x", ProductID: "p", SiteID: "s", WarehouseID: "w"}
	if f := bad.Validate(); len(f) != 5 {
		t.Fatalf("every field is checked: %v", f)
	}
	if f := (SalesDefaults{InvoiceAccountID: "no", Currency: "usd"}).Validate(); len(f) != 2 {
		t.Fatalf("defaults: %v", f)
	}
	if f := (SalesDefaults{InvoiceAccountID: spInvoice, Currency: "USD", UnitMap: map[string]string{"EA": "EACH"}}).Validate(); len(f) != 0 {
		t.Fatalf("valid defaults: %v", f)
	}
	for name, units := range map[string]map[string]string{"long key": {"EACH": "EA"}, "empty value": {"EA": ""}, "long value": {"EA": "ABCDEFGHIJK"}} {
		if f := (SalesDefaults{InvoiceAccountID: spInvoice, Currency: "USD", UnitMap: units}).Validate(); f["unitMap"] == "" {
			t.Fatalf("%s is refused: %v", name, f)
		}
	}
	if (SalesDefaults{}).AutoPosts() != true {
		t.Fatal("auto-post is on unless turned off")
	}
}
