package domain

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrSourceScope    = fmt.Errorf("%w: source document does not belong to the partner", ErrOutbound)
	ErrSourceNotReady = fmt.Errorf("%w: source document is not final", ErrOutbound)
)

type TradeProfile struct {
	LegalEntityID string   `json:"legalEntityId"`
	CustomerID    string   `json:"customerId"`
	ShipFrom      Location `json:"shipFrom"`
	ShipTo        Location `json:"shipTo"`
	BillTo        Location `json:"billTo"`
	RemitTo       Location `json:"remitTo"`
	DefaultSCAC   string   `json:"defaultScac"`
}

func (p TradeProfile) Validate() map[string]string {
	fields := map[string]string{}
	if !uuidShaped(p.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !uuidShaped(p.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	for name, l := range map[string]Location{"shipFrom": p.ShipFrom, "shipTo": p.ShipTo, "billTo": p.BillTo, "remitTo": p.RemitTo} {
		if l.Name == "" || len(l.Name) > 60 {
			fields[name+".name"] = "must be 1 to 60 characters"
		}
		if !isGLN(l.GLN) {
			fields[name+".gln"] = "must be 13 digits"
		}
	}
	if len(p.DefaultSCAC) < 2 || len(p.DefaultSCAC) > 4 || strings.ToUpper(p.DefaultSCAC) != p.DefaultSCAC {
		fields["defaultScac"] = "must be 2 to 4 upper-case characters"
	}
	return fields
}

func uuidShaped(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		dash := i == 8 || i == 13 || i == 18 || i == 23
		hex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if dash == hex {
			return false
		}
	}
	return true
}

type SourceShipLine struct {
	ID          string `json:"id"`
	OrderLineID string `json:"orderLineId"`
	ProductID   string `json:"productId"`
	Unit        string `json:"unit"`
}

type SourceContainerLine struct {
	ShipmentLineID string          `json:"shipmentLineId"`
	Qty            decimal.Decimal `json:"qty"`
}

type SourceContainer struct {
	SSCC              string                `json:"sscc"`
	Status            string                `json:"status"`
	GrossKg           decimal.Decimal       `json:"grossKg"`
	ParentContainerID *string               `json:"parentContainerId"`
	Lines             []SourceContainerLine `json:"lines"`
}

type SourceShipment struct {
	ID             string            `json:"id"`
	ShipmentNumber string            `json:"shipmentNumber"`
	LegalEntityID  string            `json:"legalEntityId"`
	OrderID        string            `json:"orderId"`
	CustomerID     string            `json:"customerId"`
	Status         string            `json:"status"`
	ActualShipDate *time.Time        `json:"actualShipDate"`
	CarrierCode    *string           `json:"carrierCode"`
	Lines          []SourceShipLine  `json:"lines"`
	Containers     []SourceContainer `json:"containers"`
}

type SourceOrderLine struct {
	ID        string          `json:"id"`
	LineNo    int             `json:"lineNo"`
	Unit      string          `json:"unit"`
	UnitPrice decimal.Decimal `json:"unitPrice"`
}

type SourceOrder struct {
	ID            string            `json:"id"`
	LegalEntityID string            `json:"legalEntityId"`
	CustomerID    string            `json:"customerId"`
	ExternalRef   string            `json:"externalRef"`
	CreatedAt     time.Time         `json:"createdAt"`
	Lines         []SourceOrderLine `json:"lines"`
}

type SourceInvoiceLine struct {
	LineNo      int             `json:"lineNo"`
	Description string          `json:"description"`
	Qty         decimal.Decimal `json:"qty"`
	UnitPrice   decimal.Decimal `json:"unitPrice"`
	Net         decimal.Decimal `json:"net"`
	Tax         decimal.Decimal `json:"tax"`
	OrderID     string          `json:"orderId"`
	OrderLineID string          `json:"orderLineId"`
	ProductID   string          `json:"productId"`
}

type SourceInvoiceTotals struct {
	Net     decimal.Decimal `json:"net"`
	Charges decimal.Decimal `json:"charges"`
	Tax     decimal.Decimal `json:"tax"`
	Gross   decimal.Decimal `json:"gross"`
}

type SourceInvoice struct {
	ID              string              `json:"id"`
	LegalEntityID   string              `json:"legalEntityId"`
	CustomerID      string              `json:"customerId"`
	Type            string              `json:"type"`
	Status          string              `json:"status"`
	Number          string              `json:"number"`
	Currency        string              `json:"currency"`
	InvoiceDate     string              `json:"invoiceDate"`
	DueDate         string              `json:"dueDate"`
	PaymentTermDays int                 `json:"paymentTermDays"`
	Totals          SourceInvoiceTotals `json:"totals"`
	Lines           []SourceInvoiceLine `json:"lines"`
}

func checkScope(p TradeProfile, legalEntityID, customerID string) error {
	if legalEntityID != p.LegalEntityID || customerID != p.CustomerID {
		return ErrSourceScope
	}
	return nil
}

func orderLines(o SourceOrder) map[string]SourceOrderLine {
	byID := make(map[string]SourceOrderLine, len(o.Lines))
	for _, l := range o.Lines {
		byID[l.ID] = l
	}
	return byID
}

func orderFor(p TradeProfile, o SourceOrder, orderID string) error {
	if o.ID != orderID {
		return outboundf("sales order %s is not the order of the source document", o.ID)
	}
	if err := checkScope(p, o.LegalEntityID, o.CustomerID); err != nil {
		return err
	}
	if o.ExternalRef == "" {
		return outboundf("sales order %s has no customer PO reference (externalRef)", o.ID)
	}
	return nil
}

func ValidateShipmentSource(p TradeProfile, sh SourceShipment) error {
	if err := checkScope(p, sh.LegalEntityID, sh.CustomerID); err != nil {
		return err
	}
	switch sh.Status {
	case "SHIPPED", "DELIVERED":
	case "PARTIALLY_SHIPPED":
		return outboundf("shipment %s is partially shipped; one 856 is sent per shipment once it is fully shipped", sh.ShipmentNumber)
	default:
		return fmt.Errorf("%w: shipment %s is %s, not shipped", ErrSourceNotReady, sh.ShipmentNumber, sh.Status)
	}
	if sh.ActualShipDate == nil {
		return fmt.Errorf("%w: shipment %s has no actual ship date", ErrSourceNotReady, sh.ShipmentNumber)
	}
	return nil
}

func ValidateInvoiceSource(p TradeProfile, inv SourceInvoice) error {
	if err := checkScope(p, inv.LegalEntityID, inv.CustomerID); err != nil {
		return err
	}
	switch inv.Status {
	case "POSTED", "PARTIALLY_SETTLED", "SETTLED":
		return nil
	}
	return fmt.Errorf("%w: invoice %s is %s, not posted", ErrSourceNotReady, inv.Number, inv.Status)
}

func InvoiceOrderID(inv SourceInvoice) (string, error) {
	switch {
	case len(inv.Lines) == 0:
		return "", outboundf("invoice %s has no lines", inv.Number)
	case inv.Type == "CREDIT_NOTE":
		return "", outboundf("invoice %s is a credit note; credit notes carry no sales order, so no 810 is generated from them", inv.Number)
	case inv.Lines[0].OrderID == "":
		return "", outboundf("invoice %s has no sales order on its lines (free-text invoice); an 810 needs the customer PO", inv.Number)
	}
	return inv.Lines[0].OrderID, nil
}

func compactDate(t time.Time) string { return t.UTC().Format("20060102") }

// ASNFromShipment builds the canonical ASN from a shipped fulfillment shipment
// and its sales order. Only SHIPPED containers become packs; each pack's items
// use the sales order line number and the product id as the vendor part (VP).
func ASNFromShipment(p TradeProfile, sh SourceShipment, o SourceOrder) (ASN, error) {
	if err := ValidateShipmentSource(p, sh); err != nil {
		return ASN{}, err
	}
	if err := orderFor(p, o, sh.OrderID); err != nil {
		return ASN{}, err
	}
	shipLines := make(map[string]SourceShipLine, len(sh.Lines))
	for _, l := range sh.Lines {
		shipLines[l.ID] = l
	}
	poLines := orderLines(o)
	var packs []ASNPack
	gross := decimal.Zero
	topLevel := 0
	for _, c := range sh.Containers {
		if c.Status != "SHIPPED" {
			continue
		}
		if c.ParentContainerID == nil || *c.ParentContainerID == "" {
			gross = gross.Add(c.GrossKg)
			topLevel++
		}
		if len(c.Lines) == 0 {
			continue
		}
		pack := ASNPack{SSCC: c.SSCC}
		for _, cl := range c.Lines {
			sl, ok := shipLines[cl.ShipmentLineID]
			po, okPO := poLines[sl.OrderLineID]
			if !ok || !okPO {
				return ASN{}, outboundf("container %s packs a line that is not on the shipment or its sales order", c.SSCC)
			}
			pack.Items = append(pack.Items, ASNItem{LineNo: strconv.Itoa(po.LineNo), Quantity: cl.Qty, UOM: sl.Unit,
				ItemRefs: []ItemRef{{Qualifier: "VP", Value: sl.ProductID}}})
		}
		packs = append(packs, pack)
	}
	if len(packs) == 0 {
		return ASN{}, fmt.Errorf("%w: shipment %s has no shipped containers", ErrSourceNotReady, sh.ShipmentNumber)
	}
	scac := p.DefaultSCAC
	if sh.CarrierCode != nil && len(*sh.CarrierCode) >= 2 && len(*sh.CarrierCode) <= 4 {
		scac = *sh.CarrierCode
	}
	at := sh.ActualShipDate.UTC()
	return ASN{
		ShipmentID: sh.ShipmentNumber, ShipDate: compactDate(at), ShipTime: at.Format("1504"),
		SCAC: scac, GrossWeight: gross, LadingQuantity: topLevel, WeightUnit: "KG", ShipFrom: p.ShipFrom, ShipTo: p.ShipTo,
		Orders: []ASNOrder{{PONumber: o.ExternalRef, PODate: compactDate(o.CreatedAt), Packs: packs}},
	}, nil
}

// InvoiceFromBilling builds the canonical 810 invoice from a posted billing
// invoice and its single sales order. Line discounts become one allowance (SAC
// C310) so quantity times unit price stays the IT1 extension; header charges
// are one SAC ZZZZ; tax is one TXI ST with the effective percent.
func InvoiceFromBilling(p TradeProfile, inv SourceInvoice, o SourceOrder) (Invoice, error) {
	if err := ValidateInvoiceSource(p, inv); err != nil {
		return Invoice{}, err
	}
	orderID, err := InvoiceOrderID(inv)
	if err != nil {
		return Invoice{}, err
	}
	if err := orderFor(p, o, orderID); err != nil {
		return Invoice{}, err
	}
	poLines := orderLines(o)
	date, err := time.Parse("2006-01-02", inv.InvoiceDate)
	if err != nil {
		return Invoice{}, outboundf("invoice %s has no usable invoice date", inv.Number)
	}
	out := Invoice{
		Number: inv.Number, Date: compactDate(date), PONumber: o.ExternalRef, PODate: compactDate(o.CreatedAt),
		CreditNote: inv.Type == "CREDIT_NOTE",
		Terms:      Terms{Code: "01", NetDays: strconv.Itoa(inv.PaymentTermDays)},
		RemitTo:    p.RemitTo, BillTo: p.BillTo, ShipTo: p.ShipTo, Total: inv.Totals.Gross,
	}
	extension, net, tax := decimal.Zero, decimal.Zero, decimal.Zero
	for _, l := range inv.Lines {
		if l.OrderID != orderID {
			return Invoice{}, outboundf("invoice %s spans more than one sales order", inv.Number)
		}
		po, ok := poLines[l.OrderLineID]
		if !ok {
			return Invoice{}, outboundf("invoice line %d has no line on sales order %s", l.LineNo, o.ID)
		}
		out.Lines = append(out.Lines, InvoiceLine{LineNo: strconv.Itoa(l.LineNo), Quantity: l.Qty, UOM: po.Unit, UnitPrice: l.UnitPrice,
			ItemRefs: []ItemRef{{Qualifier: "VP", Value: l.ProductID}}})
		extension = extension.Add(l.Qty.Mul(l.UnitPrice).Round(2))
		net = net.Add(l.Net)
		tax = tax.Add(l.Tax)
	}
	if discount := extension.Sub(net); discount.IsPositive() {
		out.Charges = append(out.Charges, InvoiceCharge{Indicator: "A", Code: "C310", Amount: discount})
	}
	if inv.Totals.Charges.IsPositive() {
		out.Charges = append(out.Charges, InvoiceCharge{Indicator: "C", Code: "ZZZZ", Amount: inv.Totals.Charges})
	}
	if tax.IsPositive() {
		percent := decimal.Zero
		if net.IsPositive() {
			percent = tax.Div(net).Mul(decimal.NewFromInt(100)).Round(4)
		}
		out.Taxes = append(out.Taxes, TaxLine{Type: "ST", Amount: tax, Percent: percent})
	}
	return out, nil
}
