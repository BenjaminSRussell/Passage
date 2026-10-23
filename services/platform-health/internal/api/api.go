package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/platform-health/internal/store"
)

const (
	maxMessageLength = 280
	readyTimeout     = 500 * time.Millisecond
	idempotencyKey   = "Idempotency-Key"
)

type API struct {
	store    store.Store
	created  atomic.Int64
	failed   atomic.Int64
	requests atomic.Int64
}

type createRequest struct {
	Message string `json:"message"`
}

func New(s store.Store) *API { return &API{store: s} }

func (a *API) Register(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(a.countRequests)
		g.Get("/readyz", a.ready)
		g.Post("/v1/hello-ledger/entries", a.create)
		g.Get("/v1/hello-ledger/entries", a.list)
	})
}

func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "hello_ledger_entries_created_total %d\nhello_ledger_entries_failed_total %d\nhello_ledger_http_requests_total %d\n",
			a.created.Load(), a.failed.Load(), a.requests.Load())
	})
}

func (a *API) countRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.requests.Add(1)
		next.ServeHTTP(w, r)
	})
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

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req createRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	message := strings.TrimSpace(req.Message)
	if message == "" || utf8.RuneCountInString(message) > maxMessageLength {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", fmt.Sprintf("message must be 1 to %d characters", maxMessageLength)))
		return
	}
	entry, err := a.store.Create(r.Context(), message, r.Header.Get(idempotencyKey))
	if err != nil {
		a.failed.Add(1)
		slog.Error("create entry failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.created.Add(1)
	httpx.JSON(w, http.StatusCreated, entry)
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	entries, err := a.store.List(r.Context())
	if err != nil {
		slog.Error("list entries failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, entries)
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	if p.LegalEntityID != "" && !ids.Valid(p.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "legal entity must be a single UUID"))
		return false
	}
	return true
}
