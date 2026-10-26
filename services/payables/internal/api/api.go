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
	"github.com/BenjaminSRussell/passage/services/payables/internal/domain"
)

const (
	readyTimeout   = 500 * time.Millisecond
	dutySettleRead = "settlement.read"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

type API struct {
	db           Pinger
	now          func() time.Time
	previews     atomic.Int64
	clientErrors atomic.Int64
	serverFaults atomic.Int64
	http         requestMetrics
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
	domain.CodeCurrency:   "validation_failed",
	"COM-0001":            "validation_failed",
	"AUTH-0001":           "forbidden_tenant",
}

func New(db Pinger, now func() time.Time) *API {
	return &API{db: db, now: now, http: newRequestMetrics()}
}

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutySettleRead)).Post("/v1/payables/settlements:preview", a.preview)
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

func (a *API) preview(w http.ResponseWriter, r *http.Request) {
	if !a.validTenant(w, r) {
		return
	}
	var dto domain.RequestDTO
	if err := httpx.Decode(r, &dto); err != nil {
		a.reject(w, r, err)
		return
	}
	req, err := domain.ParseRequest(dto, a.now())
	if err != nil {
		a.reject(w, r, err)
		return
	}
	res, err := domain.Settle(req)
	if err != nil {
		a.serverFaults.Add(1)
		slog.Error("settlement preview failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		fail(w, r, err)
		return
	}
	a.previews.Add(1)
	httpx.JSON(w, http.StatusOK, res)
}

func (a *API) reject(w http.ResponseWriter, r *http.Request, err error) {
	a.clientErrors.Add(1)
	slog.Warn("settlement preview rejected", "err", err, "correlationId", middleware.GetReqID(r.Context()))
	var de *domain.Error
	if errors.As(err, &de) {
		status := http.StatusBadRequest
		if de.Code == domain.CodeCurrency {
			status = http.StatusUnprocessableEntity
		}
		err = httpx.NewProblem(status, de.Code, de.Detail)
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

func (a *API) validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		a.clientErrors.Add(1)
		fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	if p.LegalEntityID != "" && !ids.Valid(p.LegalEntityID) {
		a.clientErrors.Add(1)
		fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "legal entity must be a single UUID"))
		return false
	}
	return true
}

func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP payables_settlement_previews_total Successful settlement previews.\n# TYPE payables_settlement_previews_total counter\npayables_settlement_previews_total %d\n", a.previews.Load())
		fmt.Fprintf(w, "# HELP payables_settlement_preview_client_errors_total Previews rejected as invalid input (4xx).\n# TYPE payables_settlement_preview_client_errors_total counter\npayables_settlement_preview_client_errors_total %d\n", a.clientErrors.Load())
		fmt.Fprintf(w, "# HELP payables_settlement_preview_server_faults_total Previews that failed on a server fault (5xx).\n# TYPE payables_settlement_preview_server_faults_total counter\npayables_settlement_preview_server_faults_total %d\n", a.serverFaults.Load())
		a.http.write(w)
	})
}
