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

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/platform-health/internal/store"
)

const (
	maxMessageLength = 280
	readyTimeout     = 500 * time.Millisecond
	idempotencyKey   = "Idempotency-Key"
	dutyWrite        = "hello.write"
	dutyRead         = "hello.read"
	dutyReleaseWrite = "Maintain platform releases"
	dutyReleaseRead  = "View platform releases"
	dutyApproveProd  = "Approve production promotion"
	dutyRollback     = "Rollback environment"
)

type API struct {
	store          store.Store
	operatorTenant string
	created        atomic.Int64
	failed         atomic.Int64
	http           requestMetrics

	releasesRecorded atomic.Int64
	releasesReplayed atomic.Int64
	releasesFailed   atomic.Int64
	chainBreaks      atomic.Int64
}

type createRequest struct {
	Message string `json:"message"`
}

func New(s store.Store, operatorTenant string) *API {
	return &API{store: s, operatorTenant: operatorTenant, http: newRequestMetrics()}
}

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyWrite)).Post("/v1/hello-ledger/entries", a.create)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/hello-ledger/entries", a.list)
	r.With(authz.RequireAnyDuty(dutyReleaseWrite, dutyApproveProd, dutyRollback)).Post("/v1/platform/releases", a.recordRelease)
	r.With(authz.RequireDuty(dutyReleaseRead)).Get("/v1/platform/releases", a.listReleases)
}

func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "# HELP hello_ledger_entries_created_total Entries created.\n# TYPE hello_ledger_entries_created_total counter\nhello_ledger_entries_created_total %d\n", a.created.Load())
		_, _ = fmt.Fprintf(w, "# HELP hello_ledger_entries_failed_total Entry creations that failed in the store.\n# TYPE hello_ledger_entries_failed_total counter\nhello_ledger_entries_failed_total %d\n", a.failed.Load())
		_, _ = fmt.Fprintf(w, "# HELP hello_ledger_releases_recorded_total Release rows recorded.\n# TYPE hello_ledger_releases_recorded_total counter\nhello_ledger_releases_recorded_total %d\n", a.releasesRecorded.Load())
		_, _ = fmt.Fprintf(w, "# HELP hello_ledger_releases_replayed_total Idempotent release replays answered without a new row.\n# TYPE hello_ledger_releases_replayed_total counter\nhello_ledger_releases_replayed_total %d\n", a.releasesReplayed.Load())
		_, _ = fmt.Fprintf(w, "# HELP hello_ledger_releases_failed_total Release recordings that failed in the store.\n# TYPE hello_ledger_releases_failed_total counter\nhello_ledger_releases_failed_total %d\n", a.releasesFailed.Load())
		_, _ = fmt.Fprintf(w, "# HELP hello_ledger_release_chain_breaks_total Recorded releases whose previousDigest differed from the newest recorded image for the service and environment.\n# TYPE hello_ledger_release_chain_breaks_total counter\nhello_ledger_release_chain_breaks_total %d\n", a.chainBreaks.Load())
		a.http.write(w)
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
	_, ok := validPrincipal(w, r)
	return ok
}

func validPrincipal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return p, false
	}
	if p.LegalEntityID != "" && !ids.Valid(p.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "legal entity must be a single UUID"))
		return p, false
	}
	return p, true
}
