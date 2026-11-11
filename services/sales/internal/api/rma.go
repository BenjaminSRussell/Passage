package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/domain"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const returnOrders = "/v1/sales/return-orders"

type returnLineRequest struct {
	LineNo           int     `json:"lineNo"`
	ProductID        string  `json:"productId"`
	SiteID           string  `json:"siteId"`
	WarehouseID      string  `json:"warehouseId"`
	Quantity         string  `json:"quantity"`
	Unit             string  `json:"unit"`
	Disposition      string  `json:"disposition"`
	ReturnCostMethod string  `json:"returnCostMethod"`
	RestockingPct    *string `json:"restockingPct"`
}

type createReturnRequest struct {
	LegalEntityID     string              `json:"legalEntityId"`
	CustomerID        string              `json:"customerId"`
	OriginalInvoiceID string              `json:"originalInvoiceId"`
	ReturnReasonCode  string              `json:"returnReasonCode"`
	Lines             []returnLineRequest `json:"lines"`
}

type receiveLineRequest struct {
	LineNo   int    `json:"lineNo"`
	Quantity string `json:"quantity"`
}

type receiveReturnRequest struct {
	Lines []receiveLineRequest `json:"lines"`
}

type inspectLineRequest struct {
	LineNo      int    `json:"lineNo"`
	Accepted    string `json:"quantityAccepted"`
	Disposition string `json:"disposition"`
}

type inspectReturnRequest struct {
	Lines []inspectLineRequest `json:"lines"`
}

var returnStatuses = map[string]bool{
	domain.RMARequested: true, domain.RMAAuthorized: true, domain.RMAReceived: true, domain.RMAInspected: true, domain.RMADispositioned: true,
	domain.RMACredited: true, domain.RMAClosed: true, domain.RMARejected: true, domain.RMAExpired: true,
}

func (a *API) registerReturnOrders(r chi.Router) {
	r.With(authz.RequireDuty(dutyWrite)).Post(returnOrders, a.createReturnOrder)
	r.With(authz.RequireDuty(dutyRead)).Get(returnOrders, a.listReturnOrders)
	r.With(authz.RequireDuty(dutyRead)).Get(returnOrders+"/{id}", a.getReturnOrder)
	r.With(authz.RequireDuty(dutyWrite)).Post(returnOrders+"/{id}:authorize", a.authorizeReturnOrder)
	r.With(authz.RequireDuty(dutyWrite)).Post(returnOrders+"/{id}:receive", a.receiveReturnOrder)
	r.With(authz.RequireDuty(dutyWrite)).Post(returnOrders+"/{id}:inspect", a.inspectReturnOrder)
	r.With(authz.RequireDuty(dutyWrite)).Post(returnOrders+"/{id}:disposition", a.dispositionReturnOrder)
	r.With(authz.RequireDuty(dutyWrite)).Post(returnOrders+"/{id}:credit", a.creditReturnOrder)
	r.With(authz.RequireDuty(dutyWrite)).Post(returnOrders+"/{id}:reject", a.rejectReturnOrder)
	r.With(authz.RequireDuty(dutyWrite)).Post(returnOrders+":backfill-reservations", a.backfillReservations)
}

func (a *API) rejectReturnOrder(w http.ResponseWriter, r *http.Request) {
	a.rmaCommand(w, r, "reject return order", a.store.RejectReturnOrder)
}

