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
	"github.com/BenjaminSRussell/passage/services/tax/internal/domain"
)

const (
	readyTimeout  = 500 * time.Millisecond
	dutyCalculate = "Calculate tax"

	dutyWithholding = "Calculate withholding tax"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

type API struct {
	db              Pinger
	providers       map[string]domain.TaxProvider
	providerTimeout time.Duration
	calculations    atomic.Int64
	failures        atomic.Int64
	fallbacks       atomic.Int64
}

type calculateRequest struct {
	domain.Document
	Config   domain.Config `json:"config"`
	Provider string        `json:"provider"`
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
	domain.CodeValidation: "validation_failed",
	"AUTH-0001":           "forbidden_tenant",
}

func New(db Pinger, providers map[string]domain.TaxProvider, providerTimeout time.Duration) *API {
	return &API{db: db, providers: providers, providerTimeout: providerTimeout}
}

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyCalculate)).Post("/v1/tax/calculate", a.calculate)
	r.With(authz.RequireDuty(dutyWithholding)).Post("/v1/tax/withholding/calculate", a.withholding)
}

func (a *API) withholding(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req domain.WithholdingRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	res, err := domain.CalculateWithholding(req)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	a.calculations.Add(1)
	httpx.JSON(w, http.StatusOK, res)
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.db.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *API) calculate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req calculateRequest
	if err := httpx.Decode(r, &req); err != nil {
		a.reject(w, r, err)
		return
	}
	res, err := a.compute(r.Context(), req)
	if err != nil {
		a.reject(w, r, err)
		return
	}
	a.calculations.Add(1)
	httpx.JSON(w, http.StatusOK, res)
}

func (a *API) compute(ctx context.Context, req calculateRequest) (domain.Result, error) {
	if req.Provider == "" {
		return domain.Calculate(req.Document, req.Config)
	}
	provider, ok := a.providers[req.Provider]
	if !ok {
		return domain.Result{}, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeProviderFallback, fmt.Sprintf("provider %q is not configured", req.Provider))
	}
	res, notice, err := domain.CalculateWithFailover(ctx, provider, req.Document, req.Config, a.providerTimeout)
	if notice != nil {
		a.fallbacks.Add(1)
		slog.Warn("tax provider fallback", "provider", notice.Provider, "reason", notice.Reason, "correlationId", middleware.GetReqID(ctx))
	}
	return res, err
}

func (a *API) reject(w http.ResponseWriter, r *http.Request, err error) {
	a.failures.Add(1)
	slog.Warn("tax calculation rejected", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	var de *domain.Error
	var pe *domain.ProviderError
	switch {
	case errors.As(err, &de):
		status := http.StatusUnprocessableEntity
		if de.Code == domain.CodeValidation {
			status = http.StatusBadRequest
		}
		problem := httpx.NewProblem(status, de.Code, de.Detail)
		if de.Field != "" {
			problem.Errors = map[string]string{de.Field: de.Detail}
		}
		err = problem
	case errors.As(err, &pe):
		err = httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeValidation, fmt.Sprintf("provider rejected the document with status %d", pe.Status))
	}
	fail(w, r, err)
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	var p *httpx.Problem
	if !errors.As(err, &p) {
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
	if p.LegalEntityID != "" && !ids.Valid(p.LegalEntityID) {
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "legal entity must be a single UUID"))
		return false
	}
	return true
}

func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE tax_calculations_total counter\ntax_calculations_total %d\n", a.calculations.Load())
		fmt.Fprintf(w, "# TYPE tax_calculation_failures_total counter\ntax_calculation_failures_total %d\n", a.failures.Load())
		fmt.Fprintf(w, "# TYPE tax_provider_fallbacks_total counter\ntax_provider_fallbacks_total %d\n", a.fallbacks.Load())
	})
}
