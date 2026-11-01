package api

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// registerAbsence mounts the absence code routes (FR-25.2).
func (a *API) registerAbsence(r chi.Router) {
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/absence-codes", a.createAbsenceCode)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/absence-codes", a.listAbsenceCodes)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/absence-codes/{id}", a.getAbsenceCode)
	r.With(authz.RequireDuty(DutySetup)).Put("/v1/shopfloor/absence-codes/{id}", a.updateAbsenceCode)
}

type absenceCodeRequest struct {
	Code          string           `json:"code"`
	Name          string           `json:"name"`
	Paid          *bool            `json:"paid"`
	PayMultiplier *decimal.Decimal `json:"payMultiplier"`
	Active        *bool            `json:"active"`
}

func (req absenceCodeRequest) input(creating bool) (store.AbsenceCodeInput, map[string]string) {
	fields := map[string]string{}
	code, name := strings.TrimSpace(req.Code), strings.TrimSpace(req.Name)
	if creating && (len(code) < 1 || len(code) > maxCodeLen) {
		fields["code"] = "must be 1 to 40 characters"
	}
	if len(name) < 1 || len(name) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if req.Paid == nil {
		fields["paid"] = "is required"
	}
	paid := req.Paid != nil && *req.Paid
	mult, msg := domain.AbsenceMultiplier(paid, req.PayMultiplier)
	if msg != "" {
		fields["payMultiplier"] = msg
	}
	return store.AbsenceCodeInput{Code: code, Name: name, Paid: paid, PayMultiplier: mult, Active: req.Active == nil || *req.Active}, fields
}

func (a *API) createAbsenceCode(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining an absence code")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req absenceCodeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.input(true)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.CreateAbsenceCode(r.Context(), in, user, idem)
	if err != nil {
		a.fail(w, r, "create absence code", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) updateAbsenceCode(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining an absence code")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeAbsenceNotFound, "absence code")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req absenceCodeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.input(false)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.UpdateAbsenceCode(r.Context(), id, version, in, user)
	if err != nil {
		a.fail(w, r, "update absence code", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getAbsenceCode(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeAbsenceNotFound, "absence code")
	if !ok {
		return
	}
	out, err := a.store.GetAbsenceCode(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get absence code", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listAbsenceCodes(w http.ResponseWriter, r *http.Request) {
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
	items, err := a.store.ListAbsenceCodes(r.Context(), active, limit)
	if err != nil {
		a.fail(w, r, "list absence codes", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.AbsenceCode]{Items: items})
}
