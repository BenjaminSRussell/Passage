package api

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/domain"
	"github.com/BenjaminSRussell/passage/services/inventory/internal/store"
)

// Vendor consignment routes (FR-16.17). ConsignmentMaintain is
// inventory.consignment.write; the stock report is an inquiry.
const (
	dutyConsignmentWrite = "inventory.consignment.write"
	consignmentPath      = "/v1/inventory/consignment-orders"
	consignmentStockPath = "/v1/inventory/consignment/stock"
)

type consignmentRequest struct {
	LegalEntityID string                   `json:"legalEntityId"`
	VendorID      string                   `json:"vendorId"`
	SiteID        string                   `json:"siteId"`
	WarehouseID   string                   `json:"warehouseId"`
	RequestedDate string                   `json:"requestedDate"`
	Currency      string                   `json:"currency"`
	Lines         []consignmentLineRequest `json:"lines"`
}

type consignmentLineRequest struct {
	ProductID        string            `json:"productId"`
	Dimensions       map[string]string `json:"dimensions"`
	Unit             string            `json:"unit"`
	Qty              string            `json:"qty"`
	ConsignmentPrice string            `json:"consignmentPrice"`
}

type consignmentReceiveRequest struct {
	ReceiptDate string                   `json:"receiptDate"`
	Lines       []consignmentReceiptLine `json:"lines"`
}

type consignmentReceiptLine struct {
	LineID string `json:"lineId"`
	Qty    string `json:"qty"`
}

type consignmentLineResponse struct {
	ID               string            `json:"id"`
	LineNo           int               `json:"lineNo"`
	ProductID        string            `json:"productId"`
	Dimensions       map[string]string `json:"dimensions"`
	Unit             string            `json:"unit"`
	Qty              string            `json:"qty"`
	ReceivedQty      string            `json:"receivedQty"`
	OpenQty          string            `json:"openQty"`
	GivenUpQty       string            `json:"givenUpQty"`
	ConsignmentPrice string            `json:"consignmentPrice"`
}

type consignmentResponse struct {
	ID            string                    `json:"id"`
	LegalEntityID string                    `json:"legalEntityId"`
	Number        string                    `json:"number"`
	VendorID      string                    `json:"vendorId"`
	SiteID        string                    `json:"siteId"`
	WarehouseID   string                    `json:"warehouseId"`
	RequestedDate string                    `json:"requestedDate"`
	Currency      string                    `json:"currency"`
	Status        string                    `json:"status"`
	Version       int                       `json:"version"`
	CreatedAt     time.Time                 `json:"createdAt"`
	ClosedAt      *time.Time                `json:"closedAt"`
	ClosedBy      string                    `json:"closedBy,omitempty"`
	CloseReason   string                    `json:"closeReason,omitempty"`
	Lines         []consignmentLineResponse `json:"lines"`
}

type consignmentStockItem struct {
	LegalEntityID    string            `json:"legalEntityId"`
	VendorID         string            `json:"vendorId"`
	ProductID        string            `json:"productId"`
	Dimensions       map[string]string `json:"dimensions"`
	Qty              string            `json:"qty"`
	ConsignmentPrice *string           `json:"consignmentPrice"`
	Currency         string            `json:"currency,omitempty"`
	StatisticalValue *string           `json:"statisticalValue"`
	OldestReceipt    *string           `json:"oldestReceipt"`
	AgeDays          *int              `json:"ageDays"`
}

// registerConsignment mounts the replenishment order and consignment stock routes.
func (a *API) registerConsignment(r chi.Router) {
	r.With(authz.RequireDuty(dutyConsignmentWrite)).Post(consignmentPath, a.createConsignment)
	r.With(authz.RequireDuty(dutyRead)).Get(consignmentPath, a.listConsignments)
	r.With(authz.RequireDuty(dutyRead)).Get(consignmentPath+"/{id}", consignmentRoute(a.getConsignment))
	r.With(authz.RequireDuty(dutyConsignmentWrite)).Post(consignmentPath+"/{id}:receive", consignmentRoute(a.receiveConsignment))
	r.With(authz.RequireDuty(dutyConsignmentWrite)).Post(consignmentPath+"/{id}:close", consignmentRoute(a.closeConsignment))
	r.With(authz.RequireDuty(dutyConsignmentWrite)).Post(consignmentPath+"/{id}:cancel", consignmentRoute(a.cancelConsignment))
	r.With(authz.RequireDuty(dutyRead)).Get(consignmentStockPath, a.consignmentStock)
}