func (a *API) createReturnOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	var req createReturnRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := buildReturnOrder(req, key)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !inScope(w, r, in.LegalEntityID) {
		return
	}
	hash, err := domain.RequestHash(req)
	if err != nil {
		a.fail(w, r, "hash request", err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	in.RequestHash, in.Actor = hash, p.UserID
	saved, created, err := a.store.CreateReturnOrder(r.Context(), in)
	if err != nil {
		a.fail(w, r, "create return order", err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	setETag(w, saved.Version)
	httpx.JSON(w, status, saved)
}

func buildReturnOrder(req createReturnRequest, key string) (store.NewReturnOrder, map[string]string) {
	fields := map[string]string{}
	if key == "" {
		fields[idempotencyKey] = "is required"
	}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if !ids.Valid(req.OriginalInvoiceID) {
		fields["originalInvoiceId"] = "is required and must be a UUID"
	}
	if req.CustomerID != "" && !ids.Valid(req.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	req.ReturnReasonCode = strings.TrimSpace(req.ReturnReasonCode)
	if n := len([]rune(req.ReturnReasonCode)); n < 1 || n > 40 {
		fields["returnReasonCode"] = "must be 1 to 40 characters"
	}
	if n := len(req.Lines); n < 1 || n > maxLines {
		fields["lines"] = fmt.Sprintf("must have 1 to %d lines", maxLines)
	}
	in := store.NewReturnOrder{LegalEntityID: req.LegalEntityID, CustomerID: req.CustomerID, OriginalInvoiceID: req.OriginalInvoiceID,
		ReturnReasonCode: req.ReturnReasonCode, IdempotencyKey: key}
	seen := map[int]bool{}
	for i, l := range req.Lines {
		name := fmt.Sprintf("lines[%d]", i)
		if l.LineNo < 1 || seen[l.LineNo] {
			fields[name+".lineNo"] = "must be a line number, once per return"
		}
		seen[l.LineNo] = true
		if !ids.Valid(l.ProductID) {
			fields[name+".productId"] = "must be a UUID"
		}
		if l.Disposition != domain.DispositionReturnToStock && l.Disposition != domain.DispositionCreditOnly {
			fields[name+".disposition"] = "must be RETURN_TO_STOCK or CREDIT_ONLY"
		}
		if domain.PhysicalReturn(l.Disposition) && (!ids.Valid(l.SiteID) || !ids.Valid(l.WarehouseID)) {
			fields[name+".siteId"] = "siteId and warehouseId are required for RETURN_TO_STOCK"
		}
		qty, err := decimal.NewFromString(l.Quantity)
		if err != nil || !qty.IsPositive() {
			fields[name+".quantity"] = "must be a positive decimal"
		}
		if !domain.ValidCostMethod(l.ReturnCostMethod) {
			fields[name+".returnCostMethod"] = "must be ORIGINAL_COST, CURRENT_COST or ZERO"
		}
		nl := store.NewReturnLine{LineNo: l.LineNo, ProductID: l.ProductID, SiteID: l.SiteID, WarehouseID: l.WarehouseID,
			Quantity: qty, Unit: l.Unit, Disposition: l.Disposition, ReturnCostMethod: l.ReturnCostMethod}
		if l.RestockingPct != nil {
			pct, err := decimal.NewFromString(*l.RestockingPct)
			if err != nil || pct.IsNegative() || pct.GreaterThan(decimal.NewFromInt(100)) {
				fields[name+".restockingPct"] = "must be a decimal from 0 to 100"
			} else {
				nl.RestockingPct = &pct
			}
		}
		in.Lines = append(in.Lines, nl)
	}
	return in, fields
}

func (a *API) listReturnOrders(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.ReturnOrderFilter{LegalEntityID: legalEntity, CustomerID: q.Get("customerId"), Status: q.Get("status")}
	fields := map[string]string{}
	if f.Status != "" && !returnStatuses[f.Status] {
		fields["status"] = "must be a return order status"
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		fields["customerId"] = "must be a UUID"
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
	items, err := a.store.ListReturnOrders(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list return orders", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func returnRoute(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !ids.Valid(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRMANotFound, "return order not found"))
			return
		}
		next(w, r, id)
	}
}

func (a *API) getReturnOrder(w http.ResponseWriter, r *http.Request) {
	returnRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		o, err := a.store.GetReturnOrder(r.Context(), id)
		if err != nil {
			a.fail(w, r, "get return order", err)
			return
		}
		setETag(w, o.Version)
		httpx.JSON(w, http.StatusOK, o)
	})(w, r)
}

func (a *API) authorizeReturnOrder(w http.ResponseWriter, r *http.Request) {
	a.rmaCommand(w, r, "authorize return order", a.store.AuthorizeReturnOrder)
}

func (a *API) inspectReturnOrder(w http.ResponseWriter, r *http.Request) {
	returnRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req inspectReturnRequest
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &req); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		fields := map[string]string{}
		var lines []store.InspectLine
		seen := map[int]bool{}
		for i, l := range req.Lines {
			name := fmt.Sprintf("lines[%d]", i)
			if l.LineNo < 1 || seen[l.LineNo] {
				fields[name+".lineNo"] = "must be a line number, once per call"
			}
			seen[l.LineNo] = true
			accepted, err := decimal.NewFromString(l.Accepted)
			if err != nil || accepted.IsNegative() {
				fields[name+".quantityAccepted"] = "must be a decimal of 0 or more"
			}
			if l.Disposition != domain.DispositionReturnToStock && l.Disposition != domain.DispositionCreditOnly {
				fields[name+".disposition"] = "must be RETURN_TO_STOCK or CREDIT_ONLY"
			}
			lines = append(lines, store.InspectLine{LineNo: l.LineNo, Accepted: accepted, Disposition: l.Disposition})
		}
		// No lines is valid: an RMA that received nothing (credit-only lines no goods came back for) is inspected
		// empty; the store refuses an empty inspection when a line was received.
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		p, _ := tenant.FromContext(r.Context())
		saved, err := a.store.InspectReturnOrder(r.Context(), id, version, lines, p.UserID, a.now().UTC())
		if err != nil {
			a.fail(w, r, "inspect return order", err)
			return
		}
		setETag(w, saved.Version)
		httpx.JSON(w, http.StatusOK, saved)
	})(w, r)
}

