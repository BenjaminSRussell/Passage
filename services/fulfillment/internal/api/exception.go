package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/fulfillment/internal/store"
)

// routesException mounts the warehouse supervisor's exception list: cases fulfillment cannot
// fix alone, such as a packing slip posted before sales cancelled the order remainder.
func (a *API) routesException(r chi.Router) {
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/exceptions", a.listExceptions)
	r.With(authz.RequireDuty("WarehouseView")).Get("/v1/fulfillment/exceptions/{id}", a.getException)
	r.With(authz.RequireDuty("WarehouseRelease")).Post("/v1/fulfillment/exceptions/{id}:resolve", a.resolveException)
}

func (a *API) listExceptions(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && status != "OPEN" && status != "RESOLVED" {
		fail(w, r, "status", "must be OPEN or RESOLVED")
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListExceptions(r.Context(), store.ExceptionFilter{Status: status, Limit: limit})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Exception]{Items: items})
}

func exceptionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeExceptionNotFound, "exception not found"))
		return "", false
	}
	return id, true
}

func (a *API) getException(w http.ResponseWriter, r *http.Request) {
	id, ok := exceptionID(w, r)
	if !ok {
		return
	}
	e, err := a.store.GetException(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, e.Version)
	httpx.JSON(w, http.StatusOK, e)
}

type resolveExceptionRequest struct {
	Note string `json:"note"`
}

// resolveException answers POST /v1/fulfillment/exceptions/{id}:resolve.
func (a *API) resolveException(w http.ResponseWriter, r *http.Request) {
	id, ok := exceptionID(w, r)
	if !ok {
		return
	}
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, CodePreconditionIf, "If-Match with the exception version is required"))
		return
	}
	version, err := strconv.Atoi(raw)
	if err != nil || version < 1 {
		fail(w, r, "If-Match", "must be the exception version as an integer")
		return
	}
	var req resolveExceptionRequest
	_, idem, err := command(r, &req)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	note := strings.TrimSpace(req.Note)
	if note == "" || len(note) > 500 {
		fail(w, r, "note", "is required, 1 to 500 characters")
		return
	}
	e, err := a.store.ResolveException(r.Context(), id, version, note, idem)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	etag(w, e.Version)
	httpx.JSON(w, http.StatusOK, e)
}
