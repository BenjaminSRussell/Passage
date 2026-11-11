package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// Customer portal routes (#30): a customer's own orders and returns. The customer comes from the verified customer-portal
// service token (the principal's CustomerID) and never from the request: a `customerId` query parameter on these routes is
// ignored, and an order or return of another customer, or an order still in DRAFT, reads as not found. The duties are
// service-only. The answers are the customer's view: no holds or credit check, no costs, no warehouse or site, no versions.
//
// The portal also writes, for the customer itself and nobody else (FR-30.14): it places an order and requests a return. Both
// take the customer from the token and refuse a body that names one. An order is a reorder: the sales service holds no price
// list, so every line takes its price, unit, site and warehouse from the customer's own last committed order of that product, and
// a product the customer never bought is refused. The order is committed through the normal confirm, so the credit check and the
// holds apply as for any order. A return names one of the customer's own posted invoices; the invoice's customer is checked
// against the token's by the return-order flow.
const (
	dutyCustomerPortal       = "sales.customer-portal.read"
	dutyCustomerPortalOrder  = "sales.customer-portal.order"
	dutyCustomerPortalReturn = "sales.customer-portal.return"
	customerPortal           = "/v1/sales/customer-portal"

	codePortalProduct = "SAL-CUS-002"
	codePortalInvoice = "SAL-CUS-003"
	// portalChannel is the order channel of a portal order, suffixed with the customer so the (channel, external reference) and
	// idempotency-key dedup of intake can never match another customer's order.
	portalChannel    = "PORTAL-"
	maxPortalLines   = 100
	portalHistoryMax = 2000
)

// ProductNamer reads product display names from the product service, which owns them.
type ProductNamer interface {
	ProductNames(ctx context.Context, legalEntityID string, productIDs []string) (map[string]string, error)
}

// WithProductNames sets the product-name source; without it the portal views carry no product names.
func (a *API) WithProductNames(n ProductNamer) *API {
	a.names = n
	return a
}

func (a *API) registerCustomerPortal(r chi.Router) {
	read := authz.RequireDuty(dutyCustomerPortal)
	r.With(authz.RequireDuty(dutyCustomerPortalOrder)).Post(customerPortal+"/orders", portalCustomerOnly(a.portalPlaceOrder))
	r.With(authz.RequireDuty(dutyCustomerPortalReturn)).Post(customerPortal+"/returns", portalCustomerOnly(a.portalRequestReturn))
	r.With(read).Get(customerPortal+"/products", portalCustomerOnly(a.portalListProducts))
	r.With(read).Get(customerPortal+"/orders", portalCustomerOnly(a.portalListOrders))
	r.With(read).Get(customerPortal+"/orders/{id}", portalCustomerOnly(a.portalGetOrder))
	r.With(read).Get(customerPortal+"/returns", portalCustomerOnly(a.portalListReturns))
	r.With(read).Get(customerPortal+"/returns/{id}", portalCustomerOnly(a.portalGetReturn))
}

type portalHandler func(w http.ResponseWriter, r *http.Request, customer string)

// portalCustomerOnly refuses any caller that is not the customer portal acting for a customer, and hands the customer from the
// verified principal to the handler.
func portalCustomerOnly(next portalHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		customer, ok := tenant.PortalCustomer(r.Context())
		if !ok || !ids.Valid(customer) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "SAL-CUS-001", "this account is not a customer account"))
			return
		}
		if !validTenant(w, r) {
			return
		}
		next(w, r, customer)
	}
}

type portalOrder struct {
	ID                string          `json:"id"`
	OrderNumber       string          `json:"orderNumber"`
	OrderType         string          `json:"orderType"`
	Status            string          `json:"status"`
	Currency          string          `json:"currency"`
	RequestedShipDate string          `json:"requestedShipDate"`
	ExternalRef       *string         `json:"externalRef,omitempty"`
	NetTotal          decimal.Decimal `json:"netTotal"`
	LineCount         int             `json:"lineCount,omitempty"`
	CreatedAt         time.Time       `json:"createdAt"`
	Lines             []portalLine    `json:"lines,omitempty"`
}

type portalLine struct {
	LineNo            int             `json:"lineNo"`
	ProductID         string          `json:"productId"`
	ProductName       string          `json:"productName,omitempty"`
	Quantity          decimal.Decimal `json:"quantity"`
	Unit              string          `json:"unit"`
	UnitPrice         decimal.Decimal `json:"unitPrice"`
	LineNet           decimal.Decimal `json:"lineNet"`
	RequestedShipDate string          `json:"requestedShipDate"`
	ConfirmedShipDate *string         `json:"confirmedShipDate,omitempty"`
	LineStatus        string          `json:"lineStatus"`
	DeliveredQty      decimal.Decimal `json:"deliveredQty"`
	InvoicedQty       decimal.Decimal `json:"invoicedQty"`
}

