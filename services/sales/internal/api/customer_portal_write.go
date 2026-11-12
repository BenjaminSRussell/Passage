package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

// portalOrderRequest is the body of POST /customer-portal/orders. It has no customer, legal entity, price, site or warehouse:
// those come from the token and from the customer's own order history. Unknown fields are refused by the decoder, so a body that
// names a customer is a 400, not an ignored field.
type portalOrderRequest struct {
	RequestedShipDate string  `json:"requestedShipDate"`
	ExternalRef       *string `json:"externalRef"`
	Lines             []struct {
		ProductID string `json:"productId"`
		Quantity  string `json:"quantity"`
		Unit      string `json:"unit"`
	} `json:"lines"`
}

type portalReturnRequest struct {
	OriginalInvoiceID string `json:"originalInvoiceId"`
	ReturnReasonCode  string `json:"returnReasonCode"`
	Lines             []struct {
		ProductID string `json:"productId"`
		Quantity  string `json:"quantity"`
	} `json:"lines"`
}

// portalNamespace is the customer's id without hyphens: it prefixes the idempotency key and names the channel, so that what one
// customer sends can never collide with, or replay, another customer's order or return.
func portalNamespace(customer string) string {
	return strings.ReplaceAll(strings.ToLower(customer), "-", "")
}

func portalKey(w http.ResponseWriter, r *http.Request, customer string) (string, bool) {
	key := strings.TrimSpace(r.Header.Get(idempotencyKey))
	if n := len(key); n < 8 || n > 80 {
		validationFailed(w, r, map[string]string{idempotencyKey: "is required: 8 to 80 characters, one per submit"})
		return "", false
	}
	return portalNamespace(customer) + ":" + key, true
}

// portalHistory reads what the customer has bought before, for the given products (all of them when none are named).
func (a *API) portalHistory(r *http.Request, customer string, productIDs []string) ([]store.PortalProduct, bool, error) {
	p, _ := tenant.FromContext(r.Context())
	if !ids.Valid(p.LegalEntityID) {
		return nil, false, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "the portal token names no legal entity")
	}
	rows, err := a.store.PortalProducts(r.Context(), store.PortalProductFilter{LegalEntityID: p.LegalEntityID, CustomerID: customer, ProductIDs: productIDs, Limit: portalHistoryMax, Since: a.now().AddDate(0, 0, -a.priceWindowDays())})
	return rows, len(rows) >= portalHistoryMax, err
}

// priceWindowDays is how far back a price may be reordered (default 365 days, SALES_PORTAL_PRICE_WINDOW_DAYS).
func (a *API) priceWindowDays() int {
	if a.portalWindowDays > 0 {
		return a.portalWindowDays
	}
	return defaultPortalWindowDays
}

// WithPortalPriceWindow sets how many days back a portal order may reuse a price.
func (a *API) WithPortalPriceWindow(days int) *API {
	a.portalWindowDays = days
	return a
}

// pickHistory is the history row a line reorders: the product's newest approved line. A unit the customer names that differs from
// that line's unit is not matched (unitMismatch tells the caller why).
func pickHistory(rows []store.PortalProduct, productID, unit string) (h store.PortalProduct, found, unitMismatch bool) {
	for _, h := range rows {
		if strings.EqualFold(h.ProductID, productID) {
			if unit != "" && h.Unit != unit {
				return h, false, true
			}
			return h, true, false
		}
	}
	return store.PortalProduct{}, false, false
}

type portalProduct struct {
	ProductID     string          `json:"productId"`
	ProductName   string          `json:"productName,omitempty"`
	Unit          string          `json:"unit"`
	UnitPrice     decimal.Decimal `json:"unitPrice"`
	Currency      string          `json:"currency"`
	LastOrderedAt time.Time       `json:"lastOrderedAt"`
}

