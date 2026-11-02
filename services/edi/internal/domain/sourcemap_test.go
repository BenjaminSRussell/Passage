package domain

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	leID       = "00000000-0000-0000-0000-0000000000e1"
	customerID = "00000000-0000-0000-0000-0000000000c1"
	orderID    = "00000000-0000-0000-0000-0000000000a1"
)

func tradeProfile() TradeProfile {
	return TradeProfile{LegalEntityID: leID, CustomerID: customerID, DefaultSCAC: "ODFL",
		ShipFrom: Location{Name: "Demo Manufacturer", GLN: "0614141000012"},
		ShipTo:   Location{Name: "Demo Distributor DC", GLN: "0614141000029"},
		BillTo:   Location{Name: "Demo Distributor AP", GLN: "0614141000036"},
		RemitTo:  Location{Name: "Demo Manufacturer AR", GLN: "0614141000043"}}
}

func sourceOrder() SourceOrder {
	return SourceOrder{ID: orderID, LegalEntityID: leID, CustomerID: customerID, ExternalRef: "PO-1001",
		CreatedAt: time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC),
		Lines:     []SourceOrderLine{{ID: "ol-1", LineNo: 10, Unit: "EA"}, {ID: "ol-2", LineNo: 20, Unit: "CS"}}}
}

func sourceShipment() SourceShipment {
	shipped := time.Date(2026, 10, 20, 14, 35, 0, 0, time.UTC)
	return SourceShipment{ID: "sh-1", ShipmentNumber: "SHP-0001", LegalEntityID: leID, CustomerID: customerID, OrderID: orderID,
		Status: "SHIPPED", ActualShipDate: &shipped,
		Lines: []SourceShipLine{{ID: "sl-1", OrderLineID: "ol-1", ProductID: "prod-a", Unit: "EA"}, {ID: "sl-2", OrderLineID: "ol-2", ProductID: "prod-b", Unit: "CS"}},
		Containers: []SourceContainer{
			{SSCC: sscc("00345678901234"), Status: "SHIPPED", GrossKg: dec("120.5"), Lines: []SourceContainerLine{{ShipmentLineID: "sl-1", Qty: dec("10")}, {ShipmentLineID: "sl-2", Qty: dec("4")}}},
			{SSCC: sscc("00345678909999"), Status: "SHIPPED", GrossKg: dec("80"), Lines: []SourceContainerLine{{ShipmentLineID: "sl-1", Qty: dec("5")}}},
			{SSCC: sscc("00345678905555"), Status: "UNPACKED", GrossKg: dec("999"), Lines: []SourceContainerLine{{ShipmentLineID: "sl-1", Qty: dec("1")}}},
		}}
}

func sourceInvoice() SourceInvoice {
	line := func(no int, ol, product, qty, price, net, tax string) SourceInvoiceLine {
		return SourceInvoiceLine{LineNo: no, Qty: dec(qty), UnitPrice: dec(price), Net: dec(net), Tax: dec(tax), OrderID: orderID, OrderLineID: ol, ProductID: product}
	}
	return SourceInvoice{ID: "inv-1", LegalEntityID: leID, CustomerID: customerID, Type: "SHIPMENT", Status: "POSTED", Number: "INV-2026-0001",
		InvoiceDate: "2026-10-21", PaymentTermDays: 30,
		Totals: SourceInvoiceTotals{Net: dec("158.00"), Charges: dec("12.50"), Tax: dec("13.60"), Gross: dec("184.10")},
		Lines:  []SourceInvoiceLine{line(1, "ol-1", "prod-a", "15", "8.00", "108.00", "9.18"), line(2, "ol-2", "prod-b", "4", "12.50", "50.00", "4.42")}}
}

func golden(t *testing.T, name string, tr Transaction, setID string) {
	t.Helper()
	env, err := OutboundEnvelope(tr, Party{Qualifier: "ZZ", ID: "PASSAGE"}, Party{Qualifier: "ZZ", ID: "DISTRIBUTOR"}, "T", 1, time.Date(2026, 10, 21, 8, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Serialize(env)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("..", "..", "testdata", "x12", name)
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s %s differs from golden:\n got: %s\nwant: %s", setID, name, got, want)
	}
}

func TestASNFromShipment_GoldenX12(t *testing.T) {
	asn, err := ASNFromShipment(tradeProfile(), sourceShipment(), sourceOrder())
	if err != nil {
		t.Fatal(err)
	}
	if len(asn.Orders) != 1 || len(asn.Orders[0].Packs) != 2 || !asn.GrossWeight.Equal(dec("200.5")) || asn.WeightUnit != "KG" || asn.SCAC != "ODFL" {
		t.Fatalf("only shipped containers count, weight sums, default SCAC applies: %+v", asn)
	}
	tr, err := Build856(asn)
	if err != nil {
		t.Fatal(err)
	}
	golden(t, "856-from-shipment.x12", tr, "856")
}

func TestASNFromShipment_CarrierCodeOverridesDefaultSCAC(t *testing.T) {
	sh := sourceShipment()
	code := "UPSN"
	sh.CarrierCode = &code
	asn, err := ASNFromShipment(tradeProfile(), sh, sourceOrder())
	if err != nil || asn.SCAC != "UPSN" {
		t.Fatalf("%v %+v", err, asn.SCAC)
	}
}

func TestInvoiceFromBilling_GoldenX12(t *testing.T) {
	inv, err := InvoiceFromBilling(tradeProfile(), sourceInvoice(), sourceOrder())
	if err != nil {
		t.Fatal(err)
	}
	if inv.PONumber != "PO-1001" || inv.PODate != "20261015" || inv.Date != "20261021" {
		t.Fatalf("PO and dates come from the sales order and the invoice: %+v", inv)
	}
	tr, err := Build810(inv)
	if err != nil {
		t.Fatalf("the 810 total must equal the billing gross: %v", err)
	}
	golden(t, "810-from-invoice.x12", tr, "810")
}

func TestInvoiceFromBilling_DiscountBecomesAllowanceAndChargeStaysSeparate(t *testing.T) {
	src := sourceInvoice()
	src.Lines[0].Net = dec("112.00")
	src.Lines[0].Tax = dec("9.52")
	src.Totals = SourceInvoiceTotals{Net: dec("162.00"), Charges: dec("12.50"), Tax: dec("13.94"), Gross: dec("188.44")}
	inv, err := InvoiceFromBilling(tradeProfile(), src, sourceOrder())
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Charges) != 2 || inv.Charges[0].Indicator != "A" || !inv.Charges[0].Amount.Equal(dec("8")) || inv.Charges[1].Indicator != "C" {
		t.Fatalf("an 8.00 line discount is one allowance, the header charge one SAC C: %+v", inv.Charges)
	}
	if _, err := Build810(inv); err != nil {
		t.Fatal(err)
	}
}

