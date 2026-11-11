package domain

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// Outbound cXML (#35, 35-4): the documents we send to a network. OrderRequest goes buyer to supplier from a purchase order;
// ConfirmationRequest, ShipNoticeRequest and InvoiceDetailRequest go supplier to buyer from a sales order, a fulfillment shipment
// and a billing invoice. Every document is wrapped in the cXML envelope with our network identity and shared secret, and carries a
// payloadID that is new for each document (BR-35.14: a payloadID is never reused with other content).
const (
	CXMLOrderRequest        = "OrderRequest"
	CXMLConfirmationRequest = "ConfirmationRequest"
	CXMLShipNoticeRequest   = "ShipNoticeRequest"
	CXMLInvoiceDetail       = "InvoiceDetailRequest"
)

// CXMLOutboundTypes are the documents the engine sends.
var CXMLOutboundTypes = []string{CXMLOrderRequest, CXMLConfirmationRequest, CXMLShipNoticeRequest, CXMLInvoiceDetail}

// ErrCXMLOutbound marks a document that cannot be built from its source.
var ErrCXMLOutbound = errors.New("cxml outbound")

func cxmlErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCXMLOutbound, fmt.Sprintf(format, args...))
}

// CXMLOrderLine is one line of a purchase order sent as ItemOut.
type CXMLOrderLine struct {
	LineNo       int
	ProductID    string
	Description  string
	Qty          string
	Unit         string
	UnitPrice    string
	DeliveryDate string
}

// CXMLOrder is a purchase order as the OrderRequest needs it.
type CXMLOrder struct {
	Number   string
	Date     time.Time
	Currency string
	Total    string
	Lines    []CXMLOrderLine
}

// CXMLCredentials are our identity on the network and the shared secret the network knows us by, and the partner's identity.
type CXMLCredentials struct {
	From, To, Secret string
}

