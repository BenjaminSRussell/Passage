package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
	"github.com/BenjaminSRussell/passage/services/close/internal/ledger"
)

const (
	readyTimeout    = 500 * time.Millisecond
	dutyPeriodCheck = "period.check"
	dutyMaintain    = "period.maintain"
	dateLayout      = "2006-01-02"
)

type Store interface {
	PeriodRows(ctx context.Context, ledgerID, periodID string) ([]domain.Row, error)
	PeriodSpans(ctx context.Context, legalEntityID, ledgerID string, date time.Time) ([]domain.Span, error)
	RegisterPeriod(ctx context.Context, in domain.PeriodRegistration) (bool, error)
	Ping(ctx context.Context) error
}

type LedgerLookup interface {
	LegalEntity(ctx context.Context, legalEntityID, ledgerID string) (string, error)
}

type API struct {
	store       Store
	ledgers     LedgerLookup
	checks      atomic.Int64
	rejections  atomic.Int64
	failures    atomic.Int64
	yearEnds    YearEndStore
	yearLedger  YearEndLedger
	yearEndRuns atomic.Int64
}

type checkRequest struct {
	LedgerID       string `json:"ledgerId"`
	FiscalPeriodID string `json:"fiscalPeriodId"`
	PostingDate    string `json:"postingDate"`
	SourceSystem   string `json:"sourceSystem"`
	GroupID        string `json:"groupId"`
}

type checkResponse struct {
	Allowed bool          `json:"allowed"`
	Module  domain.Module `json:"module"`
	Status  domain.Status `json:"status"`
}

type resolveResponse struct {
	FiscalPeriodID string        `json:"fiscalPeriodId"`
	StartDate      string        `json:"startDate"`
	EndDate        string        `json:"endDate"`
	Module         domain.Module `json:"module"`
	Status         domain.Status `json:"status"`
}

type problemBody struct {
	Type        string            `json:"type"`
	Title       string            `json:"title"`
	Status      int               `json:"status"`
	Detail      string            `json:"detail,omitempty"`
	Instance    string            `json:"instance,omitempty"`
	Code        string            `json:"code"`
	ServiceCode string            `json:"serviceCode"`
	TraceID     string            `json:"traceId,omitempty"`
	Errors      map[string]string `json:"errors,omitempty"`
}

var problemSlugs = map[string]string{
	domain.CodeValidation:        "validation_failed",
	domain.CodePeriodClosed:      "period_closed",
	domain.CodeInvalidTransition: "period_conflict",
	domain.CodeNoPeriod:          "period_not_found",
	domain.CodeAmbiguousPeriod:   "period_ambiguous",
	domain.CodeNoRun:             "year_end_run_not_found",
	domain.CodeLedgerRejected:    "ledger_rejected",
	domain.CodeIdempotencyReuse:  "idempotency_key_reuse",
	"COM-0001":                   "validation_failed",
	"AUTH-0001":                  "forbidden_tenant",
}

var problemStatuses = map[string]int{
	domain.CodeValidation:        http.StatusBadRequest,
	domain.CodePeriodClosed:      http.StatusUnprocessableEntity,
	domain.CodePeriodOnHold:      http.StatusForbidden,
	domain.CodeInvalidTransition: http.StatusConflict,
	domain.CodeNoPeriod:          http.StatusNotFound,
	domain.CodeAmbiguousPeriod:   http.StatusConflict,
	domain.CodeNoRun:             http.StatusNotFound,
	domain.CodeIdempotencyReuse:  http.StatusConflict,
}

func New(store Store) *API { return &API{store: store} }

