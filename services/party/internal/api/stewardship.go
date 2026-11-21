package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tracectx"
	"github.com/BenjaminSRussell/depot/services/party/internal/domain"
	"github.com/BenjaminSRussell/depot/services/party/internal/store"
)

const overrideReasonHeader = "X-Duplicate-Override-Reason"

func (a *API) WithStewardship(steward interface {
	store.StewardStore
	store.ElectronicStore
}) *API {
	a.steward, a.electronic = steward, steward
	return a
}

func (a *API) registerStewardship(r chi.Router) {
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/steward-tasks", a.listStewardTasks)
	r.With(authz.RequireDuty(dutyWrite)).Post("/v1/party/steward-tasks/{id}:resolve", a.resolveStewardTask)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/parties/{id}/electronic-addresses", a.listElectronicAddresses)
	r.With(authz.RequireDuty(dutyWrite)).Post("/v1/party/parties/{id}/electronic-addresses", a.addElectronicAddress)
}

func overrideReason(r *http.Request) (string, error) {
	values := r.Header.Values(overrideReasonHeader)
	if len(values) == 0 {
		return "", nil
	}
	reason := strings.TrimSpace(values[0])
	if n := len([]rune(reason)); n < 1 || n > store.MaxReasonLen {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeStewardInvalid, "the duplicate override reason failed validation")
		p.Errors = map[string]string{overrideReasonHeader: "must be 1 to 500 characters when sent"}
		return "", p
	}
	return reason, nil
}

func failDuplicate(w http.ResponseWriter, r *http.Request, dup *store.DuplicateError) {
	dup.Instance = r.URL.Path
	dup.TraceID = tracectx.TraceID(r.Context())
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(dup.Status)
	_ = json.NewEncoder(w).Encode(dup)
}

func (a *API) listStewardTasks(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "":
		status = store.StewardOpen
	case store.StewardOpen, store.StewardResolved:
	default:
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeStewardInvalid, "steward task query failed validation")
		p.Errors = map[string]string{"status": "must be open or resolved"}
		httpx.Fail(w, r, p)
		return
	}
	tasks, err := a.steward.ListStewardTasks(r.Context(), status)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"stewardTasks": tasks})
}

func (a *API) resolveStewardTask(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req store.ResolveStewardTask
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req = req.Clean()
	if errs := req.Validate(); len(errs) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeStewardInvalid, "steward resolution failed validation")
		p.Errors = errs
		httpx.Fail(w, r, p)
		return
	}
	task, err := a.steward.ResolveStewardTask(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		failParty(w, r, err)
		return
	}
	a.stewardResolved.Add(1)
	httpx.JSON(w, http.StatusOK, task)
}

func (a *API) addElectronicAddress(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.NewElectronicAddress
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req = req.Clean()
	if errs := req.Validate(); len(errs) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeElectronicInvalid, "electronic address failed validation")
		p.Errors = errs
		httpx.Fail(w, r, p)
		return
	}
	address, err := a.electronic.AddElectronicAddress(r.Context(), chi.URLParam(r, "id"), req)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, address)
}

func (a *API) listElectronicAddresses(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	addresses, err := a.electronic.ListElectronicAddresses(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"electronicAddresses": addresses})
}

func asDuplicate(err error) (*store.DuplicateError, bool) {
	var dup *store.DuplicateError
	return dup, errors.As(err, &dup)
}
