package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
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
	unmatchedRoute = "unmatched"
)

type Pinger interface {
	Ping(ctx context.Context) error
}

type API struct {
	db       Pinger
	now      func() time.Time
	previews atomic.Int64
	failed   atomic.Int64
	mu       sync.Mutex
	requests map[requestKey]int64
}

type requestKey struct {
	route  string
	status int
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
	return &API{db: db, now: now, requests: map[requestKey]int64{}}
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
	if !validTenant(w, r) {
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
		a.failed.Add(1)
		slog.Error("settlement preview failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		fail(w, r, err)
		return
	}
	a.previews.Add(1)
	httpx.JSON(w, http.StatusOK, res)
}

func (a *API) reject(w http.ResponseWriter, r *http.Request, err error) {
	a.failed.Add(1)
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
		fmt.Fprintf(w, "# HELP payables_settlement_previews_total Settlement previews computed.\n# TYPE payables_settlement_previews_total counter\npayables_settlement_previews_total %d\n", a.previews.Load())
		fmt.Fprintf(w, "# HELP payables_settlement_preview_failures_total Settlement previews rejected or failed.\n# TYPE payables_settlement_preview_failures_total counter\npayables_settlement_preview_failures_total %d\n", a.failed.Load())
		a.mu.Lock()
		defer a.mu.Unlock()
		keys := make([]requestKey, 0, len(a.requests))
		for k := range a.requests {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].route != keys[j].route {
				return keys[i].route < keys[j].route
			}
			return keys[i].status < keys[j].status
		})
		fmt.Fprint(w, "# HELP payables_http_requests_total HTTP requests by route and status.\n# TYPE payables_http_requests_total counter\n")
		for _, k := range keys {
			fmt.Fprintf(w, "payables_http_requests_total{route=%q,status=\"%d\"} %d\n", k.route, k.status, a.requests[k])
		}
	})
}

func (a *API) Instrument(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		rctx := chi.NewRouteContext()
		next.ServeHTTP(ww, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
		route := rctx.RoutePattern()
		if route == "" {
			route = unmatchedRoute
		}
		status := ww.Status()
		if status == 0 {
			status = http.StatusOK
		}
		a.mu.Lock()
		a.requests[requestKey{route, status}]++
		a.mu.Unlock()
	})
}
