package api

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/sales/internal/store"
)

const backorders = "/v1/sales/backorders"

// promiseLineRequest is the body of POST /v1/sales/orders/{id}/lines/{lineNo}:promise:
// the ATP buckets to place the line against (stateless until the projection
// exists) and whether the whole quantity must ship on one date. Absent
// completeOnly follows the partial delivery policy of the product and customer.
type promiseLineRequest struct {
	CompleteOnly *bool              `json:"completeOnly"`
	Periods      []atpPeriodRequest `json:"periods"`
}

// promiseLine places one line of a confirmed order against ATP buckets and
// stores the schedule lines (FR-27.6, duty SalesOrderMaintain). If-Match
// carries the order ETag.
func (a *API) promiseLine(w http.ResponseWriter, r *http.Request) {
	orderRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		lineNo, err := strconv.Atoi(chi.URLParam(r, "lineNo"))
		if err != nil || lineNo < 1 {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeLineNotFound, "order line not found"))
			return
		}
		version, ok := ifMatch(w, r)
		if !ok {
			return
		}
		var req promiseLineRequest
		if err := httpx.Decode(r, &req); err != nil {
			httpx.Fail(w, r, err)
			return
		}
		fields := map[string]string{}
		if n := len(req.Periods); n == 0 || n > maxPeriods {
			fields["periods"] = fmt.Sprintf("must have 1 to %d buckets", maxPeriods)
		}
		periods := parsePeriods(req.Periods, fields)
		if len(fields) > 0 {
			validationFailed(w, r, fields)
			return
		}
		p, _ := tenant.FromContext(r.Context())
		res, err := a.store.PromiseLine(r.Context(), id, version,
			store.PromiseRequest{LineNo: lineNo, Periods: periods, CompleteOnly: req.CompleteOnly}, p.UserID, a.now())
		if err != nil {
			a.fail(w, r, "promise line", err)
			return
		}
		a.atp.Add(1)
		setETag(w, res.Order.Version)
		httpx.JSON(w, http.StatusOK, res)
	})(w, r)
}

// listScheduleLines returns the order's active schedule lines (FR-27.6).
func (a *API) listScheduleLines(w http.ResponseWriter, r *http.Request) {
	orderRoute(func(w http.ResponseWriter, r *http.Request, id string) {
		lines, err := a.store.ListScheduleLines(r.Context(), id)
		if err != nil {
			a.fail(w, r, "list schedule lines", err)
			return
		}
		httpx.JSON(w, http.StatusOK, map[string]any{"items": lines})
	})(w, r)
}

// listBackorders is the backorder list by promised date (FR-27.6, AC-27.8).
func (a *API) listBackorders(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	legalEntity, ok := creditLegalEntity(w, r, false)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.BackorderFilter{LegalEntityID: legalEntity, ProductID: q.Get("productId"), CustomerID: q.Get("customerId"), PromisedTo: q.Get("promisedTo")}
	fields := map[string]string{}
	if f.ProductID != "" && !ids.Valid(f.ProductID) {
		fields["productId"] = "must be a UUID"
	}
	if f.CustomerID != "" && !ids.Valid(f.CustomerID) {
		fields["customerId"] = "must be a UUID"
	}
	if f.PromisedTo != "" {
		if _, err := time.Parse(dayLayout, f.PromisedTo); err != nil {
			fields["promisedTo"] = "must be a date such as 2027-03-01"
		}
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
	items, err := a.store.ListBackorders(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list backorders", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