// consignmentRoute checks the tenant and the order id before the handler runs.
func consignmentRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeConsignmentNotFound, "replenishment order not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) createConsignment(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req consignmentRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	req.VendorID, req.SiteID, req.WarehouseID = strings.TrimSpace(req.VendorID), strings.TrimSpace(req.SiteID), strings.TrimSpace(req.WarehouseID)
	code(fields, "vendorId", req.VendorID, false)
	code(fields, "siteId", req.SiteID, false)
	code(fields, "warehouseId", req.WarehouseID, false)
	requested, err := time.Parse(dateFormat, req.RequestedDate)
	if err != nil {
		fields["requestedDate"] = "must be a date (YYYY-MM-DD)"
	}
	if !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	switch {
	case len(req.Lines) == 0:
		fields["lines"] = "must hold at least one line"
	case len(req.Lines) > maxReceiptLines:
		fields["lines"] = fmt.Sprintf("must hold at most %d lines", maxReceiptLines)
	}
	lines := make([]domain.ConsignmentLine, 0, len(req.Lines))
	for i, l := range req.Lines {
		prefix := fmt.Sprintf("lines[%d].", i)
		if !ids.Valid(l.ProductID) {
			fields[prefix+"productId"] = "must be a UUID"
		}
		if !unitCode.MatchString(l.Unit) {
			fields[prefix+"unit"] = "is required (1 to 12 characters of A-Z, 0-9, _ or -)"
		}
		qty, qErr := decimal.NewFromString(strings.TrimSpace(l.Qty))
		if qErr != nil || !qty.IsPositive() {
			fields[prefix+"qty"] = "must be a positive decimal number"
		}
		price, pErr := decimalOr(l.ConsignmentPrice, decimal.Zero)
		if pErr != nil || price.Sign() < 0 {
			fields[prefix+"consignmentPrice"] = "must be a non-negative decimal number"
		}
		lines = append(lines, domain.ConsignmentLine{LineNo: i + 1, ProductID: l.ProductID, Dims: domain.Dimensions(l.Dimensions),
			Unit: l.Unit, Qty: qty, Price: price})
	}
	key := idempotencyKey(r, fields)
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	in := domain.ConsignmentOrder{LegalEntityID: req.LegalEntityID, VendorID: req.VendorID, Site: req.SiteID, Warehouse: req.WarehouseID,
		RequestedDate: requested, Currency: req.Currency, Lines: lines}
	out, replayed, err := a.store.CreateConsignment(r.Context(), key, in)
	if err != nil {
		a.failed(w, r, "create replenishment order", err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	writeConsignment(w, status, out)
}

func (a *API) listConsignments(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f := store.ConsignmentFilter{Status: r.URL.Query().Get("status"), VendorID: strings.TrimSpace(r.URL.Query().Get("vendor"))}
	if f.Status != "" && !slices.Contains(domain.ConsignmentStatuses, f.Status) {
		fieldProblem(w, r, map[string]string{"status": "must be CREATED, PARTIALLY_RECEIVED, RECEIVED, CLOSED or CANCELLED"})
		return
	}
	orders, err := a.store.ListConsignments(r.Context(), f)
	if err != nil {
		a.failed(w, r, "list replenishment orders", err)
		return
	}
	items := make([]consignmentResponse, 0, len(orders))
	for _, o := range orders {
		items = append(items, toConsignmentResponse(o))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getConsignment(w http.ResponseWriter, r *http.Request, id string) {
	o, err := a.store.GetConsignment(r.Context(), id)
	if err != nil {
		a.failed(w, r, "get replenishment order", err)
		return
	}
	writeConsignment(w, http.StatusOK, o)
}

// receiveConsignment books vendor-owned receipts against the order. If-Match
// carries the order version.
func (a *API) receiveConsignment(w http.ResponseWriter, r *http.Request, id string) {
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var req consignmentReceiveRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	receiptDate, ok := optionalDate(req.ReceiptDate)
	if !ok {
		fields["receiptDate"] = "must be a date (YYYY-MM-DD)"
	}
	switch {
	case len(req.Lines) == 0:
		fields["lines"] = "must hold at least one line"
	case len(req.Lines) > maxReceiptLines:
		fields["lines"] = fmt.Sprintf("must hold at most %d lines", maxReceiptLines)
	}
	receipts := make([]domain.ConsignmentReceipt, 0, len(req.Lines))
	for i, l := range req.Lines {
		prefix := fmt.Sprintf("lines[%d].", i)
		if !ids.Valid(l.LineID) {
			fields[prefix+"lineId"] = "must be a UUID"
		}
		qty, err := decimal.NewFromString(strings.TrimSpace(l.Qty))
		if err != nil || !qty.IsPositive() {
			fields[prefix+"qty"] = "must be a positive decimal number"
		}
		receipts = append(receipts, domain.ConsignmentReceipt{LineID: l.LineID, Qty: qty})
	}
	if len(fields) > 0 {
		fieldProblem(w, r, fields)
		return
	}
	o, err := a.store.ReceiveConsignment(r.Context(), id, version, receipts, receiptDate)
	if err != nil {
		a.failed(w, r, "receive replenishment order", err)
		return
	}
	writeConsignment(w, http.StatusOK, o)
}

type consignmentEndRequest struct {
	Reason string `json:"reason"`
}

// endRequest reads the optional reason of :close and :cancel (at most 200 characters; a request
// without a body, including an empty chunked one, has none). It answers false after writing the
// problem.
func endRequest(w http.ResponseWriter, r *http.Request) (string, bool) {
	var req consignmentEndRequest
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxEndBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "could not read the request body"))
		return "", false
	}
	if len(bytes.TrimSpace(raw)) > 0 {
		r.Body = io.NopCloser(bytes.NewReader(raw))
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return "", false
		}
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if utf8.RuneCountInString(req.Reason) > maxCloseReason {
		fieldProblem(w, r, map[string]string{"reason": fmt.Sprintf("must be at most %d characters", maxCloseReason)})
		return "", false
	}
	return req.Reason, true
}

