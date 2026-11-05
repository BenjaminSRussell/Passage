package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// listBottlenecks lists bottleneck episodes (AC-23.6), newest first.
func (a *API) listBottlenecks(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	if status != "" && status != "open" && status != "closed" && status != "all" {
		invalid(w, r, map[string]string{"status": "must be open, closed or all"})
		return
	}
	resourceID := strings.ToLower(q.Get("resourceId"))
	if resourceID != "" && !ids.Valid(resourceID) {
		invalid(w, r, map[string]string{"resourceId": "must be a resource UUID"})
		return
	}
	limit, ok := limitParam(w, r, 50, 200)
	if !ok {
		return
	}
	items, err := a.store.ListBottlenecks(r.Context(), store.BottleneckFilter{Status: status, ResourceID: resourceID, Limit: limit})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// getBottleneck returns one episode with the materialized weeks of its resource.
func (a *API) getBottleneck(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeBottleneckNotFound, "bottleneck episode not found"))
		return
	}
	d, err := a.store.GetBottleneck(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

// refreshCapacityLoad rebuilds the materialized load of every resource. It
// only recomputes what the bookings already say, so repeating it is harmless.
func (a *API) refreshCapacityLoad(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	out, err := a.store.RefreshCapacityLoad(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
