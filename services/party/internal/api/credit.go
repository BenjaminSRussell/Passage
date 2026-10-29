package api

import (
	"net/http"
	"sync/atomic"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const dutyApprove = "party.approve"

type creditLimitRequest struct {
	CreditLimit string `json:"creditLimit"`
}

type decisionRequest struct {
	Reason string `json:"reason"`
}

type creditMetrics struct {
	requested, applied, approved, rejected atomic.Int64
}

func (a *API) WithCredit(credit store.CreditStore) *API {
	a.credit = credit
	return a
}

func (a *API) registerCredit(r chi.Router) {
	r.With(authz.RequireDuty(dutyWrite)).Post("/v1/party/customers/{id}/credit-limit", a.setCreditLimit)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/pending-changes/{id}", a.getPendingChange)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/legal-entities/{id}/protected-fields", a.getProtectedFields)
	r.With(authz.RequireDuty(dutyApprove)).Put("/v1/party/legal-entities/{id}/protected-fields", a.setProtectedFields)
	r.With(authz.RequireDuty(dutyApprove)).Post("/v1/party/pending-changes/{id}:approve", a.decide(true))
	r.With(authz.RequireDuty(dutyApprove)).Post("/v1/party/pending-changes/{id}:reject", a.decide(false))
}

type protectedFields struct {
	Fields []string `json:"fields"`
}

func (a *API) getProtectedFields(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	fields, err := a.credit.ProtectedFields(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, protectedFields{Fields: fields})
}

func (a *API) setProtectedFields(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req protectedFields
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields, err := a.credit.SetProtectedFields(r.Context(), chi.URLParam(r, "id"), req.Fields)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, protectedFields{Fields: fields})
}

func (a *API) getPendingChange(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	change, err := a.credit.GetPendingChange(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, change)
}

func (a *API) setCreditLimit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req creditLimitRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	res, err := a.credit.RequestCreditLimit(r.Context(), chi.URLParam(r, "id"), req.CreditLimit)
	if err != nil {
		failParty(w, r, err)
		return
	}
	if res.Pending == nil {
		a.creditCounts.applied.Add(1)
		httpx.JSON(w, http.StatusOK, res)
		return
	}
	a.creditCounts.requested.Add(1)
	httpx.JSON(w, http.StatusAccepted, res)
}

func (a *API) decide(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		var req decisionRequest
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &req); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		change, err := a.credit.DecidePendingChange(r.Context(), chi.URLParam(r, "id"), approve, req.Reason)
		if err != nil {
			failParty(w, r, err)
			return
		}
		if !change.Replayed {
			if change.Status == domain.ChangeApproved {
				a.creditCounts.approved.Add(1)
			} else {
				a.creditCounts.rejected.Add(1)
			}
		}
		httpx.JSON(w, http.StatusOK, change)
	}
}
