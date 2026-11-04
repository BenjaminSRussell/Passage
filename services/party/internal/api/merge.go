package api

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/party/internal/domain"
	"github.com/BenjaminSRussell/passage/services/party/internal/store"
)

const (
	codeMergeConflict = "PTY-MERGE-CONFLICT"
	codeRoleConflict  = "PTY-MERGE-ROLE-CONFLICT"
)

// MergeStore holds merges for approval (#450): :merge only records a request,
// and the workflow-ts merge approval executes it through :approve.
type MergeStore interface {
	RequestMerge(ctx context.Context, in store.MergeRequestInput) (domain.MergeRequest, bool, error)
	GetMergeRequest(ctx context.Context, id string) (domain.MergeRequest, error)
	DecideMerge(ctx context.Context, id string, approve bool, reason string) (domain.MergeRequest, error)
}

type mergeRequest struct {
	MergedPartyID string                 `json:"mergedPartyId"`
	Rules         map[string]domain.Rule `json:"rules"`
}

type mergeMetrics struct {
	requested, approved, rejected atomic.Int64
}

func (a *API) WithMerge(merges MergeStore) *API {
	a.merges = merges
	return a
}

func (a *API) registerMerge(r chi.Router) {
	r.With(authz.RequireDuty(store.DutyMerge)).Post("/v1/party/parties/{id}:merge", a.requestMerge)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/party/merge-requests/{id}", a.getMergeRequest)
	r.With(authz.RequireDuty(store.DutyMergeExecute)).Post("/v1/party/merge-requests/{id}:approve", a.decideMerge(true))
	r.With(authz.RequireDuty(store.DutyMergeExecute)).Post("/v1/party/merge-requests/{id}:reject", a.decideMerge(false))
}

func (a *API) requestMerge(w http.ResponseWriter, r *http.Request) {
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
		httpx.Fail(w, r, invalidMerge("id", "must be a UUID"))
		return
	}
	mergedID, err := uuid.Parse(req.MergedPartyID)
	if err != nil {
		httpx.Fail(w, r, invalidMerge("mergedPartyId", "must be a UUID"))
		return
	}
	if mergedID == survivorID {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeMergeConflict, "a party cannot be merged into itself"))
		return
	}
	in := store.MergeRequestInput{SurvivorID: survivorID.String(), MergedID: mergedID.String(), Rules: req.Rules, Key: r.Header.Get(idempotencyKeyHeader)}
	in.Hash = domain.RequestHash(struct {
		Survivor string                 `json:"survivor"`
		Merged   string                 `json:"merged"`
		Rules    map[string]domain.Rule `json:"rules"`
	}{in.SurvivorID, in.MergedID, in.Rules})
	res, created, err := a.merges.RequestMerge(r.Context(), in)
	if err != nil {
		failMerge(w, r, err)
		return
	}
	if created {
		a.mergeCounts.requested.Add(1)
	}
	httpx.JSON(w, http.StatusAccepted, res)
}

func (a *API) getMergeRequest(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	res, err := a.merges.GetMergeRequest(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		failMerge(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, res)
}

func (a *API) decideMerge(approve bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		if p, _ := tenant.FromContext(r.Context()); !store.MergeExecutorPrincipal(p) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a merge request is decided only through the workflow-ts merge approval"))
			return
		}
		var req decisionRequest
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &req); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		res, err := a.merges.DecideMerge(r.Context(), chi.URLParam(r, "id"), approve, req.Reason)
		if err != nil {
			failMerge(w, r, err)
			return
		}
		if !res.Replayed {
			if res.Status == domain.MergeApproved {
				a.merged.Add(1)
				a.mergeCounts.approved.Add(1)
			} else {
				a.mergeCounts.rejected.Add(1)
			}
		}
		httpx.JSON(w, http.StatusOK, res)
	}
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
