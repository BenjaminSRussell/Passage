package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/domain"
	"github.com/BenjaminSRussell/passage/services/pricing/internal/store"
)

const (
	proceduresPath        = "/v1/pricing/procedures"
	procedurePath         = "/v1/pricing/procedures/{id}"
	procedureRevisePath   = "/v1/pricing/procedures/{id}:revise"
	procedureActivatePath = "/v1/pricing/procedures/{id}:activate"
	// Duty PricingAdmin (§5.26.17): procedures and their activation; the Procedure designer.
	dutyAdmin = "pricing.admin"
)

type procedureStepDTO struct {
	StepNo         int    `json:"stepNo"`
	ConditionType  string `json:"conditionType"`
	RequirementCEL string `json:"requirementCel"`
	ExclusionGroup string `json:"exclusionGroup"`
	ExclusionMode  string `json:"exclusionMode"`
	Statistical    bool   `json:"statistical"`
	AccountKey     string `json:"accountKey"`
}

type procedureRequest struct {
	LegalEntityID string             `json:"legalEntityId"`
	Name          string             `json:"name"`
	Steps         []procedureStepDTO `json:"steps"`
}

type procedureReviseRequest struct {
	Revision int                `json:"revision"`
	Steps    []procedureStepDTO `json:"steps"`
}

type procedureView struct {
	ID            string             `json:"id"`
	LegalEntityID string             `json:"legalEntityId"`
	Name          string             `json:"name"`
	Version       int                `json:"version"`
	Status        string             `json:"status"`
	Revision      int                `json:"revision"`
	Steps         []procedureStepDTO `json:"steps"`
}

func toSteps(in []procedureStepDTO) []domain.ProcedureStep {
	out := make([]domain.ProcedureStep, 0, len(in))
	for _, s := range in {
		out = append(out, domain.ProcedureStep{StepNo: s.StepNo, ConditionType: domain.ConditionType(s.ConditionType),
			RequirementCEL: s.RequirementCEL, ExclusionGroup: s.ExclusionGroup, ExclusionMode: s.ExclusionMode,
			Statistical: s.Statistical, AccountKey: s.AccountKey})
	}
	return out
}

func procedureResponse(p domain.Procedure) procedureView {
	steps := make([]procedureStepDTO, 0, len(p.Steps))
	for _, s := range p.Steps {
		steps = append(steps, procedureStepDTO{StepNo: s.StepNo, ConditionType: string(s.ConditionType), RequirementCEL: s.RequirementCEL,
			ExclusionGroup: s.ExclusionGroup, ExclusionMode: s.ExclusionMode, Statistical: s.Statistical, AccountKey: s.AccountKey})
	}
	return procedureView{ID: p.ID, LegalEntityID: p.LegalEntityID, Name: p.Name, Version: p.Version, Status: p.Status,
		Revision: p.Revision, Steps: steps}
}

// registerProcedureRoutes mounts the pricing procedure surface (FR-26.1). Writes need
// PricingAdmin; reads take PricingView.
func (a *API) registerProcedureRoutes(r chi.Router) {
	r.With(authz.RequireDuty(dutyAdmin)).Post(proceduresPath, a.createProcedure)
	r.With(authz.RequireDuty(dutyView)).Get(proceduresPath, a.listProcedures)
	r.With(authz.RequireDuty(dutyView)).Get(procedurePath, a.getProcedure)
	r.With(authz.RequireDuty(dutyAdmin)).Post(procedureRevisePath, a.reviseProcedure)
	r.With(authz.RequireDuty(dutyAdmin)).Post(procedureActivatePath, a.activateProcedure)
}

func (a *API) createProcedure(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req procedureRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	steps := toSteps(req.Steps)
	fields := domain.ValidateSteps(steps)
	if !ids.Valid(req.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if msg := domain.ValidateProcedureName(req.Name); msg != "" {
		fields["name"] = msg
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.CreateProcedure(r.Context(), domain.Procedure{LegalEntityID: req.LegalEntityID, Name: req.Name, Steps: steps},
		r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, procedureResponse(out))
}

func (a *API) listProcedures(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	le := r.URL.Query().Get("legalEntityId")
	if le != "" && !ids.Valid(le) {
		validationFailed(w, r, map[string]string{"legalEntityId": "must be a UUID"})
		return
	}
	rows, err := a.store.ListProcedures(r.Context(), le, store.MaxListLimit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]procedureView, 0, len(rows))
	for _, p := range rows {
		items = append(items, procedureResponse(p))
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getProcedure(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeProcedureNotFound, "pricing procedure")
	if !ok {
		return
	}
	p, err := a.store.GetProcedure(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, procedureResponse(p))
}

// reviseProcedure replaces the steps of a DRAFT version (the Procedure designer's save).
func (a *API) reviseProcedure(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeProcedureNotFound, "pricing procedure")
	if !ok {
		return
	}
	var req procedureReviseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	steps := toSteps(req.Steps)
	fields := domain.ValidateSteps(steps)
	if req.Revision < 1 {
		fields["revision"] = "is required and must be at least 1"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	p, err := a.store.ReviseProcedure(r.Context(), id, req.Revision, steps, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, procedureResponse(p))
}

// activateProcedure makes a DRAFT version ACTIVE; the previous ACTIVE version is superseded.
func (a *API) activateProcedure(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := uuidParam(w, r, domain.CodeProcedureNotFound, "pricing procedure")
	if !ok {
		return
	}
	p, err := a.store.ActivateProcedure(r.Context(), id, r.Header.Get(idempotencyKey))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, procedureResponse(p))
}
