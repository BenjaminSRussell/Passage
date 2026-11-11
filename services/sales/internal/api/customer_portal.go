package api

import (
	"fmt"
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
// ignored, and an order or return of another customer, or an order still in DRAFT, reads as not found. The duty is service-only
// and read-only. The answers are the customer's view: no holds or credit check, no costs, no warehouse or site, no versions.
const (
	dutyCustomerPortal = "sales.customer-portal.read"
	customerPortal     = "/v1/sales/customer-portal"
)

func (a *API) registerCustomerPortal(r chi.Router) {
	read := authz.RequireDuty(dutyCustomerPortal)
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
	out := portalOrder{ID: o.ID, OrderNumber: o.OrderNumber, OrderType: o.OrderType, Status: o.Status, Currency: o.Currency,
		RequestedShipDate: o.RequestedShipDate, ExternalRef: o.ExternalRef, NetTotal: o.NetTotal, LineCount: len(o.Lines), CreatedAt: o.CreatedAt,
		Lines: make([]portalLine, 0, len(o.Lines))}
	for _, l := range o.Lines {
		out.Lines = append(out.Lines, portalLine{LineNo: l.LineNo, Quantity: l.Quantity, Unit: l.Unit, UnitPrice: l.UnitPrice, LineNet: l.LineNet,
			RequestedShipDate: l.RequestedShipDate, ConfirmedShipDate: l.ConfirmedShipDate, LineStatus: l.LineStatus,
			DeliveredQty: l.DeliveredQty, InvoicedQty: l.InvoicedQty})
	}
	httpx.JSON(w, http.StatusOK, out)
}

type portalReturnLine struct {
	LineNo           int             `json:"lineNo"`
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
		out.Lines = append(out.Lines, portalReturnLine{LineNo: l.LineNo, QuantityExpected: l.QuantityExpected, QuantityReceived: l.QuantityReceived,
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
	httpx.JSON(w, http.StatusOK, toPortalReturn(o))
}
