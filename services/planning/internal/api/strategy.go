package api

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// strategyDTO is the wire form of a planning strategy (FR-22.19). A strategy
// with no stored setup is returned with the PRD defaults and configured false
// (no id, no version); Version is also sent as the ETag of a stored one.
type strategyDTO struct {
	Code       string `json:"code"`
	Configured bool   `json:"configured"`
	ID         string `json:"id,omitempty"`
	Version    int    `json:"version,omitempty"`
	strategySettings
}

// strategySettings are the fields of a strategy on the wire.
type strategySettings struct {
	ConsumptionMode string `json:"consumptionMode"`
	BackwardDays    int    `json:"backwardDays"`
	ForwardDays     int    `json:"forwardDays"`
	UsesMarking     bool   `json:"usesMarking"`
	SegregatedStock bool   `json:"segregatedStock"`
}

// strategyInput is what a create or update sends; a field left out (or null)
// takes the default of the strategy.
type strategyInput struct {
	Code            string  `json:"code"`
	ConsumptionMode *string `json:"consumptionMode"`
	BackwardDays    *int    `json:"backwardDays"`
	ForwardDays     *int    `json:"forwardDays"`
	UsesMarking     *bool   `json:"usesMarking"`
	SegregatedStock *bool   `json:"segregatedStock"`
}

// spec lays the input over the defaults of code.
func (in strategyInput) spec(code string) domain.StrategySpec {
	s := domain.DefaultStrategy(code)
	if in.ConsumptionMode != nil {
		s.ConsumptionMode = *in.ConsumptionMode
	}
	if in.BackwardDays != nil {
		s.BackwardDays = *in.BackwardDays
	}
	if in.ForwardDays != nil {
		s.ForwardDays = *in.ForwardDays
	}
	if in.UsesMarking != nil {
		s.UsesMarking = *in.UsesMarking
	}
	if in.SegregatedStock != nil {
		s.SegregatedStock = *in.SegregatedStock
	}
	return s
}

func settingsOf(s domain.StrategySpec) strategySettings {
	return strategySettings{ConsumptionMode: s.ConsumptionMode, BackwardDays: s.BackwardDays, ForwardDays: s.ForwardDays,
		UsesMarking: s.UsesMarking, SegregatedStock: s.SegregatedStock}
}

func strategyDTOOf(st store.Strategy) strategyDTO {
	return strategyDTO{Code: st.Spec.Code, Configured: true, ID: st.ID, Version: st.Version, strategySettings: settingsOf(st.Spec)}
}

func defaultStrategyDTO(code string) strategyDTO {
	return strategyDTO{Code: code, strategySettings: settingsOf(domain.DefaultStrategy(code))}
}

func writeStrategy(w http.ResponseWriter, status int, st store.Strategy) {
	w.Header().Set("ETag", etagOf(st.Version))
	httpx.JSON(w, status, strategyDTOOf(st))
}

func (a *API) registerStrategies(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/strategies", a.listStrategies)
	r.With(authz.RequireDuty(dutyMaintainSetup)).Post("/v1/planning/strategies", a.createStrategy)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/strategies/{code}", a.getStrategy)
	r.With(authz.RequireDuty(dutyMaintainSetup)).Put("/v1/planning/strategies/{code}", a.updateStrategy)
}

// strategyCodeParam reads {code}; a code that is not one of the six strategies is 404.
func strategyCodeParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	code := chi.URLParam(r, "code")
	if !domain.ValidStrategy(code) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeStrategyNotFound, "unknown planning strategy"))
		return "", false
	}
	return code, true
}

// listStrategies returns the six strategies in setup order: the stored setup of
// each, or its defaults.
func (a *API) listStrategies(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	stored, err := a.store.ListStrategies(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	byCode := make(map[string]store.Strategy, len(stored))
	for _, st := range stored {
		byCode[st.Spec.Code] = st
	}
	out := make([]strategyDTO, 0, len(domain.StrategyCodes()))
	for _, code := range domain.StrategyCodes() {
		if st, ok := byCode[code]; ok {
			out = append(out, strategyDTOOf(st))
		} else {
			out = append(out, defaultStrategyDTO(code))
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) getStrategy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code, ok := strategyCodeParam(w, r)
	if !ok {
		return
	}
	st, err := a.store.GetStrategy(r.Context(), code)
	var p *httpx.Problem
	if errors.As(err, &p) && p.Code == store.CodeStrategyNotFound {
		httpx.JSON(w, http.StatusOK, defaultStrategyDTO(code))
		return
	}
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeStrategy(w, http.StatusOK, st)
}

// createStrategy stores the setup of a strategy (FR-22.19). An optional
// Idempotency-Key makes a retry return the first result with 200 and
// Idempotent-Replayed: true.
func (a *API) createStrategy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if len(key) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "must be at most 255 characters"})
		return
	}
	var req strategyInput
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	spec := req.spec(req.Code)
	if fields := spec.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	body, _ := json.Marshal(spec)
	st, replayed, err := a.store.CreateStrategy(r.Context(), store.StrategyCreate{
		IdempotencyKey: key, InputHash: fmt.Sprintf("%x", sha256.Sum256(body)), Spec: spec,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		writeStrategy(w, http.StatusOK, st)
		return
	}
	writeStrategy(w, http.StatusCreated, st)
}

// updateStrategy replaces the setup of a strategy under If-Match; omitted
// fields take the defaults of the strategy, as on create.
func (a *API) updateStrategy(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code, ok := strategyCodeParam(w, r)
	if !ok {
		return
	}
	expected, ok := ifMatch(w, r, 1)
	if !ok {
		return
	}
	var req strategyInput
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Code != "" && req.Code != code {
		validationFailed(w, r, map[string]string{"code": "must match the strategy in the path"})
		return
	}
	spec := req.spec(code)
	if fields := spec.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	st, err := a.store.UpdateStrategy(r.Context(), expected, spec)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeStrategy(w, http.StatusOK, st)
}
