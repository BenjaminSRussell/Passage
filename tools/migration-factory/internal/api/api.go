// Package api exposes the migration-factory HTTP surface (slice 1: open-items pack runs, their
// control totals and quarantine, and the cutover runbook). Handlers decode and validate the
// request, read the principal from the tenant context, apply the domain rules and persist
// through the Store, which scopes every transaction to the tenant.
package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

// Duties checked on the routes (9.49.17).
const (
	DutyMigrationRun   = "MigrationRun"
	DutyMigrationView  = "MigrationView"
	DutyCutoverManager = "CutoverManager"
	DutyStepOwner      = "CutoverStepOwner"
)

// Problem codes raised by this service. Rule violations carry the MIG-/CUT- message code in detail.
const (
	CodeInvalid          = "MIG-0001"
	CodeNotFound         = "MIG-0003"
	CodeTransition       = "MIG-0004"
	CodeUnsupported      = "MIG-0005"
	CodeVersion          = "MIG-0006"
	CodeIdempotencyReuse = "MIG-0007"
)

// packRunBodyLimit bounds a pack run request. An extract travels in the body in slice 1.
const packRunBodyLimit = 16 << 20

// Store is the persistence the handlers need; *store.Postgres implements it.
type Store interface {
	Ping(ctx context.Context) error
	RunOpenItems(ctx context.Context, idem store.Idem, in store.OpenItemsRun) (domain.PackRun, bool, error)
	GetRun(ctx context.Context, id string) (domain.PackRun, error)
	ListRuns(ctx context.Context, f store.RunFilter) ([]domain.PackRun, error)
	CreatePlan(ctx context.Context, idem store.Idem, plan domain.Plan, createdBy string) (domain.Plan, bool, error)
	GetPlan(ctx context.Context, id string) (domain.Plan, error)
	ListPlans(ctx context.Context, f store.PlanFilter) ([]domain.Plan, error)
	CompleteStep(ctx context.Context, idem store.Idem, c store.StepCompletion) (domain.Plan, bool, error)
	Rollback(ctx context.Context, idem store.Idem, r store.RollbackRequest) (domain.Plan, bool, error)
}

// Server serves the API over a Store.
type Server struct {
	store Store
	now   func() time.Time
}

// New returns a server over st.
func New(st Store) *Server { return &Server{store: st, now: time.Now} }

// WithClock replaces the clock used for the abort-deadline warning (tests).
func (s *Server) WithClock(now func() time.Time) *Server { s.now = now; return s }

// Routes returns the router with every slice-1 endpoint mounted under /api/v1.
func (s *Server) Routes() http.Handler {
	r := httpx.NewRouter()
	s.Register(r)
	return r
}

// Register mounts the endpoints on r.
func (s *Server) Register(r chi.Router) {
	r.Route("/api/v1", func(r chi.Router) {
		r.With(authz.RequireDuty(DutyMigrationRun)).Post("/migration/packs/{pack}/runs", s.startPackRun)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs", s.listRuns)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs/{id}", s.getRun)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs/{id}/control-totals", s.controlTotals)
		r.With(authz.RequireDuty(DutyCutoverManager)).Post("/cutover/plans/generate", s.generatePlan)
		r.With(authz.RequireDuty(DutyCutoverManager)).Get("/cutover/plans", s.listPlans)
		r.With(authz.RequireDuty(DutyCutoverManager)).Get("/cutover/plans/{id}", s.getPlan)
		r.With(authz.RequireDuty(DutyStepOwner)).Post("/cutover/plans/{id}/steps/{seq}/complete", s.completeStep)
		r.With(authz.RequireDuty(DutyCutoverManager)).Post("/cutover/plans/{id}/rollback", s.rollback)
	})
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
	}
	return p, ok
}

func invalid(detail string) error {
	return httpx.NewProblem(http.StatusUnprocessableEntity, CodeInvalid, detail)
}

// fail maps domain and store errors to problem details.
func fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrUnknownPack):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeUnsupported, err.Error()))
	case errors.Is(err, domain.ErrInvalid):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeInvalid, err.Error()))
	case errors.Is(err, domain.ErrTransition):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeTransition, err.Error()))
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, CodeNotFound, "not found for this tenant"))
	case errors.Is(err, store.ErrVersion):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeVersion, err.Error()))
	case errors.Is(err, store.ErrIdempotencyReuse):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request"))
	default:
		httpx.Fail(w, r, err)
	}
}

// readCommand reads the body of a command, decodes it into v and returns the idempotency
// identity (the Idempotency-Key header bound to a hash of the request).
func readCommand(r *http.Request, v any, limit int64) (store.Idem, error) {
	body, err := io.ReadAll(http.MaxBytesReader(nil, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return store.Idem{}, httpx.NewProblem(http.StatusRequestEntityTooLarge, "COM-0413", "request body exceeds the size limit")
		}
		return store.Idem{}, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "unreadable body")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err := httpx.DecodeLimit(r, v, limit); err != nil {
		return store.Idem{}, err
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) > 200 {
		return store.Idem{}, invalid("Idempotency-Key must be at most 200 characters")
	}
	idem := store.Idem{Key: key}
	if key != "" {
		idem.Hash = store.HashRequest(r.Method, r.URL.Path, body)
	}
	return idem, nil
}

func respond(w http.ResponseWriter, status int, replayed bool, v any) {
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, status, v)
}

func pathID(r *http.Request) (string, error) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		return "", invalid("id must be a UUID")
	}
	return id, nil
}

func listLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > store.MaxListLimit {
		return 0, invalid("limit must be an integer from 1 to 200")
	}
	return n, nil
}

func after(r *http.Request) (string, error) {
	a := r.URL.Query().Get("after")
	if a != "" && !ids.Valid(a) {
		return "", invalid("after must be a UUID")
	}
	return a, nil
}
