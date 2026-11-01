package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// runKPIs answers the measures of the jobs a run still holds: lateness,
// changeover minutes and per-resource utilization (PRD 4.23.14).
func (a *API) runKPIs(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "schedule run not found"))
		return
	}
	k, err := a.store.RunKPIs(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, k)
}

// lateOrders lists orders past their due date or holding an unplaced
// operation, the Late orders inquiry (PRD 4.23.14).
func (a *API) lateOrders(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.LateOrderFilter{MinLateMinutes: 1, Limit: 100}
	if raw := q.Get("minLateMinutes"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 525600 {
			invalid(w, r, map[string]string{"minLateMinutes": "must be 1 to 525600"})
			return
		}
		f.MinLateMinutes = n
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxLateOrders {
			invalid(w, r, map[string]string{"limit": fmt.Sprintf("must be 1 to %d", store.MaxLateOrders)})
			return
		}
		f.Limit = n
	}
	items, truncated, err := a.store.LateOrders(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}
