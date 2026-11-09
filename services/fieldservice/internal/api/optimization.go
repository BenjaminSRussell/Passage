package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/optimizer"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

const (
	codeOptimizerUnavailable = "SVC-DSP-017"
	maxOptimizerSeconds      = 120
	maxLastError             = 500
)

// Optimizer is the dispatch service as fieldservice uses it.
type Optimizer interface {
	Configured() bool
	Submit(ctx context.Context, legalEntityID, key string, req optimizer.Request) (optimizer.Run, error)
	Get(ctx context.Context, legalEntityID, id string) (optimizer.Run, error)
}

// WithOptimizer sets the client of the dispatch service.
func (a *API) WithOptimizer(o Optimizer) *API {
	a.optimizer = o
	return a
}

func (a *API) registerOptimizations(r chi.Router) {
	const base = "/v1/service/dispatch/optimizations"
	r.With(authz.RequireDuty(dutyDispatch)).Post(base, a.requestOptimization)
	r.With(authz.RequireDuty(dutyInquire)).Get(base, a.listOptimizations)
	r.With(authz.RequireDuty(dutyInquire)).Get(base+"/{id}", a.getOptimization)
	r.With(authz.RequireDuty(dutyDispatch)).Post(base+"/{id}:sync", a.syncOptimization)
	r.With(authz.RequireDuty(dutyDispatch)).Post(base+"/{id}:retry", a.retryOptimization)
	r.With(authz.RequireDuty(dutyDispatch)).Post(base+"/{id}:accept", a.acceptOptimization)
}

func (a *API) optimizerReady(w http.ResponseWriter, r *http.Request) bool {
	if a.optimizer == nil || !a.optimizer.Configured() {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, codeOptimizerUnavailable, "the dispatch service is not configured"))
		return false
	}
	return true
}

// advance takes the one step the optimization is due for: submit a PENDING one whose
// back-off has passed, or poll a RUNNING one. A failed call is recorded (back-off, and
// parking after five); only a database error comes back as an error.
func (a *API) advance(ctx context.Context, o store.Optimization) (store.Optimization, error) {
	now := a.now()
	switch {
	case o.Status == store.OptPending && !now.Before(o.NextAttemptAt):
		req, err := a.store.OptimizationRequest(ctx, o.ID)
		if err != nil {
			return o, err
		}
		run, err := a.optimizer.Submit(ctx, o.LegalEntityID, fmt.Sprintf("%s:%d", o.ID, o.AttemptCount), req)
		if err != nil {
			return a.failed(ctx, o, err, now)
		}
		return a.store.RecordOptimizationSubmitted(ctx, o.ID, o.AttemptCount, run.ID, now)
	case o.Status == store.OptRunning && o.RemoteRunID != nil:
		run, err := a.optimizer.Get(ctx, o.LegalEntityID, *o.RemoteRunID)
		switch {
		case err != nil:
			return a.failed(ctx, o, err, now)
		case run.Status == optimizer.Succeeded && run.Result != nil:
			return a.store.RecordOptimizationResult(ctx, o.ID, *o.RemoteRunID, *run.Result, now)
		case run.Status == optimizer.Failed:
			reason := "the dispatch service failed the run"
			if run.Error != nil {
				reason = *run.Error
			}
			return a.failed(ctx, o, errors.New(reason), now)
		}
	}
	return o, nil
}

func (a *API) failed(ctx context.Context, o store.Optimization, cause error, now time.Time) (store.Optimization, error) {
	var refusal *optimizer.Error
	permanent := errors.As(cause, &refusal) && refusal.Permanent()
	reason := []rune(cause.Error())
	if len(reason) > maxLastError {
		reason = reason[:maxLastError]
	}
	slog.WarnContext(ctx, "dispatch optimization call failed", "optimization", o.ID, "attempt", o.AttemptCount+1, "permanent", permanent, "err", cause)
	next, err := a.store.RecordOptimizationFailure(ctx, o.ID, string(reason), permanent, now)
	if err == nil && next.Status == store.OptParked && o.Status != store.OptParked {
		a.optParked.Add(1)
	}
	return next, err
}

