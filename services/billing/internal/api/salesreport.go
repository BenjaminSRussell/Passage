package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/billing/internal/store"
)

const reportStatuses = "PENDING, REPORTED or PARKED"

type SalesReports interface {
	ListSalesReportsPage(ctx context.Context, status string, limit int, after string) ([]store.SalesReport, string, error)
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
	fields, limit, after := map[string]string{}, maxListLimit, ""
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxListLimit {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", maxListLimit)
		}
		limit = n
	}
	if raw := r.URL.Query().Get("cursor"); raw != "" {
		b, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			fields["cursor"] = "must be a nextCursor returned by a previous page"
		}
		after = string(b)
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	items, next, err := a.salesReports.ListSalesReportsPage(r.Context(), status, limit, after)
	if errors.Is(err, store.ErrBadReportCursor) {
		writeFields(w, r, map[string]string{"cursor": "must be a nextCursor returned by a previous page"})
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := map[string]any{"items": items}
	if next != "" {
		out["nextCursor"] = base64.RawURLEncoding.EncodeToString([]byte(next))
	}
	httpx.JSON(w, http.StatusOK, out)
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
