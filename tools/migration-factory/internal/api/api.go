// Package api exposes the migration-factory HTTP surface: open_items, gl_balances and project_wip
// pack runs with their control totals and quarantine release, the legacy cross-reference lookup, the
// parallel-run review, project opening balances, and the cutover runbook with its rollback
// re-keying export. Handlers decode and validate the request, read the principal from the tenant
// context, apply the domain rules and persist through the Store, which scopes every transaction
// to the tenant.
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
	DutyMigrationRun    = "MigrationRun"
	DutyMigrationView   = "MigrationView"
	DutyMigrationReview = "MigrationReview"
	DutyCutoverManager  = "CutoverManager"
	DutyStepOwner       = "CutoverStepOwner"
)

// Problem codes raised by this service. Rule violations carry the MIG-/CUT- message code in detail.
const (
	CodeInvalid          = "MIG-0001"
	CodeNotFound         = "MIG-0003"
	CodeTransition       = "MIG-0004"
	CodeUnsupported      = "MIG-0005"
	CodeVersion          = "MIG-0006"
	CodeIdempotencyReuse = "MIG-0007"
	CodeSoD              = "MIG-0008"
	CodeExists           = "MIG-0009"
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
	StartStep(ctx context.Context, idem store.Idem, st store.StepStart) (domain.Plan, bool, error)
	CompleteStep(ctx context.Context, idem store.Idem, c store.StepCompletion) (domain.Plan, bool, error)
	Rollback(ctx context.Context, idem store.Idem, r store.RollbackRequest) (domain.Plan, bool, error)
	RekeyLines(ctx context.Context, planID string, kind domain.PostingKind) ([]domain.RekeyPosting, error)
	ReleaseQuarantined(ctx context.Context, idem store.Idem, rq store.Release) (domain.PackRun, bool, error)
	LookupXref(ctx context.Context, q store.XrefQuery) ([]domain.Xref, error)
	StartParallelRun(ctx context.Context, idem store.Idem, run domain.ParallelRun) (domain.ParallelRun, bool, error)
	GetParallelRun(ctx context.Context, id string) (domain.ParallelRun, error)
	ListParallelRuns(ctx context.Context, f store.ParallelFilter) ([]domain.ParallelRun, error)
	ExplainVariance(ctx context.Context, idem store.Idem, e store.ExplainVariance) (domain.ParallelRun, bool, error)
	SignOffParallelRun(ctx context.Context, idem store.Idem, so store.SignOff) (domain.ParallelRun, bool, error)
	RunProjectWIP(ctx context.Context, idem store.Idem, in store.ProjectWIPRun) (domain.PackRun, bool, error)
	RunGLBalances(ctx context.Context, idem store.Idem, in store.GLBalancesRun) (domain.PackRun, bool, error)
	GetProjectOpening(ctx context.Context, f store.OpeningFilter) (store.ProjectOpening, error)
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

// Routes returns the router with every endpoint mounted under /api/v1.
func (s *Server) Routes() http.Handler {
	r := httpx.NewRouter()
	s.Register(r)
	return r
}

// Register mounts the endpoints on r.
func (s *Server) Register(r chi.Router) {
	r.Route("/api/v1", func(r chi.Router) {
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/packs", s.packCatalog)
		r.With(authz.RequireDuty(DutyMigrationRun)).Post("/migration/packs/{pack}/runs", s.startPackRun)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs", s.listRuns)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs/{id}", s.getRun)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs/{id}/control-totals", s.controlTotals)
		r.With(authz.RequireDuty(DutyMigrationRun)).Post("/migration/runs/{id}/quarantine/{rec}/release", s.releaseQuarantined)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs/{id}/projects/{project}/opening", s.projectOpening)
		// Any authenticated caller may resolve a legacy id (9.49.5); the tenant middleware has
		// already refused requests without a principal.
		r.Get("/migration/xref", s.lookupXref)
		r.With(authz.RequireDuty(DutyMigrationRun)).Post("/migration/parallel-runs", s.startParallelRun)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/parallel-runs", s.listParallelRuns)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/parallel-runs/{id}", s.getParallelRun)
		r.With(authz.RequireDuty(DutyMigrationReview)).Post("/migration/parallel-runs/{id}/variances/{v}/explain", s.explainVariance)
		r.With(authz.RequireDuty(DutyMigrationReview)).Post("/migration/parallel-runs/{id}/sign-off", s.signOffParallelRun)
		r.With(authz.RequireDuty(DutyCutoverManager)).Post("/cutover/plans/generate", s.generatePlan)
		r.With(authz.RequireDuty(DutyCutoverManager)).Get("/cutover/plans", s.listPlans)
		r.With(authz.RequireDuty(DutyCutoverManager)).Get("/cutover/plans/{id}", s.getPlan)
		r.With(authz.RequireDuty(DutyStepOwner)).Post("/cutover/plans/{id}/steps/{seq}/start", s.startStep)
		r.With(authz.RequireDuty(DutyStepOwner)).Post("/cutover/plans/{id}/steps/{seq}/complete", s.completeStep)
		r.With(authz.RequireDuty(DutyCutoverManager)).Post("/cutover/plans/{id}/rollback", s.rollback)
		r.With(authz.RequireDuty(DutyCutoverManager)).Get("/cutover/plans/{id}/rekeying/{file}", s.rekeyFile)
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
	case errors.Is(err, domain.ErrSoD):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, CodeSoD, err.Error()))
	case errors.Is(err, store.ErrParallelRunExists):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeExists, err.Error()))
	case errors.Is(err, domain.ErrNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, CodeNotFound, err.Error()))
	case errors.Is(err, domain.ErrTransition):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeTransition, err.Error()))
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, CodeNotFound, notFoundDetail(err)))
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

// notFoundDetail keeps the store's reason when it names one (a quarantined record, a project),
// and says "not found for this tenant" for a plain miss.
func notFoundDetail(err error) string {
	if errors.Is(err, store.ErrNotFound) && err.Error() != store.ErrNotFound.Error() {
		return err.Error()
	}
	return "not found for this tenant"
}

// seqParam reads a positive integer path parameter.
func seqParam(r *http.Request, name string) (int, error) {
	n, err := strconv.Atoi(chi.URLParam(r, name))
	if err != nil || n <= 0 {
		return 0, invalid(name + " must be a positive integer")
	}
	return n, nil
}
