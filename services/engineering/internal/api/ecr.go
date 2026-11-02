package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/engineering/internal/store"
)

type ecrBody struct {
	ID             string   `json:"id,omitempty"`
	Number         string   `json:"number"`
	Title          string   `json:"title"`
	Reason         string   `json:"reason,omitempty"`
	Priority       string   `json:"priority"`
	State          string   `json:"state,omitempty"`
	Products       []string `json:"products"`
	RequestedBy    string   `json:"requestedBy,omitempty"`
	DecidedBy      string   `json:"decidedBy,omitempty"`
	DecisionReason string   `json:"decisionReason,omitempty"`
	EcoID          string   `json:"ecoId,omitempty"`
}

func bodyFromEcr(e domain.Ecr) ecrBody {
	return ecrBody{ID: e.ID, Number: e.Number, Title: e.Title, Reason: e.Reason, Priority: e.Priority, State: string(e.State), Products: append([]string{}, e.Products...),
		RequestedBy: e.RequestedBy, DecidedBy: e.DecidedBy, DecisionReason: e.DecisionReason, EcoID: e.EcoID}
}

func (a *API) createEcr(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var body ecrBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	requester, _ := tenant.FromContext(r.Context())
	if requester.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "raising an ECR needs the acting user"))
		return
	}
	ecr := domain.Ecr{ID: ids.New(), Number: body.Number, Title: body.Title, Reason: body.Reason, Priority: body.Priority, State: domain.EcrDraft,
		Products: append([]string{}, body.Products...), RequestedBy: requester.UserID}
	if fields := ecr.Validate(); len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	created, err := a.store.CreateEcr(r.Context(), ecr, store.Idem{Key: r.Header.Get(idempotencyKey), Hash: hashOf(body)})
	if err != nil {
		a.fail(w, r, "create ecr", err, &a.ecrsFailed)
		return
	}
	a.ecrsCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, bodyFromEcr(created))
}

func (a *API) listEcrs(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	filter := store.EcrFilter{State: r.URL.Query().Get("state"), ProductID: r.URL.Query().Get("productId")}
	switch domain.EcrState(filter.State) {
	case "", domain.EcrDraft, domain.EcrSubmitted, domain.EcrUnderReview, domain.EcrApproved, domain.EcrRejected, domain.EcrCancelled:
	default:
		invalid(w, r, map[string]string{"state": "must be draft, submitted, under_review, approved, rejected or cancelled"})
		return
	}
	if len(filter.ProductID) > domain.MaxEcrProduct {
		invalid(w, r, map[string]string{"productId": fmt.Sprintf("must be at most %d characters", domain.MaxEcrProduct)})
		return
	}
	listPage(a, w, r, "list ecrs", &a.ecrsFailed, func(ctx context.Context, after string, limit int) (store.Page[domain.Ecr], error) {
		return a.store.ListEcrs(ctx, filter, after, limit)
	}, bodyFromEcr)
}

func (a *API) getEcr(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	e, err := a.store.GetEcr(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get ecr", err, &a.ecrsFailed)
		return
	}
	httpx.JSON(w, http.StatusOK, bodyFromEcr(e))
}

// moveEcr serves the workflow actions. Repeating an action that already happened returns the request
// unchanged; any other move the state machine refuses is a conflict.
func (a *API) moveEcr(action string) http.HandlerFunc {
	target := map[string]domain.EcrState{"submit": domain.EcrSubmitted, "review": domain.EcrUnderReview, "approve": domain.EcrApproved, "reject": domain.EcrRejected, "cancel": domain.EcrCancelled}[action]
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var decision struct {
			Reason string `json:"reason"`
		}
		if r.ContentLength != 0 {
			if err := httpx.Decode(r, &decision); err != nil {
				httpx.Fail(w, r, err)
				return
			}
		}
		reason := strings.TrimSpace(decision.Reason)
		if len([]rune(reason)) > maxReasonLength {
			invalid(w, r, map[string]string{"reason": fmt.Sprintf("must be at most %d characters", maxReasonLength)})
			return
		}
		ecr, err := a.store.GetEcr(r.Context(), id)
		if err != nil {
			a.fail(w, r, action+" ecr", err, &a.ecrsFailed)
			return
		}
		if ecr.State == target {
			httpx.JSON(w, http.StatusOK, bodyFromEcr(ecr))
			return
		}
		previous := ecr.State
		p, _ := tenant.FromContext(r.Context())
		switch action {
		case "submit":
			err = ecr.Submit()
		case "cancel":
			err = ecr.Cancel(p.UserID, reason)
		case "review":
			err = ecr.StartReview(p.UserID)
		default:
			err = ecr.Decide(target, p.UserID, reason)
		}
		if err == nil {
			err = a.store.SaveEcr(r.Context(), ecr, previous)
		}
		if err != nil {
			a.fail(w, r, action+" ecr", err, &a.ecrsFailed)
			return
		}
		a.ecrsDecided.Add(1)
		slog.InfoContext(r.Context(), "ecr moved", "ecrId", ecr.ID, "state", string(ecr.State), "actor", p.UserID, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusOK, bodyFromEcr(ecr))
	}
}
