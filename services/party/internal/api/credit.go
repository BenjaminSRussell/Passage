package api

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const (
	dutyApprove     = "party.approve"
	dutyPolicyWrite = "party.approval-policy.write"
	dutyInboxRead   = "party.inbox.read"

	defaultChangePage = 50
	maxChangePage     = 200
)

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
	r.With(authz.RequireAnyDuty(dutyRead, dutyInboxRead)).Get("/v1/party/pending-changes", a.listPendingChanges)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/pending-changes/{id}", a.getPendingChange)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/legal-entities/{id}/protected-fields", a.getProtectedFields)
	r.With(authz.RequireDuty(dutyPolicyWrite)).Put("/v1/party/legal-entities/{id}/protected-fields", a.setProtectedFields)
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
	var req struct {
		Fields *[]string `json:"fields"`
	}
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Fields == nil {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeFieldsInvalid, "protected fields failed validation")
		p.Errors = map[string]string{"fields": "is required; send [] to unprotect every field"}
		httpx.Fail(w, r, p)
		return
	}
	fields, err := a.credit.SetProtectedFields(r.Context(), chi.URLParam(r, "id"), *req.Fields)
	if err != nil {
		failParty(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, protectedFields{Fields: fields})
}

type pendingChangePage struct {
	Items      []domain.PendingChange `json:"items"`
	NextCursor string                 `json:"nextCursor,omitempty"`
}

func (a *API) listPendingChanges(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit := defaultChangePage
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxChangePage {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "limit must be a number from 1 to "+strconv.Itoa(maxChangePage)))
			return
		}
		limit = n
	}
	afterAt, afterID, ok := decodeChangeCursor(r.URL.Query().Get("cursor"))
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "cursor must be the nextCursor of the previous page"))
		return
	}
	changes, err := a.credit.ListPendingChanges(r.Context(), limit+1, afterAt, afterID)
	if err != nil {
		failParty(w, r, err)
		return
	}
	page := pendingChangePage{Items: changes}
	if len(changes) > limit {
		page.Items = changes[:limit]
		last := page.Items[limit-1]
		page.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(last.RequestedAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID))
	}
	httpx.JSON(w, http.StatusOK, page)
}

func decodeChangeCursor(cursor string) (time.Time, string, bool) {
	if cursor == "" {
		return time.Time{}, "", true
	}
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	at, id, found := strings.Cut(string(raw), "|")
	when, perr := time.Parse(time.RFC3339Nano, at)
	return when, id, err == nil && found && perr == nil && ids.Valid(id)
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
