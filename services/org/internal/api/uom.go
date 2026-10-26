package api

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	units             = "/v1/org/uom/units"
	maxUnitPrecision  = 6
	unitNameRule      = "must be 1 to 120 characters"
	unitPrecisionRule = "is required and must be an integer from 0 to 6"
)

type createUnitRequest struct {
	Code             string `json:"code"`
	Name             string `json:"name"`
	Class            string `json:"class"`
	DecimalPrecision *int   `json:"decimalPrecision"`
}

type unitList struct {
	Items []store.Unit `json:"items"`
}

func (a *API) registerUnits(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get(units, a.listUnits)
	r.With(authz.RequireDuty(dutyRead)).Get(units+"/{code}", a.getUnit)
	r.With(authz.RequireDuty(dutyReference)).Post(units, a.createUnit)
	r.With(authz.RequireDuty(dutyReference)).Patch(units+"/{code}", a.patchUnit)
}

func refuseScopedCaller(w http.ResponseWriter, r *http.Request, action string) bool {
	if p, _ := tenant.FromContext(r.Context()); p.LegalEntityID != "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a legal-entity-scoped caller cannot "+action))
		return true
	}
	return false
}

func (a *API) listUnits(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.UnitFilter{Class: q.Get("class"), Status: q.Get("status")}
	if f.Class != "" && !slices.Contains(store.UnitClasses(), f.Class) {
		badQuery(w, r, "class", "must be one of "+strings.Join(store.UnitClasses(), ", "))
		return
	}
	if f.Status != "" && !validStatus(f.Status) {
		badQuery(w, r, "status", "must be active or inactive")
		return
	}
	items, err := a.store.ListUnits(r.Context(), f)
	if err != nil {
		slog.Error("list units failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, unitList{Items: items})
}

func (a *API) getUnit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	unit, err := a.store.GetUnit(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		slog.Error("get unit failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, unit)
}

func (a *API) createUnit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || refuseScopedCaller(w, r, "create units") {
		return
	}
	var req createUnitRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.Unit{Code: req.Code, Name: strings.TrimSpace(req.Name), Class: req.Class}
	fields := validateShared(&in.Name, nil, nil, nil)
	if _, bad := fields["name"]; bad {
		fields["name"] = unitNameRule
	}
	if !store.ValidUnitCode(in.Code) {
		fields["code"] = "must be 1 to 10 characters of A-Z, a-z, 0-9, / or %"
	}
	if !slices.Contains(store.UnitClasses(), in.Class) {
		fields["class"] = "must be one of " + strings.Join(store.UnitClasses(), ", ")
	}
	if req.DecimalPrecision == nil || *req.DecimalPrecision < 0 || *req.DecimalPrecision > maxUnitPrecision {
		fields["decimalPrecision"] = unitPrecisionRule
	} else {
		in.DecimalPrecision = *req.DecimalPrecision
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	unit, err := a.store.CreateUnit(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("create unit failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.unitCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, unit)
}

func (a *API) patchUnit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || refuseScopedCaller(w, r, "change unit status") {
		return
	}
	var req referenceStatusRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !validStatus(req.Status) {
		badQuery(w, r, "status", "must be active or inactive")
		return
	}
	unit, err := a.store.SetUnitStatus(r.Context(), chi.URLParam(r, "code"), req.Status)
	if err != nil {
		slog.Error("set unit status failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.referenceChanged.Add(1)
	httpx.JSON(w, http.StatusOK, unit)
}
