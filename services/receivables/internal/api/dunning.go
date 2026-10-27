package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/domain"
	"github.com/BenjaminSRussell/passage/services/receivables/internal/store"
)

const (
	dutyDunning  = "DunningRun"
	maxGraceDays = 60
)

type createDunningRunRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	RunDate       string `json:"runDate"`
	GraceDays     *int   `json:"graceDays"`
}

// createDunningRun handles POST /dunning-runs. The run is proposed as DRAFT.
func (a *API) createDunningRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key, ok := requiredKey(w, r)
	if !ok {
		return
	}
	var req createDunningRunRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if _, err := store.ParseDay(req.RunDate); err != nil {
		fields["runDate"] = "must be a YYYY-MM-DD date"
	}
	grace := domain.DefaultGraceDays
	if req.GraceDays != nil {
		grace = *req.GraceDays
	}
	if grace < 0 || grace > maxGraceDays {
		fields["graceDays"] = fmt.Sprintf("must be 0 to %d", maxGraceDays)
	}
	if len(fields) > 0 {
		writeFields(w, r, fields)
		return
	}
	if !scopeAllows(w, r, req.LegalEntityID) {
		return
	}
	out, err := a.store.CreateDunningRun(r.Context(), store.DunningRun{LegalEntityID: req.LegalEntityID,
		RunDate: req.RunDate, GraceDays: grace}, key)
	if err != nil {
		a.fail("create dunning run", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

// getDunningRun handles GET /dunning-runs/{id}.
func (a *API) getDunningRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "ref")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "dunning run not found"))
		return
	}
	out, err := a.store.GetDunningRun(r.Context(), id)
	if err != nil {
		a.fail("get dunning run", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// dunningRunAction handles POST /dunning-runs/{id}:review, :issue and :cancel.
func (a *API) dunningRunAction(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, action, found := strings.Cut(chi.URLParam(r, "ref"), ":")
	if !found || !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "dunning run not found"))
		return
	}
	targets := map[string]string{"review": "REVIEWED", "issue": "ISSUED", "cancel": "CANCELLED"}
	to, ok := targets[action]
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "unknown dunning run action "+action))
		return
	}
	if !require(w, r, dutyDunning) {
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.store.TransitionDunningRun(r.Context(), id, version, to)
	if err != nil {
		a.fail("transition dunning run", w, r, err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

// listDunningRuns handles GET /dunning-runs?status=.
func (a *API) listDunningRuns(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	status := r.URL.Query().Get("status")
	if status != "" && status != "DRAFT" && status != "REVIEWED" && status != "ISSUED" && status != "CANCELLED" {
		badQuery(w, r, "status", "must be DRAFT, REVIEWED, ISSUED or CANCELLED")
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListDunningRuns(r.Context(), status, limit)
	if err != nil {
		a.fail("list dunning runs", w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// dunningRoutes mounts the dunning endpoints (FR-30.7). The custom methods share
// one route and check the duty inside dunningRunAction.
func (a *API) dunningRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyDunning)).Post("/dunning-runs", a.createDunningRun)
	r.With(authz.RequireDuty(dutyView)).Get("/dunning-runs", a.listDunningRuns)
	r.With(authz.RequireDuty(dutyView)).Get("/dunning-runs/{ref}", a.getDunningRun)
	r.Post("/dunning-runs/{ref}", a.dunningRunAction)
}
