package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const reportStatuses = "PENDING, REPORTED or PARKED"

type SalesReports interface {
	ListSalesReports(ctx context.Context, status string, limit int) ([]store.SalesReport, error)
	RetrySalesReport(ctx context.Context, id string) (store.SalesReport, error)
}

func (a *API) WithSalesReports(s SalesReports) *API {
	a.salesReports = s
	return a
}

func (a *API) salesReportsReady(w http.ResponseWriter, r *http.Request) bool {
	if a.salesReports == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "BIL-0112", "sales reports are not available in this deployment"))
		return false
	}
	return true
}

func (a *API) listSalesReports(w http.ResponseWriter, r *http.Request) {
	if !a.salesReportsReady(w, r) {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != store.SalesReportPending && status != store.SalesReportReported && status != store.SalesReportParked {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", fmt.Sprintf("status must be %s", reportStatuses)))
		return
	}
	items, err := a.salesReports.ListSalesReports(r.Context(), status, maxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) retrySalesReport(w http.ResponseWriter, r *http.Request) {
	if !a.salesReportsReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeSalesReportNotFound, "sales report not found"))
		return
	}
	report, err := a.salesReports.RetrySalesReport(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, report)
}
