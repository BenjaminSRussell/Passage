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
	"github.com/BenjaminSRussell/passage/services/audit/internal/domain"
	"github.com/BenjaminSRussell/passage/services/audit/internal/store"
)

const (
	readyTimeout = 500 * time.Millisecond
	dutyRead     = "audit.read"
	dutyWrite    = "audit.write"
)

// API is the audit HTTP surface.
type API struct {
	store   store.Store
	applied atomic.Int64
	failed  atomic.Int64
	// Read-log counters (#606).
	reads       atomic.Int64
	readsFailed atomic.Int64
	exports     atomic.Int64
	reviews     atomic.Int64
}

type ingestRequest struct {
	Entity     string    `json:"entity"`
	RecordID   string    `json:"recordId"`
	Field      string    `json:"field"`
	OldValue   string    `json:"oldValue"`
	NewValue   string    `json:"newValue"`
	UserID     string    `json:"userId"`
	TraceID    string    `json:"traceId"`
	OccurredAt time.Time `json:"occurredAt"`
}

type expandRequest struct {
	Body string            `json:"body"`
	Vars map[string]string `json:"vars"`
}

type ruleRequest struct {
	Entity string `json:"entity"`
	Field  string `json:"field"`
	Kind   string `json:"kind"`
}

// New builds the API.
func New(s store.Store) *API { return &API{store: s} }

// Register mounts routes on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyWrite)).Post("/v1/audit/changes", a.ingest)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/audit/records/{entity}/{recordId}", a.query)
	r.With(authz.RequireDuty(dutyWrite)).Post("/v1/audit/alert-rules", a.createRule)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/audit/alerts", a.listAlerts)
	r.Post("/v1/audit/texts/expand", a.expand)
	// PHI read-access log (#606). Only services record reads; the reader is the verified caller.
	r.With(authz.RequireDuty(dutyReadLogWrite)).Post("/v1/audit/reads", a.recordRead)
	r.With(authz.RequireDuty(dutyReadLogView)).Get("/v1/audit/reads", a.listReads)
	r.With(authz.RequireDuty(dutyReadLogExport)).Get("/v1/audit/reads:export", a.exportReads)
	r.With(authz.RequireDuty(dutyReadLogExport)).Get("/v1/audit/reads:verify", a.verifyReads)
	// Break-glass reads (purpose BREAK-GLASS) are reviewed by a second person (#606).
	r.With(authz.RequireDuty(dutyReadLogReview)).Get("/v1/audit/reads:break-glass", a.listBreakGlass)
	r.With(authz.RequireDuty(dutyReadLogReview)).Post("/v1/audit/reads/{id}:review", a.reviewRead)
}

// Metrics renders Prometheus text.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "# HELP audit_changes_applied_total Field changes appended.\n# TYPE audit_changes_applied_total counter\naudit_changes_applied_total %d\n", a.applied.Load())
		_, _ = fmt.Fprintf(w, "# HELP audit_changes_failed_total Failed ingest calls.\n# TYPE audit_changes_failed_total counter\naudit_changes_failed_total %d\n", a.failed.Load())
		_, _ = fmt.Fprintf(w, "# HELP audit_phi_reads_recorded_total PHI reads appended to the read log.\n# TYPE audit_phi_reads_recorded_total counter\naudit_phi_reads_recorded_total %d\n", a.reads.Load())
		_, _ = fmt.Fprintf(w, "# HELP audit_phi_reads_failed_total PHI read records that were not stored.\n# TYPE audit_phi_reads_failed_total counter\naudit_phi_reads_failed_total %d\n", a.readsFailed.Load())
		_, _ = fmt.Fprintf(w, "# HELP audit_phi_read_exports_total Read-log exports delivered.\n# TYPE audit_phi_read_exports_total counter\naudit_phi_read_exports_total %d\n", a.exports.Load())
		_, _ = fmt.Fprintf(w, "# HELP audit_phi_break_glass_reviews_total Break-glass reads reviewed.\n# TYPE audit_phi_break_glass_reviews_total counter\naudit_phi_break_glass_reviews_total %d\n", a.reviews.Load())
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

func (a *API) ingest(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req ingestRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.OccurredAt.IsZero() {
		req.OccurredAt = time.Now().UTC()
	}
	if req.UserID == "" {
		if p, ok := tenant.FromContext(r.Context()); ok {
			req.UserID = p.UserID
		}
	}
	if req.TraceID == "" {
		req.TraceID = middleware.GetReqID(r.Context())
	}
	change := domain.Change{
		Entity: req.Entity, RecordID: req.RecordID, Field: req.Field,
		OldValue: req.OldValue, NewValue: req.NewValue, UserID: req.UserID, TraceID: req.TraceID, OccurredAt: req.OccurredAt,
	}
	if fields := domain.Validate(change); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	if err := a.store.Apply(r.Context(), change); err != nil {
		a.failed.Add(1)
		slog.Error("ingest failed", "err", err, "correlationId", middleware.GetReqID(r.Context()), "traceId", req.TraceID)
		httpx.Fail(w, r, err)
		return
	}
	a.applied.Add(1)
	httpx.JSON(w, http.StatusAccepted, map[string]string{"status": "recorded"})
}

func (a *API) query(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	rows, err := a.store.Query(r.Context(), chi.URLParam(r, "entity"), chi.URLParam(r, "recordId"))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (a *API) createRule(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req ruleRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Entity == "" {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
		p.Errors = map[string]string{"entity": "is required"}
		httpx.Fail(w, r, p)
		return
	}
	rule, err := a.store.CreateRule(r.Context(), store.AlertRule{Entity: req.Entity, Field: req.Field, Kind: req.Kind})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, rule)
}

func (a *API) listAlerts(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.store.ListAlerts(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) expand(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req expandRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"body": domain.Expand(req.Body, req.Vars)})
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}
