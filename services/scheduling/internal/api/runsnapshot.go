package api

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// compareRuns answers the difference between two runs' job snapshots: the
// run in the path is the base, the run in ?to= the other (PRD 4.23.14).
func (a *API) compareRuns(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "schedule run not found"))
		return
	}
	to := strings.ToLower(r.URL.Query().Get("to"))
	if !ids.Valid(to) {
		invalid(w, r, map[string]string{"to": "must be the UUID of the run to compare with"})
		return
	}
	if to == id {
		invalid(w, r, map[string]string{"to": "must be a different run"})
		return
	}
	c, err := a.store.CompareRuns(r.Context(), id, to)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

// rollbackRun restores the jobs a previous run placed as the current plan,
// under a new run of scope rollback (PRD 4.23.14). It changes the plan, so it
// takes the run duty.
func (a *API) rollbackRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "schedule run not found"))
		return
	}
	var req struct{}
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	// The run is in the path: the same key sent for another run is a different request.
	idem.Fingerprint = fingerprint(id, body)
	out, replayed, err := a.store.RollbackRun(r.Context(), id, idem)
	if err != nil {
		slog.Error("run rollback failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if replayed {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, status, out)
}
