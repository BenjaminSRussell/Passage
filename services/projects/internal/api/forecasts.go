package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

// forecastRequest is the wire shape of POST /v1/projects/{id}/forecasts and
// PUT /v1/projects/{id}/forecasts/{modelId}: the model name and all its lines
// (line numbers are assigned in order).
type forecastRequest struct {
	Name  string                `json:"name"`
	Lines []domain.ForecastLine `json:"lines"`
}

type forecastList struct {
	Items []domain.ForecastModel `json:"items"`
}

// forecastDetail is one forecast model with its lines, always present (the
// list leaves them out).
type forecastDetail struct {
	domain.ForecastModel
	Lines []domain.ForecastLine `json:"lines"`
}

// forecastView is a forecast model with its lines and the forecast-vs-actual rows.
type forecastView struct {
	Model      forecastDetail       `json:"model"`
	Comparison []domain.ForecastRow `json:"comparison"`
}

// registerForecasts mounts the forecast models of FR-41.20: reads need
// ProjInquire, creating and replacing ProjPlan (§8.41.17).
func (a *API) registerForecasts(r chi.Router) {
	r.With(authz.RequireDuty(DutyInquire)).Get("/{id}/forecasts", a.listForecasts)
	r.With(authz.RequireDuty(DutyPlan)).Post("/{id}/forecasts", a.createForecast)
	r.With(authz.RequireDuty(DutyInquire)).Get("/{id}/forecasts/{modelId}", a.getForecast)
	r.With(authz.RequireDuty(DutyPlan)).Put("/{id}/forecasts/{modelId}", a.putForecast)
}

// forecastID reads {modelId}; a malformed id is 404 like an unknown one.
func forecastID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "modelId")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeNotFound, "forecast model not found"))
		return "", false
	}
	return id, true
}

func withLines(m domain.ForecastModel) forecastDetail {
	d := forecastDetail{ForecastModel: m, Lines: m.Lines}
	if d.Lines == nil {
		d.Lines = []domain.ForecastLine{}
	}
	return d
}

func (a *API) listForecasts(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	items, err := a.store.ListForecastModels(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if items == nil {
		items = []domain.ForecastModel{}
	}
	httpx.JSON(w, http.StatusOK, forecastList{Items: items})
}

// createForecast adds a forecast model. Idempotency-Key is required; a replay
// returns the stored model with 200 and Idempotent-Replayed: true. Lines are
// validated against the project currency and WORKING plan (400 with field
// errors); a duplicate name is 422 PROJ-FC-001.
func (a *API) createForecast(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok || !requireUser(w, r, p) {
		return
	}
	var req forecastRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key, fields := idempotencyKey(r)
	if fields != nil {
		httpx.Fail(w, r, validation(fields))
		return
	}
	out, created, err := a.store.CreateForecastModel(r.Context(), chi.URLParam(r, "id"), store.ForecastInput{Name: req.Name, Lines: req.Lines}, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	if !created {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, createdStatus(created), withLines(out))
}

// getForecast returns a forecast model with its lines and forecast vs actual by
// period and transaction type (§8.41.5 GET /{id}/forecasts/{modelId}).
func (a *API) getForecast(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := forecastID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetForecast(r.Context(), chi.URLParam(r, "id"), id)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	rows := out.Rows
	if rows == nil {
		rows = []domain.ForecastRow{}
	}
	setETag(w, out.Model.Version)
	httpx.JSON(w, http.StatusOK, forecastView{Model: withLines(out.Model), Comparison: rows})
}

// putForecast renames a forecast model and replaces all its lines. If-Match
// carries the model version (428 when absent, 412 when stale).
func (a *API) putForecast(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	id, ok := forecastID(w, r)
	if !ok {
		return
	}
	expected, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	var req forecastRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out, err := a.store.ReplaceForecast(r.Context(), chi.URLParam(r, "id"), id, expected, store.ForecastInput{Name: req.Name, Lines: req.Lines})
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, withLines(out))
}
