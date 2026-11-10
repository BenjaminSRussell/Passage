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

type TaxReports interface {
	ListTaxReports(ctx context.Context, status string, limit int) ([]store.TaxReport, error)
	RetryTaxReport(ctx context.Context, id string) (store.TaxReport, error)
}

func (a *API) WithTaxReports(s TaxReports) *API {
	a.taxReports = s
	return a
}

func (a *API) taxReportsReady(w http.ResponseWriter, r *http.Request) bool {
	if a.taxReports == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "BIL-0122", "tax reports are not available in this deployment"))
		return false
	}
	return true
}

func (a *API) listTaxReports(w http.ResponseWriter, r *http.Request) {
	if !a.taxReportsReady(w, r) {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != store.TaxReportPending && status != store.TaxReportReported && status != store.TaxReportParked {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", fmt.Sprintf("status must be %s", reportStatuses)))
		return
	}
	items, err := a.taxReports.ListTaxReports(r.Context(), status, maxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) retryTaxReport(w http.ResponseWriter, r *http.Request) {
	if !a.taxReportsReady(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeTaxReportNotFound, "tax report not found"))
		return
	}
	report, err := a.taxReports.RetryTaxReport(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, report)
}