// maxCloseReason bounds the free-text reason of closing or cancelling an order; maxEndBody bounds
// the body read for it.
const (
	maxCloseReason = 200
	maxEndBody     = 64 << 10
)

// closeConsignment gives up the open quantity of a partly received order (If-Match).
func (a *API) closeConsignment(w http.ResponseWriter, r *http.Request, id string) {
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	reason, ok := endRequest(w, r)
	if !ok {
		return
	}
	o, err := a.store.CloseConsignment(r.Context(), id, version, reason)
	if err != nil {
		a.failed(w, r, "close replenishment order", err)
		return
	}
	writeConsignment(w, http.StatusOK, o)
}

// cancelConsignment cancels an order nothing was received against (If-Match).
func (a *API) cancelConsignment(w http.ResponseWriter, r *http.Request, id string) {
	version, err := ifMatchVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	reason, ok := endRequest(w, r)
	if !ok {
		return
	}
	o, err := a.store.CancelConsignment(r.Context(), id, version, reason)
	if err != nil {
		a.failed(w, r, "cancel replenishment order", err)
		return
	}
	writeConsignment(w, http.StatusOK, o)
}

// consignmentStock answers the consignment stock report (3.16.14): vendor-owned
// quantity by vendor and dimension set, with its age and statistical value.
func (a *API) consignmentStock(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	rows, err := a.store.ListConsignmentStock(r.Context(), strings.TrimSpace(r.URL.Query().Get("vendor")))
	if err != nil {
		a.failed(w, r, "consignment stock", err)
		return
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	items := make([]consignmentStockItem, 0, len(rows))
	for _, row := range rows {
		it := consignmentStockItem{LegalEntityID: row.LegalEntityID, VendorID: row.VendorID, ProductID: row.ProductID, Dimensions: row.DimensionSet,
			Qty: row.Qty.String(), Currency: row.Currency}
		if row.Price != nil {
			price, value := row.Price.String(), row.Value.String()
			it.ConsignmentPrice, it.StatisticalValue = &price, &value
		}
		if row.OldestReceipt != nil {
			date := row.OldestReceipt.Format(dateFormat)
			age := int(today.Sub(row.OldestReceipt.UTC().Truncate(24*time.Hour)).Hours() / 24)
			it.OldestReceipt, it.AgeDays = &date, &age
		}
		items = append(items, it)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func toConsignmentResponse(o domain.ConsignmentOrder) consignmentResponse {
	out := consignmentResponse{ID: o.ID, LegalEntityID: o.LegalEntityID, Number: o.Number, VendorID: o.VendorID, SiteID: o.Site,
		WarehouseID: o.Warehouse, RequestedDate: o.RequestedDate.Format(dateFormat), Currency: o.Currency, Status: o.Status,
		Version: o.Version, CreatedAt: o.CreatedAt, ClosedAt: o.ClosedAt, ClosedBy: o.ClosedBy, CloseReason: o.CloseReason, Lines: []consignmentLineResponse{}}
	for _, l := range o.Lines {
		out.Lines = append(out.Lines, consignmentLineResponse{ID: l.ID, LineNo: l.LineNo, ProductID: l.ProductID, Dimensions: l.Dims,
			Unit: l.Unit, Qty: l.Qty.String(), ReceivedQty: l.ReceivedQty.String(), OpenQty: o.OpenQty(l).String(), GivenUpQty: o.GivenUpQty(l).String(),
			ConsignmentPrice: l.Price.String()})
	}
	return out
}

func writeConsignment(w http.ResponseWriter, status int, o domain.ConsignmentOrder) {
	w.Header().Set("ETag", strconv.Quote(strconv.Itoa(o.Version)))
	httpx.JSON(w, status, toConsignmentResponse(o))
}