type optimizeRequest struct {
	LegalEntityID    string `json:"legalEntityId"`
	TerritoryID      string `json:"territoryId"`
	Date             string `json:"date"`
	TimeZone         string `json:"timeZone"`
	TimeLimitSeconds int    `json:"timeLimitSeconds"`
}

// requestOptimization snapshots the territory-day and starts a run at the dispatch
// service (BR-45.3). The answer is the optimization as it stands after the first call;
// :sync polls it and :accept writes the plan.
func (a *API) requestOptimization(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req optimizeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	uuidField(fields, "legalEntityId", req.LegalEntityID)
	uuidField(fields, "territoryId", req.TerritoryID)
	if _, ok := parseDate(req.Date); !ok {
		fields["date"] = "must be a date as YYYY-MM-DD"
	}
	zone := zoneOf(fields, "timeZone", req.TimeZone)
	if req.TimeLimitSeconds == 0 {
		req.TimeLimitSeconds = store.DefaultOptimizerSeconds
	}
	if req.TimeLimitSeconds < 1 || req.TimeLimitSeconds > maxOptimizerSeconds {
		fields["timeLimitSeconds"] = fmt.Sprintf("must be 1 to %d", maxOptimizerSeconds)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if !a.optimizerReady(w, r) {
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	o, replayed, err := a.store.RequestOptimization(r.Context(), store.OptimizeInput{LegalEntityID: req.LegalEntityID, TerritoryID: req.TerritoryID,
		Date: req.Date, TimeZone: zone, TimeLimit: req.TimeLimitSeconds}, a.now(), key, hashOf(req))
	if err == nil && !replayed {
		a.optRequested.Add(1)
		o, err = a.advance(r.Context(), o)
	}
	if err != nil {
		a.fail(w, r, "request dispatch optimization", err)
		return
	}
	a.respond(w, replayed, http.StatusCreated, o)
}

func (a *API) listOptimizations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f, ok := listFilter(w, r, map[string][]string{"status": {store.OptPending, store.OptRunning, store.OptSucceeded, store.OptAccepted, store.OptParked}}, "territoryId")
	if !ok {
		return
	}
	page, err := a.store.ListOptimizations(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list dispatch optimizations", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func optimizationID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !validTenant(w, r) {
		return "", false
	}
	return pathID(w, r, "id", store.CodeOptimizationUnknown, "dispatch optimization not found")
}

func (a *API) getOptimization(w http.ResponseWriter, r *http.Request) {
	id, ok := optimizationID(w, r)
	if !ok {
		return
	}
	o, err := a.store.GetOptimization(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get dispatch optimization", err)
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}

// syncOptimization takes the next step of an optimization: submit it when its back-off
// has passed, or poll the run at the dispatch service.
func (a *API) syncOptimization(w http.ResponseWriter, r *http.Request) {
	id, ok := optimizationID(w, r)
	if !ok || !a.optimizerReady(w, r) {
		return
	}
	o, err := a.store.GetOptimization(r.Context(), id)
	if err == nil {
		o, err = a.advance(r.Context(), o)
	}
	if err != nil {
		a.fail(w, r, "sync dispatch optimization", err)
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}

// retryOptimization puts a PARKED optimization back at attempt zero and tries it now.
func (a *API) retryOptimization(w http.ResponseWriter, r *http.Request) {
	id, ok := optimizationID(w, r)
	if !ok || !a.optimizerReady(w, r) {
		return
	}
	o, err := a.store.RetryOptimization(r.Context(), id, a.now())
	if err == nil {
		o, err = a.advance(r.Context(), o)
	}
	if err != nil {
		a.fail(w, r, "retry dispatch optimization", err)
		return
	}
	httpx.JSON(w, http.StatusOK, o)
}

// acceptOptimization writes the plan of a SUCCEEDED optimization as assignments.
func (a *API) acceptOptimization(w http.ResponseWriter, r *http.Request) {
	id, ok := optimizationID(w, r)
	if !ok {
		return
	}
	o, replayed, err := a.store.AcceptOptimization(r.Context(), id, a.now())
	if err != nil {
		a.fail(w, r, "accept dispatch optimization", err)
		return
	}
	if !replayed {
		a.optAccepted.Add(1)
	}
	a.respond(w, replayed, http.StatusOK, o)
}
