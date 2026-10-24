package api

import (
	"context"
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
	"github.com/BenjaminSRussell/passage/services/numbering/internal/domain"
	"github.com/BenjaminSRussell/passage/services/numbering/internal/store"
)

const (
	readyTimeout   = 500 * time.Millisecond
	idempotencyKey = "Idempotency-Key"
	dutyRead       = "numbering.read"
	dutyWrite      = "numbering.write"
)

// API is the numbering HTTP surface.
type API struct {
	store     store.Store
	allocated atomic.Int64
	failed    atomic.Int64
}

type createRequest struct {
	Code          string `json:"code"`
	Name          string `json:"name"`
	Format        string `json:"format"`
	LegalEntityID string `json:"legalEntityId"`
	Mode          string `json:"mode"`
	StartFrom     int64  `json:"startFrom"`
}

// New builds the API.
func New(s store.Store) *API { return &API{store: s} }

// Register mounts routes on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyWrite)).Post("/v1/numbering/sequences", a.create)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/numbering/sequences/{code}", a.get)
	r.With(authz.RequireDuty(dutyWrite)).Post("/v1/numbering/sequences/{code}/next", a.allocate)
}

// Metrics renders Prometheus text.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "# HELP numbering_allocated_total Numbers issued.\n# TYPE numbering_allocated_total counter\nnumbering_allocated_total %d\n", a.allocated.Load())
		_, _ = fmt.Fprintf(w, "# HELP numbering_failed_total Failed allocations or creates.\n# TYPE numbering_failed_total counter\nnumbering_failed_total %d\n", a.failed.Load())
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
	if req.StartFrom == 0 {
		req.StartFrom = 1
	}
	in := store.Sequence{Code: req.Code, Name: req.Name, Format: req.Format, LegalEntityID: req.LegalEntityID, Mode: req.Mode, StartFrom: req.StartFrom}
	if !ids.Valid(in.LegalEntityID) {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		p.Errors = map[string]string{"legalEntityId": "must be a UUID"}
		httpx.Fail(w, r, p)
		return
	}
	if fields := domain.Validate(domain.Definition{Code: in.Code, Name: in.Name, Format: in.Format, LegalEntityID: in.LegalEntityID, Mode: in.Mode, StartFrom: in.StartFrom}); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	entity, err := a.store.Create(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		a.failed.Add(1)
		slog.Error("create sequence failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, entity)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	entity, err := a.store.Get(r.Context(), chi.URLParam(r, "code"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, entity)
}

func (a *API) allocate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	issued, err := a.store.Allocate(r.Context(), chi.URLParam(r, "code"), r.Header.Get(idempotencyKey))
	if err != nil {
		a.failed.Add(1)
		slog.Error("allocate failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.allocated.Add(1)
	httpx.JSON(w, http.StatusOK, issued)
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	if p.LegalEntityID != "" && p.LegalEntityID != "*" && !ids.Valid(p.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "legal entity must be a single UUID"))
		return false
	}
	return true
}
