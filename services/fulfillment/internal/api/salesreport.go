package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

var salesReportStatuses = map[string]bool{store.SalesReportPending: true, store.SalesReportReported: true, store.SalesReportParked: true, store.SalesReportWithdrawn: true}

// routesSalesReport mounts the reports of posted packing slips to sales'
// :record-progress (#496): the list, to see what is pending or parked, and
// :retry, which puts a parked report back in the reporter's queue.
func (a *API) routesSalesReport(r chi.Router) {
	r.With(authz.RequireDuty("fulfillment.warehouse.view")).Get("/v1/fulfillment/sales-delivery-reports", a.listSalesReports)
	r.With(authz.RequireDuty("fulfillment.packing-slip.post")).Post("/v1/fulfillment/sales-delivery-reports/{id}:retry", a.retrySalesReport)
}

func (a *API) listSalesReports(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.SalesReportFilter{Status: q.Get("status"), PackingSlipID: q.Get("packingSlipId")}
	if f.Status != "" && !salesReportStatuses[f.Status] {
		fail(w, r, "status", "must be PENDING, REPORTED, PARKED or WITHDRAWN")
		return
	}
	if f.PackingSlipID != "" && !ids.Valid(f.PackingSlipID) {
		fail(w, r, "packingSlipId", "must be a UUID")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	f.Limit = limit
	items, err := a.store.ListSalesReports(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.SalesReport]{Items: items})
}

// retrySalesReport answers POST /v1/fulfillment/sales-delivery-reports/{id}:retry.
func (a *API) retrySalesReport(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeSalesReportNotFound, "sales delivery report not found"))
		return
	}
	_, idem, err := command(r, nil)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	rep, err := a.store.RetrySalesReport(r.Context(), id, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rep)
}