func TestInvoiceFromBilling_CreditNote(t *testing.T) {
	src := sourceInvoice()
	src.Type = "CREDIT_NOTE"
	inv, err := InvoiceFromBilling(tradeProfile(), src, sourceOrder())
	if err != nil || !inv.CreditNote {
		t.Fatalf("%v %+v", err, inv.CreditNote)
	}
}

func TestSourceMapping_RefusesWhatDoesNotBelongOrIsNotFinal(t *testing.T) {
	other := "00000000-0000-0000-0000-0000000000ff"
	cases := []struct {
		name string
		run  func() error
		want error
	}{
		{"shipment of another legal entity", func() error {
			sh := sourceShipment()
			sh.LegalEntityID = other
			_, err := ASNFromShipment(tradeProfile(), sh, sourceOrder())
			return err
		}, ErrSourceScope},
		{"shipment of another customer", func() error {
			sh := sourceShipment()
			sh.CustomerID = other
			_, err := ASNFromShipment(tradeProfile(), sh, sourceOrder())
			return err
		}, ErrSourceScope},
		{"order of another customer", func() error {
			o := sourceOrder()
			o.CustomerID = other
			_, err := ASNFromShipment(tradeProfile(), sourceShipment(), o)
			return err
		}, ErrSourceScope},
		{"shipment not yet shipped", func() error {
			sh := sourceShipment()
			sh.Status = "PACKED"
			_, err := ASNFromShipment(tradeProfile(), sh, sourceOrder())
			return err
		}, ErrSourceNotReady},
		{"partially shipped shipment", func() error {
			sh := sourceShipment()
			sh.Status = "PARTIALLY_SHIPPED"
			_, err := ASNFromShipment(tradeProfile(), sh, sourceOrder())
			return err
		}, ErrOutbound},
		{"invoice line without a sales order", func() error {
			inv := sourceInvoice()
			inv.Lines[0].OrderID = ""
			_, err := InvoiceFromBilling(tradeProfile(), inv, sourceOrder())
			return err
		}, ErrOutbound},
		{"shipped shipment without shipped containers", func() error {
			sh := sourceShipment()
			sh.Containers = sh.Containers[2:]
			_, err := ASNFromShipment(tradeProfile(), sh, sourceOrder())
			return err
		}, ErrSourceNotReady},
		{"invoice of another legal entity", func() error {
			inv := sourceInvoice()
			inv.LegalEntityID = other
			_, err := InvoiceFromBilling(tradeProfile(), inv, sourceOrder())
			return err
		}, ErrSourceScope},
		{"invoice of another customer", func() error {
			inv := sourceInvoice()
			inv.CustomerID = other
			_, err := InvoiceFromBilling(tradeProfile(), inv, sourceOrder())
			return err
		}, ErrSourceScope},
		{"invoice still a proposal", func() error {
			inv := sourceInvoice()
			inv.Status = "PROPOSAL"
			_, err := InvoiceFromBilling(tradeProfile(), inv, sourceOrder())
			return err
		}, ErrSourceNotReady},
		{"order without customer PO", func() error {
			o := sourceOrder()
			o.ExternalRef = ""
			_, err := InvoiceFromBilling(tradeProfile(), sourceInvoice(), o)
			return err
		}, ErrOutbound},
		{"invoice spanning two orders", func() error {
			inv := sourceInvoice()
			inv.Lines[1].OrderID = other
			_, err := InvoiceFromBilling(tradeProfile(), inv, sourceOrder())
			return err
		}, ErrOutbound},
	}
	for _, c := range cases {
		if err := c.run(); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

func TestTradeProfileValidate(t *testing.T) {
	if f := tradeProfile().Validate(); len(f) != 0 {
		t.Fatalf("valid profile: %v", f)
	}
	p := tradeProfile()
	p.LegalEntityID, p.ShipTo.GLN, p.DefaultSCAC = "x", "123", "odfl"
	if f := p.Validate(); len(f) != 3 {
		t.Fatalf("legal entity, GLN and SCAC are each refused: %v", f)
	}
}
