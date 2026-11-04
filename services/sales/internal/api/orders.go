package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const (
	dayLayout         = "2006-01-02"
	maxOverrideReason = 500
)

type orderLineRequest struct {
	ProductID         string  `json:"productId"`
	Quantity          string  `json:"quantity"`
	Unit              string  `json:"unit"`
	SiteID            string  `json:"siteId"`
	WarehouseID       string  `json:"warehouseId"`
	UnitPrice         string  `json:"unitPrice"`
	DiscPct           *string `json:"discPct"`
	RequestedShipDate string  `json:"requestedShipDate"`
	ConfigurationID   *string `json:"configurationId,omitempty"`
	// DeliveryDateControl is the line's own control (FR-27.3); absent, the
	// legal entity's default applies at confirm. omitempty keeps the request
	// hash of orders that do not set it unchanged.
	DeliveryDateControl *string `json:"deliveryDateControl,omitempty"`
}

// createOrderRequest is the body of POST /v1/sales/orders. customerGroup
// stands in for the w7 customer group so GROUP listing entries apply;
// listingOverrideReason is required, with duty sales.order.approve, to accept a
// line the product listing denies (FR-27.15).
type createOrderRequest struct {
	LegalEntityID         string  `json:"legalEntityId"`
	CustomerID            string  `json:"customerId"`
	InvoiceAccountID      string  `json:"invoiceAccountId"`
	OrderType             string  `json:"orderType"`
	Currency              string  `json:"currency"`
	RequestedShipDate     string  `json:"requestedShipDate"`
	Channel               string  `json:"channel"`
	ExternalRef           *string `json:"externalRef"`
	CustomerGroup         *string `json:"customerGroup,omitempty"`
	ListingOverrideReason string  `json:"listingOverrideReason,omitempty"`
	// OneTime makes the order a one-time customer order (FR-27.22): the
	// walk-in buyer's name, address and tax id, held to one_time_order_limit.
	OneTime *domain.OneTimeParty `json:"oneTime,omitempty"`
	Lines   []orderLineRequest   `json:"lines"`
}

// confirmResponse is the order with the confirmation outcome.
type confirmResponse struct {
	store.Order
	Confirmed bool `json:"confirmed"`
}

