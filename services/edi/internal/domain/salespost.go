package domain

import (
	"fmt"
	"strings"
	"time"
)

const (
	CodeNoSalesDefaults = "EDI-0020"
	CodeOrderUnpostable = "EDI-0022"
	CodeSalesRefused    = "EDI-0023"
	CodeUnitUnmapped    = "EDI-0024"

	SalesChannel = "EDI"
)

var xrefQualifiers = map[string]bool{"BP": true, "VP": true, "UP": true, "EN": true}

type ItemXref struct {
	ID          string `json:"id,omitempty"`
	Qualifier   string `json:"qualifier"`
	PartnerItem string `json:"partnerItem"`
	ProductID   string `json:"productId"`
	SiteID      string `json:"siteId"`
	WarehouseID string `json:"warehouseId"`
	Version     int    `json:"version,omitempty"`
}

func (x ItemXref) Validate() map[string]string {
	f := map[string]string{}
	if !xrefQualifiers[x.Qualifier] {
		f["qualifier"] = "must be BP, VP, UP or EN"
	}
	if n := len(x.PartnerItem); n == 0 || n > 48 || x.PartnerItem != strings.TrimSpace(x.PartnerItem) {
		f["partnerItem"] = "must be 1 to 48 characters without surrounding spaces"
	}
	for name, v := range map[string]string{"productId": x.ProductID, "siteId": x.SiteID, "warehouseId": x.WarehouseID} {
		if !uuidShaped(v) {
			f[name] = "must be a UUID"
		}
	}
	return f
}

// SalesDefaults are the partner's order defaults. AutoPost (on when absent) lets the background poster post the partner's
// mapped orders; UnitMap turns the partner's units of measure into the ones sales knows.
type SalesDefaults struct {
	InvoiceAccountID string            `json:"invoiceAccountId"`
	Currency         string            `json:"currency"`
	AutoPost         *bool             `json:"autoPost,omitempty"`
	UnitMap          map[string]string `json:"unitMap"`
}

func (d SalesDefaults) AutoPosts() bool { return d.AutoPost == nil || *d.AutoPost }

func (d SalesDefaults) Validate() map[string]string {
	f := map[string]string{}
	if !uuidShaped(d.InvoiceAccountID) {
		f["invoiceAccountId"] = "must be a UUID"
	}
	if !currencyRe.MatchString(d.Currency) {
		f["currency"] = "must be an ISO 4217 code"
	}
	if len(d.UnitMap) > 200 {
		f["unitMap"] = "must hold at most 200 units"
	}
	for from, to := range d.UnitMap {
		if n := len(from); n < 1 || n > 3 || from != strings.TrimSpace(from) {
			f["unitMap"] = "keys must be 1 to 3 characters (the partner's unit code) without surrounding spaces"
		}
		if n := len(to); n < 1 || n > 10 || to != strings.TrimSpace(to) {
			f["unitMap"] = "values must be 1 to 10 characters (our unit) without surrounding spaces"
		}
	}
	return f
}

type SalesOrderLine struct {
	ProductID         string `json:"productId"`
	Quantity          string `json:"quantity"`
	Unit              string `json:"unit"`
	SiteID            string `json:"siteId"`
	WarehouseID       string `json:"warehouseId"`
	UnitPrice         string `json:"unitPrice"`
	RequestedShipDate string `json:"requestedShipDate"`
}

// SalesOrderRequest is the body of POST /v1/sales/orders for channel EDI.
type SalesOrderRequest struct {
	LegalEntityID     string           `json:"legalEntityId"`
	CustomerID        string           `json:"customerId"`
	InvoiceAccountID  string           `json:"invoiceAccountId"`
	Currency          string           `json:"currency"`
	RequestedShipDate string           `json:"requestedShipDate"`
	Channel           string           `json:"channel"`
	ExternalRef       string           `json:"externalRef"`
	Lines             []SalesOrderLine `json:"lines"`
}

type SalesOrderRef struct {
	ID string `json:"id"`
}

// Park is why an order cannot be posted; the message is parked with it.
type Park struct{ Code, Detail string }

func (p Park) Error() string { return p.Code + ": " + p.Detail }

// SalesExternalRef scopes the PO number to the partner, so two partners that
// number their orders alike do not collide in sales' (channel, externalRef) dedup.
func SalesExternalRef(partnerID, poNumber string) string { return partnerID + ":" + poNumber }

func ediDate(s string) (string, bool) {
	t, err := time.Parse("20060102", s)
	if err != nil {
		return "", false
	}
	return t.Format("2006-01-02"), true
}

// BuildSalesOrder maps the canonical order onto the sales intake request. Only
// an original order (BEG01 00) is posted: a replace or change belongs to sales'
// change flow. Every line must resolve through xrefs to a product, and the
// order needs a requested ship or delivery date; every unit of measure must be in the partner's unit map; anything else is a Park.
func BuildSalesOrder(partnerID string, o Order, p TradeProfile, d SalesDefaults, xrefs map[ItemRef]ItemXref) (SalesOrderRequest, *Park) {
	if o.Purpose != "00" {
		return SalesOrderRequest{}, &Park{CodeOrderUnpostable, "BEG01 purpose " + o.Purpose + " is a change or cancellation; only an original order (00) is posted"}
	}
	if o.OrderType == "BK" || o.OrderType == "RL" {
		return SalesOrderRequest{}, &Park{CodeOrderUnpostable, "BEG02 order type " + o.OrderType + " is a blanket or release; only a stand-alone order (SA) is posted"}
	}
	date, ok := ediDate(o.RequestedShip)
	if !ok {
		if date, ok = ediDate(o.RequestedDelivery); !ok {
			return SalesOrderRequest{}, &Park{CodeOrderUnpostable, "the order has no requested ship (DTM 010) or delivery (DTM 002) date"}
		}
	}
	currency := o.Currency
	if currency == "" {
		currency = d.Currency
	}
	req := SalesOrderRequest{LegalEntityID: p.LegalEntityID, CustomerID: p.CustomerID, InvoiceAccountID: d.InvoiceAccountID,
		Currency: currency, RequestedShipDate: date, Channel: SalesChannel, ExternalRef: SalesExternalRef(partnerID, o.PONumber)}
	var missing, unmapped []string
	for _, l := range o.Lines {
		if l.ItemRef == nil {
			missing = append(missing, "line "+l.LineNo+" (no item reference)")
			continue
		}
		x, found := xrefs[*l.ItemRef]
		if !found {
			missing = append(missing, fmt.Sprintf("line %s (%s %s)", l.LineNo, l.ItemRef.Qualifier, l.ItemRef.Value))
			continue
		}
		unit, mapped := d.UnitMap[l.UOM]
		if !mapped {
			unmapped = append(unmapped, fmt.Sprintf("line %s (%q)", l.LineNo, l.UOM))
			continue
		}
		req.Lines = append(req.Lines, SalesOrderLine{ProductID: x.ProductID, Quantity: l.Quantity, Unit: unit, SiteID: x.SiteID,
			WarehouseID: x.WarehouseID, UnitPrice: l.UnitPrice, RequestedShipDate: date})
	}
	if len(missing) > 0 {
		return SalesOrderRequest{}, &Park{CodeNoItemRef, "no item cross-reference for " + strings.Join(missing, ", ")}
	}
	if len(unmapped) > 0 {
		return SalesOrderRequest{}, &Park{CodeUnitUnmapped, "no unit of measure mapping in the partner's sales defaults for " + strings.Join(unmapped, ", ")}
	}
	return req, nil
}
