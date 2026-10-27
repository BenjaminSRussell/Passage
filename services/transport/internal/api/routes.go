package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/transport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/transport/internal/optimizer"
	"github.com/BenjaminSRussell/passage/services/transport/internal/store"
)

// Optimizer solves routes and stacks load plans in services/routing-py.
// optimizer.Client implements it.
type Optimizer interface {
	Solve(ctx context.Context, req domain.RouteRequest) (domain.RouteSolution, error)
	Stack(ctx context.Context, req domain.LoadPlanRequest) (domain.LoadPlan, error)
}

// solveOverhead is the time allowed on top of the solver budget for the call.
const solveOverhead = 4 * time.Second

// stackTimeout bounds a load-plan call (§6.34.19: < 5 s for 60 pallets).
const stackTimeout = 10 * time.Second

// warning is a non-blocking finding in a response, such as dropped stops.
type warning struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Detail   string `json:"detail"`
}

// routePlanResponse is a stored plan with its warnings and whether this call
// solved it or returned the plan stored for the same request hash.
type routePlanResponse struct {
	domain.RoutePlan
	Replayed bool      `json:"replayed"`
	Warnings []warning `json:"warnings"`
}

// WithOptimizer plugs in the optimizer. Without one, optimize and load-plan calls
// return 503 TMS-0123.
func (a *API) WithOptimizer(o Optimizer) *API {
	a.optimizer = o
	return a
}

// optimizeRoutes solves a VRP request (FR-34.2, BR-34.1) and stores the plan. The
// request hash is the idempotency key (§6.34.5): the same request returns the
// stored plan with 200 instead of solving again. A solver failure or a reply that
// loses a stop is 503 TMS-0123, never an empty plan; dropped stops are TMS-0104.
func (a *API) optimizeRoutes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.RouteRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Normalize()
	if fields := req.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	hash, err := req.Hash()
	if err != nil {
		a.fail(w, r, "hash route request failed", err)
		return
	}
	stored, found, err := a.store.FindRoutePlan(r.Context(), hash)
	if err != nil {
		a.fail(w, r, "find route plan failed", err)
		return
	}
	if found {
		httpx.JSON(w, http.StatusOK, planResponse(stored, true))
		return
	}
	if a.optimizer == nil {
		httpx.Fail(w, r, optimizerUnavailable("the route optimizer is not configured (TMS_OPTIMIZER_URL)"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.TimeLimitS)*time.Second+solveOverhead)
	defer cancel()
	sol, err := a.optimizer.Solve(ctx, req)
	if err != nil {
		a.optimizerFailed(w, r, "route optimizer call failed", err)
		return
	}
	if msg := domain.CheckSolution(req, sol); msg != "" {
		a.optimizerFailed(w, r, "route optimizer reply is not a plan", errors.New(msg))
		return
	}
	plan, created, err := a.store.SaveRoutePlan(r.Context(), domain.NewRoutePlan(req, hash, sol))
	if err != nil {
		a.fail(w, r, "save route plan failed", err)
		return
	}
	a.routesOptimized.Add(1)
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	httpx.JSON(w, status, planResponse(plan, !created))
}

func planResponse(plan domain.RoutePlan, replayed bool) routePlanResponse {
	resp := routePlanResponse{RoutePlan: plan, Replayed: replayed, Warnings: []warning{}}
	if n := len(plan.Solution.Dropped); n > 0 {
		resp.Warnings = append(resp.Warnings, warning{
			Code:     domain.CodeStopsDropped,
			Severity: "warning",
			Detail:   fmt.Sprintf("%d stops dropped as infeasible; see route report.", n),
		})
	}
	return resp
}

func (a *API) listRoutePlans(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	plans, err := a.store.ListRoutePlans(r.Context())
	if err != nil {
		a.fail(w, r, "list route plans failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": plans})
}

func (a *API) getRoutePlan(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !uuidRe.MatchString(id) {
		httpx.Fail(w, r, store.NotFound("route plan"))
		return
	}
	plan, err := a.store.GetRoutePlan(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get route plan failed", err)
		return
	}
	httpx.JSON(w, http.StatusOK, planResponse(plan, false))
}

// stackLoadPlan computes a layer-based load plan for a trailer (FR-34.18). The
// plan is returned, not stored. A reply that loses a pallet or stacks on a
// non-stackable one is refused with 503 TMS-0123.
func (a *API) stackLoadPlan(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.LoadPlanRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Normalize()
	if fields := req.Validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	if a.optimizer == nil {
		httpx.Fail(w, r, optimizerUnavailable("the load planner is not configured (TMS_OPTIMIZER_URL)"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), stackTimeout)
	defer cancel()
	plan, err := a.optimizer.Stack(ctx, req)
	if err != nil {
		a.optimizerFailed(w, r, "load planner call failed", err)
		return
	}
	if msg := domain.CheckLoadPlan(req, plan); msg != "" {
		a.optimizerFailed(w, r, "load planner reply is not a plan", errors.New(msg))
		return
	}
	a.loadPlans.Add(1)
	httpx.JSON(w, http.StatusOK, plan)
}

// optimizerFailed logs the cause and answers 503 TMS-0123 without its detail.
func (a *API) optimizerFailed(w http.ResponseWriter, r *http.Request, msg string, err error) {
	a.optimizerErrors.Add(1)
	if errors.Is(err, optimizer.ErrRejected) {
		msg += " (contract drift with routing-py)"
	}
	slog.Error(msg, "err", err, "correlationId", middleware.GetReqID(r.Context()))
	httpx.Fail(w, r, optimizerUnavailable("the optimizer is unavailable; try again shortly"))
}

func optimizerUnavailable(detail string) error {
	return httpx.NewProblem(http.StatusServiceUnavailable, domain.CodeOptimizerUnavailable, detail)
}