// buildOrder validates an intake request and computes line nets. Field errors
// are keyed by their JSON path.
func buildOrder(req createOrderRequest) (store.NewOrder, map[string]string) {
	f := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		f["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.CustomerID) {
		f["customerId"] = "must be a UUID"
	}
	if !ids.Valid(req.InvoiceAccountID) {
		f["invoiceAccountId"] = "must be a UUID"
	}
	orderType := req.OrderType // empty: the legal entity's default order type applies
	if orderType != "" && !orderTypes[orderType] {
		f["orderType"] = "must be JOURNAL, ORDER, SUBSCRIPTION or RETURNED"
	}
	if !currencyCode.MatchString(req.Currency) {
		f["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if _, err := time.Parse(dayLayout, req.RequestedShipDate); err != nil {
		f["requestedShipDate"] = "must be a date such as 2027-03-04"
	}
	channel := req.Channel
	if channel == "" {
		channel = "UI"
	}
	if !channelName.MatchString(channel) {
		f["channel"] = "must be 1 to 40 letters, digits, - or _"
	}
	if req.ExternalRef != nil && (*req.ExternalRef == "" || len(*req.ExternalRef) > maxExternalRef) {
		f["externalRef"] = "must be 1 to 100 characters"
	}
	if req.CustomerGroup != nil && (*req.CustomerGroup == "" || len(*req.CustomerGroup) > maxScopeKey) {
		f["customerGroup"] = fmt.Sprintf("must be 1 to %d characters", maxScopeKey)
	}
	if len(req.ListingOverrideReason) > maxOverrideReason {
		f["listingOverrideReason"] = fmt.Sprintf("must be at most %d characters", maxOverrideReason)
	}
	if req.OneTime != nil {
		for k, v := range req.OneTime.Validate() {
			f["oneTime."+k] = v
		}
	}
	if len(req.Lines) == 0 || len(req.Lines) > maxLines {
		f["lines"] = fmt.Sprintf("must have 1 to %d lines", maxLines)
	}
	in := store.NewOrder{LegalEntityID: req.LegalEntityID, CustomerID: req.CustomerID, InvoiceAccountID: req.InvoiceAccountID,
		OrderType: orderType, Currency: req.Currency, RequestedShipDate: req.RequestedShipDate, Channel: channel, ExternalRef: req.ExternalRef,
		CustomerGroup: req.CustomerGroup, ListingOverrideReason: req.ListingOverrideReason, OneTime: req.OneTime}
	for i, l := range req.Lines {
		path := func(name string) string { return fmt.Sprintf("lines[%d].%s", i, name) }
		line, ok := buildLine(i, l, f, path)
		if ok {
			in.Lines = append(in.Lines, line)
		}
	}
	return in, f
}

func buildLine(i int, l orderLineRequest, f map[string]string, path func(string) string) (store.Line, bool) {
	valid := true
	if !ids.Valid(l.ProductID) {
		f[path("productId")] = "must be a UUID"
		valid = false
	}
	if !ids.Valid(l.SiteID) {
		f[path("siteId")] = "must be a UUID"
		valid = false
	}
	if !ids.Valid(l.WarehouseID) {
		f[path("warehouseId")] = "must be a UUID"
		valid = false
	}
	if n := len(l.Unit); n == 0 || n > 10 {
		f[path("unit")] = "must be 1 to 10 characters"
		valid = false
	}
	if _, err := time.Parse(dayLayout, l.RequestedShipDate); err != nil {
		f[path("requestedShipDate")] = "must be a date such as 2027-03-04"
		valid = false
	}
	if l.ConfigurationID != nil && !ids.Valid(*l.ConfigurationID) {
		f[path("configurationId")] = "must be a UUID"
		valid = false
	}
	if l.DeliveryDateControl != nil {
		if msg := domain.ValidateDeliveryControl(*l.DeliveryDateControl); msg != "" {
			f[path("deliveryDateControl")] = msg
			valid = false
		}
	}
	qty, errQty := decimal.NewFromString(l.Quantity)
	price, errPrice := decimal.NewFromString(l.UnitPrice)
	disc := decimal.Zero
	var errDisc error
	if l.DiscPct != nil {
		disc, errDisc = decimal.NewFromString(*l.DiscPct)
	}
	if errQty != nil {
		f[path("quantity")] = "must be a decimal"
		valid = false
	}
	if errPrice != nil {
		f[path("unitPrice")] = "must be a decimal"
		valid = false
	}
	if errDisc != nil {
		f[path("discPct")] = "must be a decimal from 0 to 100"
		valid = false
	}
	if !valid || errQty != nil || errPrice != nil || errDisc != nil {
		return store.Line{}, false
	}
	net, err := domain.LineNet(qty, price, disc)
	if err != nil {
		f[path("quantity")] = err.Error()
		return store.Line{}, false
	}
	return store.Line{LineNo: i + 1, ProductID: l.ProductID, Quantity: qty, Unit: l.Unit, SiteID: l.SiteID, WarehouseID: l.WarehouseID,
		UnitPrice: price, DiscPct: disc, LineNet: net, RequestedShipDate: l.RequestedShipDate, ConfigurationID: l.ConfigurationID,
		DeliveryDateControl: l.DeliveryDateControl}, true
}

// submitOrder validates and stores one intake order for the caller. Every
// refusal is returned as an error (a *httpx.Problem for the 4xx ones) and the
// channel refusals are logged by the caller. It is shared by POST /orders and
// by the replay of a refused intake order.
func (a *API) submitOrder(ctx context.Context, p tenant.Principal, req createOrderRequest, key string) (store.Order, bool, error) {
	in, fields := buildOrder(req)
	if key == "" {
		fields[idempotencyKey] = "is required"
	}
	if len(fields) > 0 {
		pb := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		pb.Errors = fields
		return store.Order{}, false, pb
	}
	if !tenant.InLegalEntityScope(p, in.LegalEntityID) {
		return store.Order{}, false, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the order's legal entity is outside the caller's scope")
	}
	if err := a.checkConfigurations(ctx, in.LegalEntityID, in.Lines); err != nil {
		return store.Order{}, false, err
	}
	hash, err := domain.RequestHash(req)
	if err != nil {
		return store.Order{}, false, err
	}
	in.RequestHash = hash
	in.IdempotencyKey = &key
	in.Actor = p.UserID
	in.CanOverrideListing = p.HasDuty(dutyOrderApprove)
	in.OrderDate = a.now()
	return a.store.CreateOrder(ctx, in)
}

// createOrder stores an intake order. A repeat of Idempotency-Key or of
// (channel, externalRef) with the same body returns 200 and the original order.
func (a *API) createOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	key := r.Header.Get(idempotencyKey)
	var req createOrderRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	order, created, err := a.submitOrder(r.Context(), p, req, key)
	if err != nil {
		a.logIntakeFailure(r.Context(), req, key, err)
		a.fail(w, r, "create order", err)
		return
	}
	status := http.StatusOK
	if created {
		a.created.Add(1)
		status = http.StatusCreated
	}
	setETag(w, order.Version)
	httpx.JSON(w, status, order)
}

func orderRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeOrderNotFound, "order not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) getOrder(w http.ResponseWriter, r *http.Request) {
	orderRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		order, err := a.store.GetOrder(r.Context(), id)
		if err != nil {
			a.fail(w, r, "get order", err)
			return
		}
		setETag(w, order.Version)
		httpx.JSON(w, http.StatusOK, order)
	})(w, r)
}

// confirmOrder runs the credit check and confirms the order, or places a CREDIT
// hold and returns 200 with confirmed=false. If-Match carries the order ETag.
func (a *API) confirmOrder(w http.ResponseWriter, r *http.Request) {
	orderRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		p, _ := tenant.FromContext(r.Context())
		a.confirms.Add(1)
		res, err := a.store.ConfirmOrder(r.Context(), id, version, p.UserID, a.now())
		if err != nil {
			a.fail(w, r, "confirm order", err)
			return
		}
		if !res.Confirmed {
			a.held.Add(1)
		}
		setETag(w, res.Order.Version)
		httpx.JSON(w, http.StatusOK, confirmResponse{Order: res.Order, Confirmed: res.Confirmed})
	})(w, r)
}

// releaseHold releases one hold. CREDIT holds need sales.credit.release; other
// hold types need sales.order.hold. Releasing the CREDIT hold placed by a
// confirm confirms the order (confirmed=true in the response, AC-27.3).
func (a *API) releaseHold(w http.ResponseWriter, r *http.Request) {
	orderRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		holdID := chi.URLParam(r, "holdId")
		if !ids.Valid(holdID) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeHoldNotFound, "hold not found"))
			return
		}
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		p, _ := tenant.FromContext(r.Context())
		current, err := a.store.GetOrder(r.Context(), id)
		if err != nil {
			a.fail(w, r, "load order for release", err)
			return
		}
		for _, h := range current.Holds {
			if h.ID != holdID {
				continue
			}
			if need := releaseDuty(h.HoldType); !p.HasDuty(need) {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+need))
				return
			}
		}
		res, err := a.store.ReleaseHold(r.Context(), id, holdID, version, p.UserID, a.now())
		if err != nil {
			a.fail(w, r, "release hold", err)
			return
		}
		a.released.Add(1)
		setETag(w, res.Order.Version)
		httpx.JSON(w, http.StatusOK, confirmResponse{Order: res.Order, Confirmed: res.Confirmed})
	})(w, r)
}

// releaseDuty is the duty that releases a hold of holdType (PRD §5.27.17).
func releaseDuty(holdType string) string {
	if holdType == domain.HoldCredit {
		return dutyCreditRelease
	}
	return dutyHold
}

// listOrders returns one page of orders, newest first. hold=CREDIT is the
// credit release queue.
func (a *API) listOrders(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.OrderFilter{Status: q.Get("status"), CustomerID: q.Get("customerId"), HoldType: q.Get("hold"), Before: q.Get("cursor")}
	fields := map[string]string{}
	if f.Status != "" && !orderStatuses[f.Status] {
		fields["status"] = "must be an order status such as OPEN or CONFIRMED"
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if f.HoldType != "" && !holdTypes[f.HoldType] {
		fields["hold"] = "must be CREDIT, FRAUD, MANUAL or PRICE_REVIEW"
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
		a.fail(w, r, "list orders", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

var (
	orderStatuses = map[string]bool{"DRAFT": true, "OPEN": true, "CONFIRMED": true, "PARTIALLY_SHIPPED": true, "SHIPPED": true,
		"INVOICED": true, "CLOSED": true, "CANCELLED": true}
	holdTypes = map[string]bool{domain.HoldCredit: true, domain.HoldFraud: true, domain.HoldManual: true, domain.HoldPriceReview: true}
)

// pageLimit parses ?limit=, defaulting to defaultPageSize.
func pageLimit(raw string) (int, bool) {
	if raw == "" {
		return defaultPageSize, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > maxPageSize {
		return 0, false
	}
	return n, true
}