func (a *API) WithLedgers(l LedgerLookup) *API {
	a.ledgers = l
	return a
}

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyPeriodCheck)).Post("/v1/close/period-statuses:check", a.check)
	r.With(authz.RequireDuty(dutyPeriodCheck)).Get("/v1/close/periods:resolve", a.resolve)
	r.With(authz.RequireDuty(dutyMaintain)).Put("/v1/close/periods/{fiscalPeriodId}", a.register)
	r.With(authz.RequireDuty(dutyYearEndRun)).Post(yearEndPath, a.runYearEnd)
	r.With(authz.RequireDuty(dutyYearEndRead)).Get(yearEndPath+"/{runId}", a.getYearEnd)
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *API) check(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req checkRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if err := validate(req); err != nil {
		fail(w, r, err)
		return
	}
	rows, err := a.store.PeriodRows(r.Context(), req.LedgerID, req.FiscalPeriodID)
	if err != nil {
		a.failures.Add(1)
		slog.Error("period status read failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		fail(w, r, err)
		return
	}
	a.checks.Add(1)
	module := domain.ModuleForSource(req.SourceSystem)
	status, err := domain.Decide(rows, module, req.GroupID)
	if err != nil {
		a.rejections.Add(1)
		slog.Warn("posting rejected by period control", "err", err, "module", module, "correlationId", middleware.GetReqID(r.Context()))
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, checkResponse{Allowed: true, Module: module, Status: status})
}

func (a *API) resolve(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	q := r.URL.Query()
	module := domain.ModuleForSource(q.Get("sourceSystem"))
	errs := map[string]string{}
	if !ids.Valid(p.LegalEntityID) {
		errs["legalEntityId"] = "a single legal entity is required"
	}
	if !ids.Valid(q.Get("ledgerId")) {
		errs["ledgerId"] = "must be a UUID"
	}
	date, err := time.Parse(dateLayout, q.Get("postingDate"))
	if err != nil {
		errs["postingDate"] = "must be an ISO date (YYYY-MM-DD)"
	}
	if len(errs) > 0 {
		v := httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "request failed validation")
		v.Errors = errs
		fail(w, r, v)
		return
	}
	spans, err := a.store.PeriodSpans(r.Context(), p.LegalEntityID, q.Get("ledgerId"), date)
	if err != nil {
		a.failures.Add(1)
		slog.Error("period resolve failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		fail(w, r, err)
		return
	}
	span, err := domain.SelectSpan(spans, date)
	if err != nil {
		slog.Warn("period resolve rejected", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		fail(w, r, err)
		return
	}
	row := domain.Resolve(span.Rows, module)
	httpx.JSON(w, http.StatusOK, resolveResponse{
		FiscalPeriodID: span.FiscalPeriodID, StartDate: span.Start.Format(dateLayout), EndDate: span.End.Format(dateLayout),
		Module: row.Module, Status: row.Status,
	})
}

type registerRequest struct {
	LedgerID  string `json:"ledgerId"`
	StartDate string `json:"startDate"`
	EndDate   string `json:"endDate"`
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return
	}
	var req registerRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	periodID := chi.URLParam(r, "fiscalPeriodId")
	errs := map[string]string{}
	if !ids.Valid(p.LegalEntityID) {
		errs["legalEntityId"] = "a single legal entity is required"
	}
	if !ids.Valid(periodID) {
		errs["fiscalPeriodId"] = "must be a UUID"
	}
	if !ids.Valid(req.LedgerID) {
		errs["ledgerId"] = "must be a UUID"
	}
	start, startErr := time.Parse(dateLayout, req.StartDate)
	if startErr != nil {
		errs["startDate"] = "must be an ISO date (YYYY-MM-DD)"
	}
	end, endErr := time.Parse(dateLayout, req.EndDate)
	if endErr != nil {
		errs["endDate"] = "must be an ISO date (YYYY-MM-DD)"
	}
	if startErr == nil && endErr == nil && end.Before(start) {
		errs["endDate"] = "must not be before startDate"
	}
	if len(errs) > 0 {
		v := httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "request failed validation")
		v.Errors = errs
		fail(w, r, v)
		return
	}
	if !a.ledgerBelongsToCaller(w, r, p.LegalEntityID, req.LedgerID) {
		return
	}
	created, err := a.store.RegisterPeriod(r.Context(), domain.PeriodRegistration{
		LegalEntityID: p.LegalEntityID, LedgerID: req.LedgerID, FiscalPeriodID: periodID, Start: start, End: end, Actor: p.UserID,
	})
	if err != nil {
		var de *domain.Error
		if !errors.As(err, &de) {
			a.failures.Add(1)
			slog.Error("period registration failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		}
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", r.URL.Path)
	}
	httpx.JSON(w, status, map[string]string{"fiscalPeriodId": periodID, "startDate": req.StartDate, "endDate": req.EndDate})
}

func (a *API) ledgerBelongsToCaller(w http.ResponseWriter, r *http.Request, legalEntityID, ledgerID string) bool {
	if a.ledgers == nil {
		fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "ledger lookup is not configured"))
		return false
	}
	owner, err := a.ledgers.LegalEntity(r.Context(), legalEntityID, ledgerID)
	switch {
	case errors.Is(err, ledger.ErrNotVisible):
		fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the ledger does not belong to the caller's legal entity"))
		return false
	case err != nil:
		a.failures.Add(1)
		slog.Error("ledger lookup failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "ledger unavailable"))
		return false
	}
	want, _ := ids.Canonical(legalEntityID)
	if got, _ := ids.Canonical(owner); got != want {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the ledger does not belong to the caller's legal entity"))
		return false
	}
	return true
}

func validate(req checkRequest) error {
	errs := map[string]string{}
	if !ids.Valid(req.LedgerID) {
		errs["ledgerId"] = "must be a UUID"
	}
	if !ids.Valid(req.FiscalPeriodID) {
		errs["fiscalPeriodId"] = "must be a UUID"
	}
	if _, err := time.Parse(dateLayout, req.PostingDate); err != nil {
		errs["postingDate"] = "must be an ISO date (YYYY-MM-DD)"
	}
	if len(errs) == 0 {
		return nil
	}
	p := httpx.NewProblem(http.StatusBadRequest, domain.CodeValidation, "request failed validation")
	p.Errors = errs
	return p
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	var p *httpx.Problem
	var de *domain.Error
	switch {
	case errors.As(err, &p):
	case errors.As(err, &de):
		status := problemStatuses[de.Code]
		if status == 0 {
			status = http.StatusBadRequest
		}
		p = httpx.NewProblem(status, de.Code, de.Detail)
	default:
		slog.Error("unhandled error", "err", err, "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
		p = httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "internal error")
	}
	code := problemSlugs[p.Code]
	if code == "" {
		code = p.Code
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(problemBody{
		Type: p.Type, Title: p.Title, Status: p.Status, Detail: p.Detail, Instance: r.URL.Path,
		Code: code, ServiceCode: p.Code, TraceID: middleware.GetReqID(r.Context()), Errors: p.Errors,
	})
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		tenant.WriteReplayMetrics(w)
		fmt.Fprintf(w, "# TYPE close_period_checks_total counter\nclose_period_checks_total %d\n", a.checks.Load())
		fmt.Fprintf(w, "# TYPE close_period_rejections_total counter\nclose_period_rejections_total %d\n", a.rejections.Load())
		fmt.Fprintf(w, "# TYPE close_period_check_failures_total counter\nclose_period_check_failures_total %d\n", a.failures.Load())
		fmt.Fprintf(w, "# TYPE close_year_end_runs_completed_total counter\nclose_year_end_runs_completed_total %d\n", a.yearEndRuns.Load())
	})
}
