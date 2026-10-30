package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

func (a *API) registerScenarios(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/scenarios", a.listScenarios)
	r.With(authz.RequireDuty(dutySimulate)).Post("/v1/planning/scenarios", a.createScenario)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/scenarios/{code}", a.getScenario)
	r.With(authz.RequireDuty(dutySimulate)).Put("/v1/planning/scenarios/{code}", a.updateScenario)
	r.With(authz.RequireDuty(dutySimulate)).Post("/v1/planning/scenarios/{code}:run", a.runScenario)
}

// scenarioDTO is the wire form of a scenario (FR-22.15). Version is also the ETag.
type scenarioDTO struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Code    string `json:"code"`
	scenarioSettings
}

// scenarioSettings are the fields a create or update sends. Omitted fields
// keep the defaults set before decoding: demand factor 1 and active.
type scenarioSettings struct {
	PlanCode          string                    `json:"planCode"`
	Name              string                    `json:"name"`
	DemandFactor      decimal.Decimal           `json:"demandFactor"`
	ItemFactors       []domain.ItemFactor       `json:"itemFactors"`
	AddedDemand       []domain.AddedDemand      `json:"addedDemand"`
	LeadTimeOverrides []domain.LeadTimeOverride `json:"leadTimeOverrides"`
	Active            bool                      `json:"active"`
}

type createScenarioRequest struct {
	Code string `json:"code"`
	scenarioSettings
}

func defaultScenario() scenarioSettings {
	return scenarioSettings{DemandFactor: decimal.NewFromInt(1), Active: true}
}

func (c scenarioSettings) spec(code string) domain.ScenarioSpec {
	return domain.ScenarioSpec{Code: code, PlanCode: c.PlanCode, Name: c.Name, DemandFactor: c.DemandFactor, ItemFactors: c.ItemFactors,
		AddedDemand: c.AddedDemand, LeadTimeOverrides: c.LeadTimeOverrides, Active: c.Active}
}

func scenarioDTOOf(sc store.Scenario) scenarioDTO {
	s := sc.Spec
	d := scenarioDTO{ID: sc.ID, Version: sc.Version, Code: s.Code, scenarioSettings: scenarioSettings{
		PlanCode: s.PlanCode, Name: s.Name, DemandFactor: s.DemandFactor, ItemFactors: s.ItemFactors, AddedDemand: s.AddedDemand,
		LeadTimeOverrides: s.LeadTimeOverrides, Active: s.Active}}
	if d.ItemFactors == nil {
		d.ItemFactors = []domain.ItemFactor{}
	}
	if d.AddedDemand == nil {
		d.AddedDemand = []domain.AddedDemand{}
	}
	if d.LeadTimeOverrides == nil {
		d.LeadTimeOverrides = []domain.LeadTimeOverride{}
	}
	return d
}

func writeScenario(w http.ResponseWriter, status int, sc store.Scenario) {
	w.Header().Set("ETag", etagOf(sc.Version))
	httpx.JSON(w, status, scenarioDTOOf(sc))
}

// scenarioCodeParam reads {code}; an ill-formed code cannot exist, so it is 404.
func scenarioCodeParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	code := chi.URLParam(r, "code")
	if !planCodePattern.MatchString(code) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeScenarioNotFound, "planning scenario not found"))
		return "", false
	}
	return code, true
}

func (a *API) listScenarios(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	plan := r.URL.Query().Get("planCode")
	if plan != "" && !planCodePattern.MatchString(plan) {
		validationFailed(w, r, map[string]string{"planCode": codeMessage})
		return
	}
	list, err := a.store.ListScenarios(r.Context(), store.ScenarioQuery{PlanCode: plan, Limit: limit})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]scenarioDTO, 0, len(list))
	for _, sc := range list {
		out = append(out, scenarioDTOOf(sc))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) getScenario(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code, ok := scenarioCodeParam(w, r)
	if !ok {
		return
	}
	sc, err := a.store.GetScenario(r.Context(), code)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeScenario(w, http.StatusOK, sc)
}