func (a *API) dispositionReturnOrder(w http.ResponseWriter, r *http.Request) {
	a.rmaCommand(w, r, "disposition return order", a.store.DispositionReturnOrder)
}

// creditReturnOrder posts the credit. With ?recompute=true on a CREDITED return whose credit was not confirmed it first
// works the credit out again from the invoice as billing holds it now, so a credit that receivables released with :cancel
// can be corrected and sent again under the same RMA key.
func (a *API) creditReturnOrder(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("recompute") != "true" {
		a.rmaCommand(w, r, "credit return order", a.store.CreditReturnOrder)
		return
	}
	a.rmaCommand(w, r, "credit return order", func(ctx context.Context, id string, version int, actor string, now time.Time) (store.ReturnOrder, error) {
		o, err := a.store.RecomputeReturnCredit(ctx, id, version, actor, now)
		if err != nil {
			return store.ReturnOrder{}, err
		}
		return a.store.CreditReturnOrder(ctx, id, o.Version, actor, now)
	})
}

// backfillReservations drives receivables' :backfill-reservation for the credits sales posted before billing
// reservations existed; call it again while `more` is true.
func (a *API) backfillReservations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			validationFailed(w, r, map[string]string{"limit": "must be a whole number of 1 or more"})
			return
		}
		limit = n
	}
	if !inScope(w, r, le) {
		return
	}
	out, err := a.store.BackfillReservations(r.Context(), le, limit)
	if err != nil {
		a.fail(w, r, "backfill rma reservations", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) receiveReturnOrder(w http.ResponseWriter, r *http.Request) {
	returnRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var lines []store.ReceiveLine
		if r.ContentLength != 0 {
			var req receiveReturnRequest
			if err := httpx.Decode(r, &req); err != nil {
				httpx.Fail(w, r, err)
				return
			}
			fields := map[string]string{}
			seen := map[int]bool{}
			for i, l := range req.Lines {
				name := fmt.Sprintf("lines[%d]", i)
				if l.LineNo < 1 || seen[l.LineNo] {
					fields[name+".lineNo"] = "must be a line number, once per call"
				}
				seen[l.LineNo] = true
				qty, err := decimal.NewFromString(l.Quantity)
				if err != nil || qty.IsNegative() {
					fields[name+".quantity"] = "must be a decimal of 0 or more"
				}
				lines = append(lines, store.ReceiveLine{LineNo: l.LineNo, Quantity: qty})
			}
			if len(fields) > 0 {
				validationFailed(w, r, fields)
				return
			}
		}
		p, _ := tenant.FromContext(r.Context())
		saved, err := a.store.ReceiveReturnOrder(r.Context(), id, version, lines, p.UserID, a.now().UTC())
		if err != nil {
			a.fail(w, r, "receive return order", err)
			return
		}
		setETag(w, saved.Version)
		httpx.JSON(w, http.StatusOK, saved)
	})(w, r)
}

func (a *API) rmaCommand(w http.ResponseWriter, r *http.Request, op string, fn func(context.Context, string, int, string, time.Time) (store.ReturnOrder, error)) {
	returnRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		p, _ := tenant.FromContext(r.Context())
		saved, err := fn(r.Context(), id, version, p.UserID, a.now().UTC())
		if err != nil {
			a.fail(w, r, op, err)
			return
		}
		setETag(w, saved.Version)
		httpx.JSON(w, http.StatusOK, saved)
	})(w, r)
}