// visibleOrder is true for an order that is the customer's and past DRAFT: a draft is internal until it is committed.
func visibleOrder(customerID, status, customer string) bool {
	return strings.EqualFold(customerID, customer) && status != "DRAFT"
}

func (a *API) portalListOrders(w http.ResponseWriter, r *http.Request, customer string) {
	q := r.URL.Query()
	f := store.OrderFilter{CustomerID: customer, ExcludeDraft: true, Status: q.Get("status"), Before: q.Get("cursor")}
	fields := map[string]string{}
	if f.Status != "" && (!orderStatuses[f.Status] || f.Status == "DRAFT") {
		fields["status"] = "must be an order status such as OPEN or CONFIRMED"
	}
	if f.Before != "" && !ids.Valid(f.Before) {
		fields["cursor"] = "must be the nextCursor of the previous page"
	}
	limit, ok := pageLimit(q.Get("limit"))
	if !ok {
		fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPageSize)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	f.Limit = limit
	page, err := a.store.ListOrders(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list customer portal orders", err)
		return
	}
	out := struct {
		Items      []portalOrder `json:"items"`
		NextCursor string        `json:"nextCursor,omitempty"`
	}{Items: make([]portalOrder, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, o := range page.Items {
		// Belt and braces: the filter narrows the query, and a row of another customer or a draft is still never sent.
		if visibleOrder(o.CustomerID, o.Status, customer) {
			out.Items = append(out.Items, portalOrder{ID: o.ID, OrderNumber: o.OrderNumber, OrderType: o.OrderType, Status: o.Status, Currency: o.Currency,
				RequestedShipDate: o.RequestedShipDate, ExternalRef: o.ExternalRef, NetTotal: o.NetTotal, LineCount: o.LineCount, CreatedAt: o.CreatedAt})
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) portalGetOrder(w http.ResponseWriter, r *http.Request, customer string) {
	notFound := httpx.NewProblem(http.StatusNotFound, store.CodeOrderNotFound, "order not found")
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, notFound)
		return
	}
	o, err := a.store.GetOrder(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get customer portal order", err)
		return
	}
	if !visibleOrder(o.CustomerID, o.Status, customer) {
		httpx.Fail(w, r, notFound)
		return
	}
	out := toPortalOrder(o)
	a.nameOrderLines(r.Context(), &out)
	httpx.JSON(w, http.StatusOK, out)
}

func toPortalOrder(o store.Order) portalOrder {
	out := portalOrder{ID: o.ID, OrderNumber: o.OrderNumber, OrderType: o.OrderType, Status: o.Status, Currency: o.Currency,
		RequestedShipDate: o.RequestedShipDate, ExternalRef: o.ExternalRef, NetTotal: o.NetTotal, LineCount: len(o.Lines), CreatedAt: o.CreatedAt,
		Lines: make([]portalLine, 0, len(o.Lines))}
	for _, l := range o.Lines {
		out.Lines = append(out.Lines, portalLine{LineNo: l.LineNo, ProductID: l.ProductID, Quantity: l.Quantity, Unit: l.Unit, UnitPrice: l.UnitPrice, LineNet: l.LineNet,
			RequestedShipDate: l.RequestedShipDate, ConfirmedShipDate: l.ConfirmedShipDate, LineStatus: l.LineStatus,
			DeliveredQty: l.DeliveredQty, InvoicedQty: l.InvoicedQty})
	}
	return out
}

// productNames answers the display names of ids, or an empty map when the product service is not wired or cannot answer: a
// screen without names is still correct, so a name read never fails a request.
func (a *API) productNames(ctx context.Context, ids []string) map[string]string {
	if a.names == nil || len(ids) == 0 {
		return map[string]string{}
	}
	p, _ := tenant.FromContext(ctx)
	names, err := a.names.ProductNames(ctx, p.LegalEntityID, ids)
	if err != nil {
		slog.WarnContext(ctx, "product names unavailable for the customer portal", "err", err)
		return map[string]string{}
	}
	return names
}

func (a *API) nameOrderLines(ctx context.Context, o *portalOrder) {
	seen := map[string]bool{}
	var want []string
	for _, l := range o.Lines {
		if !seen[l.ProductID] {
			seen[l.ProductID] = true
			want = append(want, l.ProductID)
		}
	}
	names := a.productNames(ctx, want)
	for i := range o.Lines {
		o.Lines[i].ProductName = names[strings.ToLower(o.Lines[i].ProductID)]
	}
}

func (a *API) nameReturnLines(ctx context.Context, rs []portalReturn) {
	seen := map[string]bool{}
	var want []string
	for _, r := range rs {
		for _, l := range r.Lines {
			if !seen[l.ProductID] {
				seen[l.ProductID] = true
				want = append(want, l.ProductID)
			}
		}
	}
	names := a.productNames(ctx, want)
	for i := range rs {
		for j := range rs[i].Lines {
			rs[i].Lines[j].ProductName = names[strings.ToLower(rs[i].Lines[j].ProductID)]
		}
	}
}

type portalReturnLine struct {
	LineNo           int             `json:"lineNo"`
	ProductID        string          `json:"productId"`
	ProductName      string          `json:"productName,omitempty"`
	QuantityExpected decimal.Decimal `json:"quantityExpected"`
	QuantityReceived decimal.Decimal `json:"quantityReceived"`
	QuantityAccepted decimal.Decimal `json:"quantityAccepted"`
	Unit             string          `json:"unit"`
	UnitPrice        decimal.Decimal `json:"unitPrice"`
}

type portalReturn struct {
	ID                    string             `json:"id"`
	RMANumber             string             `json:"rmaNumber"`
	OriginalInvoiceNumber string             `json:"originalInvoiceNumber"`
	ReturnReasonCode      string             `json:"returnReasonCode"`
	Status                string             `json:"status"`
	Currency              string             `json:"currency"`
	ExpiresAt             *string            `json:"expiresAt,omitempty"`
	Gross                 decimal.Decimal    `json:"gross"`
	Restocking            decimal.Decimal    `json:"restocking"`
	Net                   decimal.Decimal    `json:"net"`
	Tax                   decimal.Decimal    `json:"tax"`
	CreatedAt             time.Time          `json:"createdAt"`
	Lines                 []portalReturnLine `json:"lines"`
}

func toPortalReturn(o store.ReturnOrder) portalReturn {
	out := portalReturn{ID: o.ID, RMANumber: o.RMANumber, OriginalInvoiceNumber: o.OriginalInvoiceNo, ReturnReasonCode: o.ReturnReasonCode,
		Status: o.Status, Currency: o.Currency, ExpiresAt: o.ExpiresAt, Gross: o.Gross, Restocking: o.Restocking, Net: o.Net, Tax: o.Tax,
		CreatedAt: o.CreatedAt, Lines: make([]portalReturnLine, 0, len(o.Lines))}
	for _, l := range o.Lines {
		out.Lines = append(out.Lines, portalReturnLine{LineNo: l.LineNo, ProductID: l.ProductID, QuantityExpected: l.QuantityExpected, QuantityReceived: l.QuantityReceived,
			QuantityAccepted: l.QuantityAccepted, Unit: l.Unit, UnitPrice: l.UnitPrice})
	}
	return out
}

func (a *API) portalListReturns(w http.ResponseWriter, r *http.Request, customer string) {
	q := r.URL.Query()
	p, _ := tenant.FromContext(r.Context())
	f := store.ReturnOrderFilter{LegalEntityID: p.LegalEntityID, CustomerID: customer, Status: q.Get("status")}
	fields := map[string]string{}
	if f.Status != "" && !returnStatuses[f.Status] {
		fields["status"] = "must be a return order status"
	}
	limit, ok := pageLimit(q.Get("limit"))
	if !ok {
		fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPageSize)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	f.Limit = limit
	rows, err := a.store.ListReturnOrders(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list customer portal returns", err)
		return
	}
	items := make([]portalReturn, 0, len(rows))
	for _, o := range rows {
		if strings.EqualFold(o.CustomerID, customer) {
			items = append(items, toPortalReturn(o))
		}
	}
	a.nameReturnLines(r.Context(), items)
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) portalGetReturn(w http.ResponseWriter, r *http.Request, customer string) {
	notFound := httpx.NewProblem(http.StatusNotFound, store.CodeRMANotFound, "return order not found")
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, notFound)
		return
	}
	o, err := a.store.GetReturnOrder(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get customer portal return", err)
		return
	}
	if !strings.EqualFold(o.CustomerID, customer) {
		httpx.Fail(w, r, notFound)
		return
	}
	out := []portalReturn{toPortalReturn(o)}
	a.nameReturnLines(r.Context(), out)
	httpx.JSON(w, http.StatusOK, out[0])
}