// createScenario stores a new scenario (FR-22.15). An optional
// Idempotency-Key makes a retry return the first result with 200 and
// Idempotent-Replayed: true.
func (a *API) createScenario(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if len(key) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "must be at most 255 characters"})
		return
	}
	req := createScenarioRequest{scenarioSettings: defaultScenario()}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	spec := req.spec(req.Code)
	if fields := spec.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	body, _ := json.Marshal(scenarioDTOOf(store.Scenario{Spec: spec}))
	sc, replayed, err := a.store.CreateScenario(r.Context(), store.ScenarioCreate{
		IdempotencyKey: key, InputHash: fmt.Sprintf("%x", sha256.Sum256(body)), Spec: spec,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		writeScenario(w, http.StatusOK, sc)
		return
	}
	writeScenario(w, http.StatusCreated, sc)
}

// updateScenario replaces a scenario's settings under If-Match; omitted fields
// take the defaults, as on create. The plan cannot change: planCode may be
// left out or must equal the stored one.
func (a *API) updateScenario(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code, ok := scenarioCodeParam(w, r)
	if !ok {
		return
	}
	expected, ok := ifMatch(w, r, 1)
	if !ok {
		return
	}
	req := defaultScenario()
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	current, err := a.store.GetScenario(r.Context(), code)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.PlanCode == "" {
		req.PlanCode = current.Spec.PlanCode
	}
	spec := req.spec(code)
	if fields := spec.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	sc, err := a.store.UpdateScenario(r.Context(), expected, spec)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeScenario(w, http.StatusOK, sc)
}

// runScenario runs the scenario's plan on the posted snapshot with the
// scenario applied and stores the result as a simulation version (FR-22.15,
// BR-22.14): the operational current version, its messages and its events are
// untouched, and the version's planned orders cannot be firmed (409 PLAN-0003).
func (a *API) runScenario(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code, ok := scenarioCodeParam(w, r)
	if !ok {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if key == "" {
		validationFailed(w, r, map[string]string{idempotencyKey: "header is required on simulation runs"})
		return
	}
	var req runRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := req.validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	sc, err := a.store.GetScenario(r.Context(), code)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !sc.Spec.Active {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, store.CodeScenarioInactive, "scenario "+code+" is not active"))
		return
	}
	// The fingerprint covers the scenario as stored and the snapshot as sent.
	body, _ := json.Marshal(struct {
		Scenario scenarioDTO `json:"scenario"`
		Request  runRequest  `json:"request"`
	}{scenarioDTOOf(sc), req})
	asOf, items, ok := a.buildSnapshot(w, r, sc.Spec.PlanCode, req)
	if !ok {
		return
	}
	items, err = domain.ApplyScenario(items, asOf, sc.Spec)
	if err != nil {
		validationFailed(w, r, map[string]string{"scenario": strings.TrimPrefix(err.Error(), domain.ErrInvalidItem.Error()+": ")})
		return
	}
	started := a.now()
	result, err := domain.RunWithCalendar(0, req.Calendar.toDomain(asOf), items)
	if err != nil {
		a.runsRefused.Add(1)
		httpx.Fail(w, r, runProblem(err))
		return
	}
	v, replayed, err := a.store.RunSimulation(r.Context(), store.SimulationRequest{
		ScenarioCode: code, LegalEntityID: req.LegalEntityID, SiteID: req.SiteID, AsOf: asOf, ItemsPlanned: len(items),
		DurationMs: a.now().Sub(started).Milliseconds(), InputHash: fmt.Sprintf("%x", sha256.Sum256(body)), IdempotencyKey: key, Result: result,
	})
	if err != nil {
		slog.Error("simulation run failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, v)
		return
	}
	httpx.JSON(w, http.StatusCreated, v)
}
