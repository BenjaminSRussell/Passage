package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// registerProcessCategories mounts the process message category master routes (FR-25.17).
func (a *API) registerProcessCategories(r chi.Router) {
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/process-categories", a.createProcessCategory)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/process-categories", a.listProcessCategories)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/process-categories/{id}", a.getProcessCategory)
	r.With(authz.RequireDuty(DutySetup)).Put("/v1/shopfloor/process-categories/{id}", a.updateProcessCategory)
}

type processCategoryRequest struct {
	Code             string                     `json:"code"`
	Name             string                     `json:"name"`
	Destinations     []string                   `json:"destinations"`
	RejectOutOfLimit bool                       `json:"rejectOutOfLimit"`
	Characteristics  []domain.CharacteristicDef `json:"characteristics"`
	Active           *bool                      `json:"active"`
}

func (req processCategoryRequest) input() domain.CategoryInput {
	return domain.CategoryInput{Code: strings.TrimSpace(req.Code), Name: strings.TrimSpace(req.Name), Destinations: req.Destinations,
		RejectOutOfLimit: req.RejectOutOfLimit, Characteristics: req.Characteristics, Active: req.Active == nil || *req.Active}
}

func (a *API) createProcessCategory(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a process category")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req processCategoryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := req.input()
	if fields := in.FieldErrors(true); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.CreateProcessCategory(r.Context(), in, user, idem)
	if err != nil {
		a.fail(w, r, "create process category", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) updateProcessCategory(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a process category")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeCategoryNotFound, "process category")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req processCategoryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := req.input()
	if fields := in.FieldErrors(false); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.UpdateProcessCategory(r.Context(), id, version, in, user)
	if err != nil {
		a.fail(w, r, "update process category", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getProcessCategory(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeCategoryNotFound, "process category")
	if !ok {
		return
	}
	out, err := a.store.GetProcessCategory(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get process category", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listProcessCategories(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	active, ok := activeFilter(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListProcessCategories(r.Context(), active, limit)
	if err != nil {
		a.fail(w, r, "list process categories", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.ProcessCategory]{Items: items})
}