func xmlAttr(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&apos;")
		default:
			if r >= 32 || r == '\t' || r == '\n' {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

var sharedSecretElement = regexp.MustCompile(`<SharedSecret>[^<]*</SharedSecret>`)

// RedactCXMLSecret returns the document with every SharedSecret element emptied, whatever characters the secret held (the sent copy
// carries it XML-escaped, so it cannot be found by searching for the raw secret). It is the copy that is stored.
func RedactCXMLSecret(doc string) string {
	return sharedSecretElement.ReplaceAllString(doc, "<SharedSecret>[redacted]</SharedSecret>")
}

func cxmlWrap(c CXMLCredentials, payloadID string, at time.Time, body string) string {
	cred := func(id string) string {
		return `<Credential domain="NetworkId"><Identity>` + xmlAttr(id) + `</Identity></Credential>`
	}
	return `<?xml version="1.0" encoding="UTF-8"?>` + "\n" +
		`<cXML payloadID="` + xmlAttr(payloadID) + `" timestamp="` + at.UTC().Format(time.RFC3339) + `">` +
		`<Header><From>` + cred(c.From) + `</From><To>` + cred(c.To) + `</To><Sender><Credential domain="NetworkId"><Identity>` + xmlAttr(c.From) +
		`</Identity><SharedSecret>` + xmlAttr(c.Secret) + `</SharedSecret></Credential><UserAgent>Passage EDI</UserAgent></Sender></Header>` +
		`<Request deploymentMode="production">` + body + `</Request></cXML>`
}

func money(cur string, v string) string {
	return `<Money currency="` + xmlAttr(cur) + `">` + xmlAttr(v) + `</Money>`
}

func uom(u string) string { return "<UnitOfMeasure>" + xmlAttr(u) + "</UnitOfMeasure>" }

func ymd(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05Z") }

// BuildCXMLOrderRequest is the OrderRequest of a purchase order. orderID is the PO number; the confirmation and the ship notice of the
// supplier name it, which is how they find the order again.
func BuildCXMLOrderRequest(c CXMLCredentials, payloadID string, at time.Time, o CXMLOrder) (string, error) {
	if o.Number == "" || len(o.Lines) == 0 || o.Currency == "" {
		return "", cxmlErr("a purchase order needs its number, currency and at least one line")
	}
	var b strings.Builder
	b.WriteString(`<OrderRequest><OrderRequestHeader orderID="` + xmlAttr(o.Number) + `" orderDate="` + ymd(o.Date) + `" type="new"><Total>` + money(o.Currency, o.Total) + `</Total></OrderRequestHeader>`)
	for _, l := range o.Lines {
		if l.Qty == "" || l.Unit == "" || l.UnitPrice == "" {
			return "", cxmlErr("purchase order line %d needs quantity, unit and unit price", l.LineNo)
		}
		desc := l.Description
		if desc == "" {
			desc = "Line " + strconv.Itoa(l.LineNo)
		}
		b.WriteString(`<ItemOut quantity="` + xmlAttr(l.Qty) + `" lineNumber="` + strconv.Itoa(l.LineNo) + `"`)
		if l.DeliveryDate != "" {
			b.WriteString(` requestedDeliveryDate="` + xmlAttr(l.DeliveryDate) + `"`)
		}
		b.WriteString(`><ItemID><SupplierPartID>` + xmlAttr(l.ProductID) + `</SupplierPartID></ItemID><ItemDetail><UnitPrice>` + money(o.Currency, l.UnitPrice) +
			`</UnitPrice><Description xml:lang="en">` + xmlAttr(desc) + `</Description>` + uom(l.Unit) + `</ItemDetail></ItemOut>`)
	}
	b.WriteString(`</OrderRequest>`)
	return cxmlWrap(c, payloadID, at, b.String()), nil
}

// BuildCXMLConfirmation is the ConfirmationRequest of a sales order that came from the partner (orderID is the partner's PO number,
// the order's externalRef): every line accepted as ordered.
func BuildCXMLConfirmation(c CXMLCredentials, payloadID string, at time.Time, o SourceOrder, confirmID string) (string, error) {
	if o.ExternalRef == "" || len(o.Lines) == 0 {
		return "", cxmlErr("sales order %s has no customer PO reference or no lines", o.ID)
	}
	var b strings.Builder
	b.WriteString(`<ConfirmationRequest><ConfirmationHeader type="accept" noticeDate="` + ymd(at) + `" confirmID="` + xmlAttr(confirmID) + `"/>` +
		`<OrderReference orderID="` + xmlAttr(o.ExternalRef) + `"/>`)
	for _, l := range o.Lines {
		if !l.Quantity.IsPositive() {
			continue
		}
		q := l.Quantity.String()
		b.WriteString(`<ConfirmationItem lineNumber="` + strconv.Itoa(l.LineNo) + `" quantity="` + q + `">` + uom(l.Unit) +
			`<ConfirmationStatus type="accept" quantity="` + q + `">` + uom(l.Unit) + `</ConfirmationStatus></ConfirmationItem>`)
	}
	b.WriteString(`</ConfirmationRequest>`)
	return cxmlWrap(c, payloadID, at, b.String()), nil
}

// BuildCXMLShipNotice is the ShipNoticeRequest of a shipped fulfillment shipment (shipmentID is the shipment number, orderID the
// partner's PO number). One ShipNoticeItem per shipped line, quantity summed over the containers.
func BuildCXMLShipNotice(c CXMLCredentials, payloadID string, at time.Time, sh SourceShipment, o SourceOrder) (string, error) {
	if sh.ActualShipDate == nil {
		return "", cxmlErr("shipment %s has no actual ship date", sh.ShipmentNumber)
	}
	if o.ExternalRef == "" {
		return "", cxmlErr("sales order %s has no customer PO reference", o.ID)
	}
	byLine := map[string]decimal.Decimal{}
	var order []string
	for _, ct := range sh.Containers {
		if ct.Status != "SHIPPED" {
			continue
		}
		for _, cl := range ct.Lines {
			if _, seen := byLine[cl.ShipmentLineID]; !seen {
				order = append(order, cl.ShipmentLineID)
			}
			byLine[cl.ShipmentLineID] = byLine[cl.ShipmentLineID].Add(cl.Qty)
		}
	}
	if len(order) == 0 {
		return "", cxmlErr("shipment %s has no shipped containers", sh.ShipmentNumber)
	}
	shipLines := map[string]SourceShipLine{}
	for _, l := range sh.Lines {
		shipLines[l.ID] = l
	}
	poLines := orderLines(o)
	var b strings.Builder
	b.WriteString(`<ShipNoticeRequest><ShipNoticeHeader shipmentID="` + xmlAttr(sh.ShipmentNumber) + `" noticeDate="` + ymd(at) + `" shipmentDate="` + ymd(*sh.ActualShipDate) + `"/>` +
		`<ShipControl><CarrierIdentifier domain="SCAC">` + xmlAttr(carrierOf(sh)) + `</CarrierIdentifier><ShipmentIdentifier>` + xmlAttr(sh.ShipmentNumber) + `</ShipmentIdentifier></ShipControl>` +
		`<ShipNoticePortion><OrderReference orderID="` + xmlAttr(o.ExternalRef) + `"/>`)
	for _, id := range order {
		sl, ok := shipLines[id]
		po, okPO := poLines[sl.OrderLineID]
		if !ok || !okPO {
			return "", cxmlErr("shipment %s ships a line that is not on the shipment or its sales order", sh.ShipmentNumber)
		}
		b.WriteString(`<ShipNoticeItem lineNumber="` + strconv.Itoa(po.LineNo) + `" quantity="` + byLine[id].String() + `"><ItemID><SupplierPartID>` + xmlAttr(sl.ProductID) +
			`</SupplierPartID></ItemID>` + uom(sl.Unit) + `</ShipNoticeItem>`)
	}
	b.WriteString(`</ShipNoticePortion></ShipNoticeRequest>`)
	return cxmlWrap(c, payloadID, at, b.String()), nil
}

func carrierOf(sh SourceShipment) string {
	if sh.CarrierCode != nil && *sh.CarrierCode != "" {
		return *sh.CarrierCode
	}
	return "UNKNOWN"
}

// BuildCXMLInvoice is the InvoiceDetailRequest of a posted billing invoice (orderID is the partner's PO number). Lines carry the
// quantity, unit price and net amount; the tax is the one TaxTotal of the summary at the effective rate.
func BuildCXMLInvoice(c CXMLCredentials, payloadID string, at time.Time, inv SourceInvoice, orderRef string) (string, error) {
	if _, err := ValidateInvoiceStatus(inv); err != nil {
		return "", err
	}
	if orderRef == "" || len(inv.Lines) == 0 || inv.Currency == "" {
		return "", cxmlErr("invoice %s needs the customer PO reference, a currency and lines", inv.Number)
	}
	purpose := "standard"
	if inv.Type == "CREDIT_NOTE" {
		purpose = "lineLevelCreditMemo"
	}
	var b strings.Builder
	b.WriteString(`<InvoiceDetailRequest><InvoiceDetailRequestHeader invoiceID="` + xmlAttr(inv.Number) + `" purpose="` + purpose + `" operation="new" invoiceDate="` + xmlAttr(inv.InvoiceDate) + `T00:00:00Z"/>` +
		`<InvoiceDetailOrder><InvoiceDetailOrderInfo><OrderReference orderID="` + xmlAttr(orderRef) + `"/></InvoiceDetailOrderInfo>`)
	net := decimal.Zero
	for _, l := range inv.Lines {
		net = net.Add(l.Net)
		b.WriteString(`<InvoiceDetailItem invoiceLineNumber="` + strconv.Itoa(l.LineNo) + `" quantity="` + l.Qty.String() + `">` + uom("EA") +
			`<UnitPrice>` + money(inv.Currency, l.UnitPrice.String()) + `</UnitPrice><InvoiceDetailItemReference lineNumber="` + strconv.Itoa(l.LineNo) +
			`"><Description xml:lang="en">` + xmlAttr(l.Description) + `</Description></InvoiceDetailItemReference><SubtotalAmount>` + money(inv.Currency, l.Net.StringFixed(2)) + `</SubtotalAmount></InvoiceDetailItem>`)
	}
	b.WriteString(`</InvoiceDetailOrder><InvoiceDetailSummary><SubtotalAmount>` + money(inv.Currency, net.StringFixed(2)) + `</SubtotalAmount><Tax>` +
		money(inv.Currency, inv.Totals.Tax.StringFixed(2)) + `<Description xml:lang="en">VAT</Description></Tax><GrossAmount>` + money(inv.Currency, inv.Totals.Gross.StringFixed(2)) +
		`</GrossAmount><NetAmount>` + money(inv.Currency, inv.Totals.Gross.StringFixed(2)) + `</NetAmount></InvoiceDetailSummary></InvoiceDetailRequest>`)
	return cxmlWrap(c, payloadID, at, b.String()), nil
}