// portalListProducts answers what the customer can order: the products of its own earlier orders, with the price it paid last.
func (a *API) portalListProducts(w http.ResponseWriter, r *http.Request, customer string) {
	rows, _, err := a.portalHistory(r, customer, nil)
	if err != nil {
		a.fail(w, r, "list customer portal products", err)
		return
	}
	want := make([]string, 0, len(rows))
	for _, h := range rows {
		want = append(want, h.ProductID)
	}
	names := a.productNames(r.Context(), want)
	out := make([]portalProduct, 0, len(rows))
	for _, h := range rows {
		price := h.UnitPrice.Mul(decimal.NewFromInt(100).Sub(h.DiscPct)).Div(decimal.NewFromInt(100)).Round(6)
		out = append(out, portalProduct{ProductID: h.ProductID, ProductName: names[strings.ToLower(h.ProductID)], Unit: h.Unit, UnitPrice: price, Currency: h.Currency, LastOrderedAt: h.LastOrderedAt})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) portalPlaceOrder(w http.ResponseWriter, r *http.Request, customer string) {
	key, ok := portalKey(w, r, customer)
	if !ok {
		return
	}
	var req portalOrderRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	fields := map[string]string{}
	if day, err := time.Parse(dayLayout, req.RequestedShipDate); err != nil {
		fields["requestedShipDate"] = "must be a date such as 2027-03-04"
	} else if today := a.now().UTC().Truncate(24 * time.Hour); day.Before(today) {
		fields["requestedShipDate"] = "must not be in the past"
	}
	if n := len(req.Lines); n < 1 || n > maxPortalLines {
		fields["lines"] = fmt.Sprintf("must have 1 to %d lines", maxPortalLines)
	}
	wanted := make([]string, 0, len(req.Lines))
	for i, l := range req.Lines {
		if !ids.Valid(l.ProductID) {
			fields[fmt.Sprintf("lines[%d].productId", i)] = "must be a product id from your product list"
			continue
		}
		wanted = append(wanted, strings.ToLower(l.ProductID))
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	history, _, err := a.portalHistory(r, customer, wanted)
	if err != nil {
		a.fail(w, r, "read customer portal order history", err)
		return
	}
	in := createOrderRequest{LegalEntityID: p.LegalEntityID, CustomerID: customer, OrderType: "ORDER", RequestedShipDate: req.RequestedShipDate,
		Channel: portalChannel + portalNamespace(customer), ExternalRef: req.ExternalRef}
	for i, l := range req.Lines {
		h, found, mismatch := pickHistory(history, l.ProductID, l.Unit)
		if mismatch {
			fields[fmt.Sprintf("lines[%d].unit", i)] = "is not the unit you last bought this product in (" + h.Unit + ")"
			continue
		}
		if !found {
			fields[fmt.Sprintf("lines[%d].productId", i)] = "is not a product you have had approved and ordered recently; ask your sales contact to add it"
			continue
		}
		if in.Currency == "" {
			in.Currency, in.InvoiceAccountID = h.Currency, h.InvoiceAccountID
		} else if in.Currency != h.Currency || in.InvoiceAccountID != h.InvoiceAccountID {
			fields[fmt.Sprintf("lines[%d].productId", i)] = "is invoiced differently from the other lines; order it separately"
			continue
		}
		disc := h.DiscPct.String()
		in.Lines = append(in.Lines, orderLineRequest{ProductID: h.ProductID, Quantity: l.Quantity, Unit: h.Unit, SiteID: h.SiteID, WarehouseID: h.WarehouseID,
			UnitPrice: h.UnitPrice.String(), DiscPct: &disc, RequestedShipDate: req.RequestedShipDate})
	}
	if len(fields) > 0 {
		pb := httpx.NewProblem(http.StatusUnprocessableEntity, codePortalProduct, "some lines cannot be ordered from the portal")
		pb.Errors = fields
		httpx.Fail(w, r, pb)
		return
	}
	for i, l := range in.Lines {
		if q, err := decimal.NewFromString(l.Quantity); err != nil || !q.IsPositive() {
			fields[fmt.Sprintf("lines[%d].quantity", i)] = "must be a positive number"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	order, created, err := a.submitOrder(r.Context(), p, in, key)
	if err != nil {
		a.fail(w, r, "place customer portal order", err)
		return
	}
	// A new order, or one whose first attempt stopped before the confirm, is committed now: the credit check runs and a CREDIT
	// hold may be placed, exactly as for an order entered by staff. A replay of a committed order is answered as it is.
	if created || order.Status == "DRAFT" {
		res, err := a.store.ConfirmOrder(r.Context(), order.ID, order.Version, p.UserID, a.now())
		if err != nil {
			a.fail(w, r, "confirm customer portal order", err)
			return
		}
		order = res.Order
	}
	if !strings.EqualFold(order.CustomerID, customer) {
		// Cannot happen with the namespaced key and channel; never answer with another customer's order if it somehow does.
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, store.CodeDuplicateIntake, "the order could not be placed; retry"))
		return
	}
	out := toPortalOrder(order)
	a.nameOrderLines(r.Context(), &out)
	status := http.StatusOK
	if created {
		a.created.Add(1)
		status = http.StatusCreated
	}
	httpx.JSON(w, status, out)
}

func (a *API) portalRequestReturn(w http.ResponseWriter, r *http.Request, customer string) {
	key, ok := portalKey(w, r, customer)
	if !ok {
		return
	}
	var req portalReturnRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	in := createReturnRequest{LegalEntityID: p.LegalEntityID, CustomerID: customer, OriginalInvoiceID: req.OriginalInvoiceID, ReturnReasonCode: req.ReturnReasonCode}
	for i, l := range req.Lines {
		// Unit, site and warehouse come from the order line the invoice line was billed from (FromOrderLine below); the ids here only
		// satisfy the shared validation and are replaced by the store.
		in.Lines = append(in.Lines, returnLineRequest{LineNo: i + 1, ProductID: l.ProductID, SiteID: ids.New(), WarehouseID: ids.New(), Quantity: l.Quantity, Unit: "EA",
			Disposition: domain.DispositionReturnToStock, ReturnCostMethod: domain.CostCurrent})
	}
	order, fieldsErr := buildReturnOrder(in, key)
	if len(fieldsErr) > 0 {
		validationFailed(w, r, fieldsErr)
		return
	}
	// The hash is of what the customer sent, not of the placeholder ids above, so a retry of the same body is the same request.
	hash, err := domain.RequestHash(req)
	if err != nil {
		a.fail(w, r, "hash request", err)
		return
	}
	order.RequestHash, order.Actor = hash, p.UserID
	for i := range order.Lines {
		order.Lines[i].FromOrderLine = true
	}
	saved, created, err := a.store.CreateReturnOrder(r.Context(), order)
	if err != nil {
		a.fail(w, r, "create customer portal return", portalReturnError(err))
		return
	}
	if !strings.EqualFold(saved.CustomerID, customer) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, store.CodeIdempotencyReuse, "the return could not be requested; retry"))
		return
	}
	out := []portalReturn{toPortalReturn(saved)}
	a.nameReturnLines(r.Context(), out)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, out[0])
}

// portalReturnError gives the customer one answer for an invoice that does not exist, is another customer's, or cannot be
// returned, so a return request cannot be used to probe for other customers' invoice ids.
func portalReturnError(err error) error {
	var pb *httpx.Problem
	if errors.As(err, &pb) && pb.Code == store.CodeRMAInvoice && strings.Contains(pb.Detail, "original invoice") {
		return httpx.NewProblem(http.StatusUnprocessableEntity, codePortalInvoice, "that invoice is not one of your posted invoices; choose it from your invoice list")
	}
	return err
}
