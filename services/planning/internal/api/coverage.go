package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

// coverageGroupDTO is the wire form of a coverage group (FR-22.2). Version is
// also sent as the ETag.
type coverageGroupDTO struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
	Code    string `json:"code"`
	coverageSettings
}

// coverageSettings are the fields a create or update sends. A field the
// caller omits keeps the FR-22.2 default set before decoding (an explicit
// null leaves the default too).
type coverageSettings struct {
	CoverageCode          string `json:"coverageCode"`
	CoverageTimeFenceDays int    `json:"coverageTimeFenceDays"`
	NegativeDays          int    `json:"negativeDays"`
	PositiveDays          int    `json:"positiveDays"`
	PeriodDays            int    `json:"periodDays"`
	FreezeFenceDays       int    `json:"freezeFenceDays"`
	ActionFenceDays       int    `json:"actionFenceDays"`
	FuturesFenceDays      int    `json:"futuresFenceDays"`
	FirmingFenceDays      int    `json:"firmingFenceDays"`
	AutoFirm              bool   `json:"autoFirm"`
	GenerateActions       bool   `json:"generateActions"`
	GenerateFutures       bool   `json:"generateFutures"`
}

type createCoverageGroupRequest struct {
	Code string `json:"code"`
	coverageSettings
}

func defaultSettings() coverageSettings {
	d := domain.DefaultCoverageGroup("", "")
	return coverageSettings{CoverageTimeFenceDays: d.CoverageTimeFenceDays, NegativeDays: d.NegativeDays,
		GenerateActions: d.GenerateActions, GenerateFutures: d.GenerateFutures}
}

func (c coverageSettings) spec(code string) domain.CoverageGroupSpec {
	return domain.CoverageGroupSpec{Code: code, CoverageCode: c.CoverageCode, CoverageTimeFenceDays: c.CoverageTimeFenceDays,
		NegativeDays: c.NegativeDays, PositiveDays: c.PositiveDays, PeriodDays: c.PeriodDays, FreezeFenceDays: c.FreezeFenceDays,
		ActionFenceDays: c.ActionFenceDays, FuturesFenceDays: c.FuturesFenceDays, FirmingFenceDays: c.FirmingFenceDays,
		AutoFirm: c.AutoFirm, GenerateActions: c.GenerateActions, GenerateFutures: c.GenerateFutures}
}

func coverageDTOOf(g store.CoverageGroup) coverageGroupDTO {
	s := g.Spec
	return coverageGroupDTO{ID: g.ID, Version: g.Version, Code: s.Code, coverageSettings: coverageSettings{
		CoverageCode: s.CoverageCode, CoverageTimeFenceDays: s.CoverageTimeFenceDays, NegativeDays: s.NegativeDays,
		PositiveDays: s.PositiveDays, PeriodDays: s.PeriodDays, FreezeFenceDays: s.FreezeFenceDays, ActionFenceDays: s.ActionFenceDays,
		FuturesFenceDays: s.FuturesFenceDays, FirmingFenceDays: s.FirmingFenceDays, AutoFirm: s.AutoFirm,
		GenerateActions: s.GenerateActions, GenerateFutures: s.GenerateFutures}}
}

func writeCoverageGroup(w http.ResponseWriter, status int, g store.CoverageGroup) {
	w.Header().Set("ETag", etagOf(g.Version))
	httpx.JSON(w, status, coverageDTOOf(g))
}

// coverageCodeParam reads {code}; an ill-formed code cannot exist, so it is 404.
func coverageCodeParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	code := chi.URLParam(r, "code")
	if !planCodePattern.MatchString(code) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeCoverageNotFound, "coverage group not found"))
		return "", false
	}
	return code, true
}

// listCoverageGroups returns the coverage groups of the tenant ordered by code.
func (a *API) listCoverageGroups(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	groups, err := a.store.ListCoverageGroups(r.Context(), limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	out := make([]coverageGroupDTO, 0, len(groups))
	for _, g := range groups {
		out = append(out, coverageDTOOf(g))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (a *API) getCoverageGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code, ok := coverageCodeParam(w, r)
	if !ok {
		return
	}
	g, err := a.store.GetCoverageGroup(r.Context(), code)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeCoverageGroup(w, http.StatusOK, g)
}

// createCoverageGroup stores a new coverage group (FR-22.2). An optional
// Idempotency-Key makes a retry return the first result with 200 and
// Idempotent-Replayed: true.
func (a *API) createCoverageGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := r.Header.Get(idempotencyKey)
	if len(key) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "must be at most 255 characters"})
		return
	}
	req := createCoverageGroupRequest{coverageSettings: defaultSettings()}
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
	g, replayed, err := a.store.CreateCoverageGroup(r.Context(), store.CoverageCreate{
		IdempotencyKey: key, InputHash: fmt.Sprintf("%x", sha256.Sum256(body)), Spec: spec,
	})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		writeCoverageGroup(w, http.StatusOK, g)
		return
	}
	writeCoverageGroup(w, http.StatusCreated, g)
}

// updateCoverageGroup replaces the settings of a coverage group under
// If-Match; omitted fields take the FR-22.2 defaults, as on create.
func (a *API) updateCoverageGroup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code, ok := coverageCodeParam(w, r)
	if !ok {
		return
	}
	expected, ok := ifMatch(w, r, 1)
	if !ok {
		return
	}
	req := defaultSettings()
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	spec := req.spec(code)
	if fields := spec.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	g, err := a.store.UpdateCoverageGroup(r.Context(), expected, spec)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	writeCoverageGroup(w, http.StatusOK, g)
}
