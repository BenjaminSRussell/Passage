package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/failover"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
)

// requireFailover admits a caller who holds ops.failover.execute, or a named operator inside an active, logged
// break-glass window. A window is checked and its use recorded in one statement, so no use goes unlogged.
func (a *API) requireFailover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := tenant.FromContext(r.Context())
		if !ok {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
			return
		}
		if p.HasDuty(DutyFailover) {
			next.ServeHTTP(w, r)
			return
		}
		allowed, err := a.store.AuthoriseBreakGlass(r.Context(), p.UserID, r.Method, r.URL.Path)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if !allowed {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty "+DutyFailover+" and no active break-glass window"))
			return
		}
		slog.Warn("break-glass used", "user", p.UserID, "method", r.Method, "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
		a.breakGlassUses.Add(1)
		next.ServeHTTP(w, r)
	})
}

func (a *API) writeGauges(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	g, err := a.store.Gauges(ctx)
	if err != nil {
		slog.Error("ops gauges", "err", err)
		return
	}
	_, _ = fmt.Fprintf(w, "# HELP passage_ops_region_fenced Regions currently FENCED.\n# TYPE passage_ops_region_fenced gauge\npassage_ops_region_fenced %d\n", g.FencedRegions)
	_, _ = fmt.Fprintf(w, "# HELP passage_ops_failover_in_progress Failovers with steps not yet DONE.\n# TYPE passage_ops_failover_in_progress gauge\npassage_ops_failover_in_progress %d\n", g.FailoversInProgress)
	_, _ = fmt.Fprintf(w, "# HELP ops_break_glass_active Break-glass windows open now.\n# TYPE ops_break_glass_active gauge\nops_break_glass_active %d\n", g.BreakGlassActive)
	_, _ = fmt.Fprintf(w, "# HELP ops_break_glass_review_overdue Break-glass windows past their review date without a review.\n# TYPE ops_break_glass_review_overdue gauge\nops_break_glass_review_overdue %d\n", g.BreakGlassOverdue)
	a.writeLicenseMetrics(w, r)
	_, _ = fmt.Fprintf(w, "# HELP ops_break_glass_opened_total Break-glass windows opened.\n# TYPE ops_break_glass_opened_total counter\nops_break_glass_opened_total %d\n", a.breakGlassOpened.Load())
	_, _ = fmt.Fprintf(w, "# HELP ops_break_glass_uses_total Requests authorised by a break-glass window.\n# TYPE ops_break_glass_uses_total counter\nops_break_glass_uses_total %d\n", a.breakGlassUses.Load())
}

func (a *API) failoverDetail(w http.ResponseWriter, r *http.Request) {
	d, err := a.store.FailoverByEvent(r.Context(), chi.URLParam(r, "eventId"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, d)
}

type stepRequest struct {
	Order      int       `json:"order"`
	Region     string    `json:"region"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail"`
	Forced     bool      `json:"forced"`
	StartedAt  time.Time `json:"startedAt"`
	FinishedAt time.Time `json:"finishedAt"`
}

var knownActions = map[string]bool{
	failover.ActionFence: true, failover.ActionPromote: true, failover.ActionMirror: true,
	failover.ActionTemporal: true, failover.ActionDNS: true, failover.ActionChain: true,
}

// recordStep stores a step result the runner reports. The reporter is the verified caller.
func (a *API) recordStep(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	action := chi.URLParam(r, "action")
	var req stepRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	switch {
	case p.UserID == "":
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "recording a step needs a named user"))
	case !knownActions[action]:
		httpx.Fail(w, r, validation("action", "must be one of the plan's step actions"))
	case req.Order <= 0 || req.Order > 100:
		httpx.Fail(w, r, validation("order", "must be the step's position in the plan"))
	case req.Region == "" || len(req.Region) > 128:
		httpx.Fail(w, r, validation("region", "is required"))
	case req.Status != failover.StatusDone && req.Status != failover.StatusFailed:
		httpx.Fail(w, r, validation("status", "must be DONE or FAILED"))
	case req.StartedAt.IsZero() || req.FinishedAt.IsZero() || req.FinishedAt.Before(req.StartedAt):
		httpx.Fail(w, r, validation("finishedAt", "startedAt and finishedAt are required and finishedAt must not be earlier"))
	case req.Forced && (action != failover.ActionFence || req.Status != failover.StatusDone || req.Detail == ""):
		httpx.Fail(w, r, validation("forced", "applies only to a DONE fence-old-primary step and needs a detail with the operator's reason"))
	case len(req.Detail) > 2000:
		httpx.Fail(w, r, validation("detail", "is limited to 2000 characters"))
	default:
		err := a.store.RecordStep(r.Context(), chi.URLParam(r, "eventId"), store.Step{
			Order: req.Order, Action: action, Region: req.Region, Status: req.Status, Detail: req.Detail, Forced: req.Forced,
			StartedAt: req.StartedAt, FinishedAt: req.FinishedAt,
		}, p.UserID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		if req.Forced {
			slog.Warn("fence recorded without reaching the old region", "event", chi.URLParam(r, "eventId"), "region", req.Region, "by", p.UserID, "reason", req.Detail, "correlationId", middleware.GetReqID(r.Context()))
		}
		d, err := a.store.FailoverByEvent(r.Context(), chi.URLParam(r, "eventId"))
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		httpx.JSON(w, http.StatusOK, d)
	}
}

type breakGlassRequest struct {
	ReasonCode string `json:"reasonCode"`
	TicketRef  string `json:"ticketRef"`
	Minutes    int    `json:"minutes"`
}

func (a *API) openBreakGlass(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	var req breakGlassRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	win, err := domain.PlanBreakGlass(domain.BreakGlassRequest{UserID: p.UserID, ReasonCode: req.ReasonCode, TicketRef: req.TicketRef, Minutes: req.Minutes, Start: time.Now().UTC()})
	if err != nil {
		httpx.Fail(w, r, store.Problem(err))
		return
	}
	g, err := a.store.OpenBreakGlass(r.Context(), win)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.breakGlassOpened.Add(1)
	slog.Warn("break-glass opened", "id", g.ID, "user", g.UserID, "reason", g.ReasonCode, "ticket", g.TicketRef, "expiresAt", g.ExpiresAt, "correlationId", middleware.GetReqID(r.Context()))
	httpx.JSON(w, http.StatusCreated, g)
}

func (a *API) listBreakGlass(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.ListBreakGlass(r.Context(), r.URL.Query().Get("unreviewed") == "true")
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func (a *API) revokeBreakGlass(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	g, err := a.store.RevokeBreakGlass(r.Context(), chi.URLParam(r, "id"), p.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	slog.Warn("break-glass revoked", "id", g.ID, "user", g.UserID, "correlationId", middleware.GetReqID(r.Context()))
	httpx.JSON(w, http.StatusOK, g)
}

type reviewRequest struct {
	Outcome string `json:"outcome"`
	Note    string `json:"note"`
}

func (a *API) reviewBreakGlass(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	var req reviewRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	g, err := a.store.ReviewBreakGlass(r.Context(), chi.URLParam(r, "id"), p.UserID, req.Outcome, req.Note)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	slog.Warn("break-glass reviewed", "id", g.ID, "user", g.UserID, "reviewer", g.ReviewedBy, "outcome", g.ReviewOutcome, "correlationId", middleware.GetReqID(r.Context()))
	httpx.JSON(w, http.StatusOK, g)
}
