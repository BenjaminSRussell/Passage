package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const (
	codeMergeConflict = "PTY-MERGE-CONFLICT"
	codeRoleConflict  = "PTY-MERGE-ROLE-CONFLICT"
)

type MergeStore interface {
	Merge(ctx context.Context, req store.MergeRequest) (store.MergeResult, error)
}

type mergeRequest struct {
	MergedPartyID string                 `json:"mergedPartyId"`
	Rules         map[string]domain.Rule `json:"rules"`
}

type mergeResponse struct {
	MergeID             string            `json:"mergeId"`
	SurvivorPartyID     string            `json:"survivorPartyId"`
	MergedPartyID       string            `json:"mergedPartyId"`
	RolesMoved          int               `json:"rolesMoved"`
	ReferencesRewritten int               `json:"referencesRewritten"`
	Resolved            map[string]string `json:"resolved"`
}

func (a *API) WithMerge(merges MergeStore) *API {
	a.merges = merges
	return a
}

func (a *API) registerMerge(r chi.Router) {
	r.With(authz.RequireDuty(store.DutyMerge)).Post("/v1/party/parties/{id}:merge", a.mergeParties)
}

func (a *API) mergeParties(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req mergeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	survivorID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		failParty(w, r, store.ErrInvalidID)
		return
	}
	mergedID, err := uuid.Parse(req.MergedPartyID)
	if err != nil {
		httpx.Fail(w, r, invalidMerge("mergedPartyId", "must be a UUID"))
		return
	}
	if mergedID == survivorID {
		httpx.Fail(w, r, invalidMerge("mergedPartyId", "must differ from the surviving party"))
		return
	}
	res, err := a.merges.Merge(r.Context(), store.MergeRequest{WinnerID: survivorID.String(), LoserID: mergedID.String(), Rules: req.Rules})
	if err != nil {
		failMerge(w, r, err)
		return
	}
	a.merged.Add(1)
	httpx.JSON(w, http.StatusOK, mergeResponse{
		MergeID: res.MergeID, SurvivorPartyID: survivorID.String(), MergedPartyID: mergedID.String(),
		RolesMoved: res.RolesMoved, ReferencesRewritten: res.ReferencesRewritten, Resolved: res.Resolved,
	})
}

func invalidMerge(field, reason string) *httpx.Problem {
	p := httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeInvalid, "merge request failed validation")
	p.Errors = map[string]string{field: reason}
	return p
}

func failMerge(w http.ResponseWriter, r *http.Request, err error) {
	var rule *domain.RuleError
	switch {
	case errors.As(err, &rule):
		httpx.Fail(w, r, invalidMerge("rules."+rule.Field, rule.Reason))
	case errors.Is(err, store.ErrNotMergeable):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeMergeConflict, err.Error()))
	case errors.Is(err, store.ErrRoleConflict):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeRoleConflict, err.Error()))
	case errors.Is(err, store.ErrScopedPrincipal), errors.Is(err, dbx.ErrNotTenantWide):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", store.ErrScopedPrincipal.Error()))
	default:
		failParty(w, r, err)
	}
}
