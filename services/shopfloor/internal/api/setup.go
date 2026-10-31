package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

const maxCodeLen = 40

func (a *API) registerSetup(r chi.Router) {
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/time-profiles", a.createProfile)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/time-profiles", a.listProfiles)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/time-profiles/{id}", a.getProfile)
	r.With(authz.RequireDuty(DutySetup)).Put("/v1/shopfloor/time-profiles/{id}", a.updateProfile)
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/pay-agreements", a.createAgreement)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/pay-agreements", a.listAgreements)
	r.With(authz.RequireDuty(DutyView)).Get("/v1/shopfloor/pay-agreements/{id}", a.getAgreement)
	r.With(authz.RequireDuty(DutySetup)).Put("/v1/shopfloor/pay-agreements/{id}", a.updateAgreement)
	r.With(authz.RequireDuty(DutySetup)).Post("/v1/shopfloor/workers/{id}:assign-setup", a.assignSetup)
}

func etag(version int) string { return `"` + strconv.Itoa(version) + `"` }

// ifMatch reads If-Match as the version the caller last read. The header is required.
func ifMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	n, err := strconv.Atoi(strings.Trim(raw, `"`))
	if raw == "" || err != nil || n < 1 || !strings.HasPrefix(raw, `"`) || !strings.HasSuffix(raw, `"`) {
		validationFailed(w, r, map[string]string{"If-Match": `header is required: the version you read as an entity tag, for example "3"`})
		return 0, false
	}
	return n, true
}

func pathID(w http.ResponseWriter, r *http.Request, code, what string) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, code, what+" not found"))
		return "", false
	}
	return id, true
}

func activeFilter(w http.ResponseWriter, r *http.Request) (bool, bool) {
	switch r.URL.Query().Get("active") {
	case "", "false":
		return false, true
	case "true":
		return true, true
	}
	validationFailed(w, r, map[string]string{"active": "must be true or false"})
	return false, false
}

type profileRequest struct {
	Code   string                   `json:"code"`
	Name   string                   `json:"name"`
	Active *bool                    `json:"active"`
	Days   []domain.ProfileDayInput `json:"days"`
}

func (req profileRequest) input(creating bool) (store.TimeProfileInput, map[string]string) {
	_, fields := domain.ProfileFromDays(req.Days)
	code, name := strings.TrimSpace(req.Code), strings.TrimSpace(req.Name)
	if creating && (len(code) < 1 || len(code) > maxCodeLen) {
		fields["code"] = "must be 1 to 40 characters"
	}
	if len(name) < 1 || len(name) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	active := req.Active == nil || *req.Active
	return store.TimeProfileInput{Code: code, Name: name, Active: active, Days: req.Days}, fields
}

func (a *API) createProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a time profile")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req profileRequest
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
	out, err := a.store.CreateTimeProfile(r.Context(), in, user, idem)
	if err != nil {
		a.fail(w, r, "create time profile", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) updateProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a time profile")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeProfileNotFound, "time profile")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req profileRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.input(false)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.UpdateTimeProfile(r.Context(), id, version, in, user)
	if err != nil {
		a.fail(w, r, "update time profile", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeProfileNotFound, "time profile")
	if !ok {
		return
	}
	out, err := a.store.GetTimeProfile(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get time profile", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listProfiles(w http.ResponseWriter, r *http.Request) {
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
	items, err := a.store.ListTimeProfiles(r.Context(), active, limit)
	if err != nil {
		a.fail(w, r, "list time profiles", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.TimeProfile]{Items: items})
}

type agreementRequest struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Active *bool  `json:"active"`
	domain.AgreementInput
}

func (req agreementRequest) input(creating bool) (store.PayAgreementInput, map[string]string) {
	rules, fields := req.AgreementInput.Rules()
	code, name := strings.TrimSpace(req.Code), strings.TrimSpace(req.Name)
	if creating && (len(code) < 1 || len(code) > maxCodeLen) {
		fields["code"] = "must be 1 to 40 characters"
	}
	if len(name) < 1 || len(name) > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	return store.PayAgreementInput{Code: code, Name: name, Active: req.Active == nil || *req.Active, Rules: rules}, fields
}

func (a *API) createAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a pay agreement")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	var req agreementRequest
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
	out, err := a.store.CreatePayAgreement(r.Context(), in, user, idem)
	if err != nil {
		a.fail(w, r, "create pay agreement", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) updateAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "maintaining a pay agreement")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeAgreementNotFound, "pay agreement")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req agreementRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in, fields := req.input(false)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, err := a.store.UpdatePayAgreement(r.Context(), id, version, in, user)
	if err != nil {
		a.fail(w, r, "update pay agreement", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) getAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeAgreementNotFound, "pay agreement")
	if !ok {
		return
	}
	out, err := a.store.GetPayAgreement(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get pay agreement", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) listAgreements(w http.ResponseWriter, r *http.Request) {
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
	items, err := a.store.ListPayAgreements(r.Context(), active, limit)
	if err != nil {
		a.fail(w, r, "list pay agreements", err)
		return
	}
	httpx.JSON(w, http.StatusOK, itemsResponse[store.PayAgreement]{Items: items})
}

type assignRequest struct {
	ProfileID      *string `json:"profileId"`
	PayAgreementID *string `json:"payAgreementId"`
}

func (a *API) assignSetup(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	user, ok := authenticatedUser(w, r, "assigning a pay setup")
	if !ok {
		return
	}
	id, ok := pathID(w, r, store.CodeWorkerNotFound, "worker")
	if !ok {
		return
	}
	idem, ok := readIdem(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req assignRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.ProfileID != nil && !ids.Valid(*req.ProfileID) {
		fields["profileId"] = "must be a UUID or null"
	}
	if req.PayAgreementID != nil && !ids.Valid(*req.PayAgreementID) {
		fields["payAgreementId"] = "must be a UUID or null"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.replayed(w, r, idem) {
		return
	}
	out, err := a.store.AssignWorkerSetup(r.Context(), id, version, req.ProfileID, req.PayAgreementID, user, idem)
	if err != nil {
		a.fail(w, r, "assign worker setup", err)
		return
	}
	w.Header().Set("ETag", etag(out.Version))
	httpx.JSON(w, http.StatusOK, out)
}
