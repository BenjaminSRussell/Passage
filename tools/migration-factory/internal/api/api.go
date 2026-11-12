// Package api exposes the migration-factory HTTP surface: open_items, gl_balances, onhand and project_wip
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
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/replay"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
)

// Duties checked on the routes (9.49.17).
const (
	DutyMigrationRun    = "migration.factory.run"
	DutyMigrationView   = "migration.factory.view"
	DutyMigrationReview = "migration.factory.review"
	DutyCutoverManager  = "migration.cutover.manage"
	DutyStepOwner       = "migration.cutover-step.execute"
	// DutyOpeningTargetRelease frees a legacy company's pinned opening-journal target (#549).
	DutyOpeningTargetRelease = "migration.opening-target.release"
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
	ListTolerances(ctx context.Context) ([]domain.ToleranceRule, error)
	PutTolerance(ctx context.Context, idem store.Idem, c store.ToleranceChange) (domain.ToleranceRule, bool, error)
	ListRuns(ctx context.Context, f store.RunFilter) ([]domain.PackRun, error)
	CreatePlan(ctx context.Context, idem store.Idem, plan domain.Plan, createdBy string) (domain.Plan, bool, error)
	GetPlan(ctx context.Context, id string) (domain.Plan, error)
	ListPlans(ctx context.Context, f store.PlanFilter) ([]domain.Plan, error)
	StartStep(ctx context.Context, idem store.Idem, st store.StepStart) (domain.Plan, bool, error)
	CompleteStep(ctx context.Context, idem store.Idem, c store.StepCompletion) (domain.Plan, bool, error)
	Rollback(ctx context.Context, idem store.Idem, r store.RollbackRequest) (store.RollbackResult, bool, error)
	GetReadiness(ctx context.Context, planID string) (domain.ReadinessFacts, error)
	RecordDecision(ctx context.Context, idem store.Idem, r store.DecisionRequest) (store.DecisionResult, bool, error)
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
	RunOnhand(ctx context.Context, idem store.Idem, in store.OnhandRun) (domain.PackRun, bool, error)
	RunBankBalances(ctx context.Context, idem store.Idem, in store.BankRun) (domain.PackRun, bool, error)
	RunOpenOrders(ctx context.Context, idem store.Idem, in store.OpenOrdersRun) (domain.PackRun, bool, error)
	RunStaged(ctx context.Context, idem store.Idem, in store.StagedRun) (domain.PackRun, bool, error)
	PostCompensations(ctx context.Context, ledger ledgerclient.Ledger, in store.CompensationRun) (store.CompensationReport, error)
	CompensationReport(ctx context.Context, planID string) (store.CompensationReport, error)
	PostOpeningJournal(ctx context.Context, ledger ledgerclient.Ledger, in store.OpeningJournalRequest) (store.OpeningJournalResult, error)
	OpeningPostings(ctx context.Context, runID string) ([]store.OpeningPosting, error)
	SetOpeningMapping(ctx context.Context, planID string, system domain.SourceSystem, company, legalEntityID, ledgerID, by string) (store.OpeningMapping, error)
	ListOpeningMappings(ctx context.Context, planID string) ([]store.OpeningMapping, error)
	ReleaseOpeningTarget(ctx context.Context, system domain.SourceSystem, company, reason, by string) (store.OpeningRelease, error)
	ListCoexistence(ctx context.Context, system domain.SourceSystem, company string) ([]domain.CoexistFlag, error)
	SetCoexistence(ctx context.Context, idem store.Idem, c store.CoexistChange) (domain.CoexistFlag, bool, error)
	GetProjectOpening(ctx context.Context, f store.OpeningFilter) (store.ProjectOpening, error)
	GetTargetLoad(ctx context.Context, runID string) (domain.TargetLoad, error)
	ListTargetRows(ctx context.Context, runID string, f store.TargetRowFilter) ([]domain.TargetRowView, error)
	ClaimTargetLoad(ctx context.Context, runID, startedBy string, lease time.Duration) (domain.TargetLoad, []store.PendingRow, error)
	RecordTargetChunk(ctx context.Context, runID string, lease time.Duration, outs []store.RowOutcome) error
	FinishTargetLoad(ctx context.Context, runID string, fin store.TargetFinish) (domain.TargetLoad, []domain.ControlTotal, error)
	GetPayablesLoad(ctx context.Context, runID string) (domain.PayablesLoad, error)
	ClaimPayablesLoad(ctx context.Context, runID, startedBy string, lease time.Duration) (domain.PayablesLoad, []store.PendingPayable, error)
	RecordPayablesBatch(ctx context.Context, runID string, lease time.Duration, b store.PayablesBatchSent) error
	RejectPayablesRows(ctx context.Context, runID string, lease time.Duration, rejects []store.PayablesReject) error
	FinishPayablesLoad(ctx context.Context, runID string, fin store.PayablesFinish) (domain.PayablesLoad, []domain.ControlTotal, error)
	GetOpeningLoad(ctx context.Context, runID string) (domain.OpeningLoad, error)
	ClaimOpeningLoad(ctx context.Context, runID, legalEntityID, startedBy string, lease time.Duration) (domain.OpeningLoad, store.OpeningRun, []store.PlannedOpening, []store.PendingOpening, error)
	PlanOpeningBatch(ctx context.Context, runID, legalEntityID string, lease time.Duration, p store.PlannedOpening) error
	DropOpeningPlan(ctx context.Context, runID, key string) error
	RecordOpeningBatch(ctx context.Context, runID, legalEntityID string, lease time.Duration, b store.OpeningBatchSent) error
	RejectOpeningRows(ctx context.Context, runID, legalEntityID string, lease time.Duration, rejects []store.OpeningReject) error
	ListOpeningRejects(ctx context.Context, runID string, limit int) ([]store.OpeningRejectRow, error)
	OpeningOnHandExpected(ctx context.Context, legalEntityID string) (map[string]decimal.Decimal, error)
	FinishOpeningLoad(ctx context.Context, runID string, fin store.OpeningFinish) (domain.OpeningLoad, []domain.ControlTotal, error)
}

// Server serves the API over a Store.
type Server struct {
	store  Store
	now    func() time.Time
	target Target
	// payables is the target of the AP lines of an open_items run (49-3).
	payables PayablesTarget
	// inventory and treasury are the targets of the staged opening stock and opening bank balances (H-S9-1, H-S10-1).
	inventory StockTarget
	treasury  BankTarget
	ledger    ledgerclient.Ledger
	// subledgers post the subledger documents of a parallel-run replay (49-4).
	subledgers replay.Ports
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
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/tolerances", s.listTolerances)
		r.With(authz.RequireDuty(DutyMigrationReview)).Put("/migration/tolerances", s.putTolerance)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs", s.listRuns)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs/{id}", s.getRun)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs/{id}/control-totals", s.controlTotals)
		r.With(authz.RequireDuty(DutyMigrationRun)).Post("/migration/runs/{id}/quarantine/{rec}/release", s.releaseQuarantined)
		r.With(authz.RequireDuty(DutyMigrationRun)).Post("/migration/runs/{id}/load", s.runTargetLoad)
		r.With(authz.RequireDuty(DutyMigrationRun)).Post("/migration/runs/{id}/load-payables", s.runPayablesLoad)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs/{id}/payables-load", s.getPayablesLoad)
		r.With(authz.RequireDuty(DutyMigrationRun)).Post("/migration/runs/{id}/opening-load", s.runOpeningLoad)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs/{id}/opening-load", s.getOpeningLoad)
		r.With(authz.RequireDuty(DutyMigrationRun)).Post("/migration/runs/{id}/opening-journal", s.postOpeningJournal)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs/{id}/opening-journal", s.getOpeningJournal)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs/{id}/target-load", s.getTargetLoad)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs/{id}/target-rows", s.listTargetRows)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/runs/{id}/projects/{project}/opening", s.projectOpening)
		// Any authenticated caller may resolve a legacy id (9.49.5); the tenant middleware has
		// already refused requests without a principal.
		r.Get("/migration/xref", s.lookupXref)
		r.With(authz.RequireDuty(DutyMigrationRun)).Post("/migration/parallel-runs", s.startParallelRun)
		r.With(authz.RequireDuty(DutyMigrationRun)).Post("/migration/parallel-runs/replay", s.replayParallelRun)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/parallel-runs", s.listParallelRuns)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/coexistence", s.listCoexistence)
		r.With(authz.RequireDuty(DutyCutoverManager)).Put("/migration/coexistence/{system}/{company}/{entity}", s.setCoexistence)
		r.With(authz.RequireDuty(DutyMigrationView)).Get("/migration/parallel-runs/{id}", s.getParallelRun)
		r.With(authz.RequireDuty(DutyMigrationReview)).Post("/migration/parallel-runs/{id}/variances/{v}/explain", s.explainVariance)
		r.With(authz.RequireDuty(DutyMigrationReview)).Post("/migration/parallel-runs/{id}/sign-off", s.signOffParallelRun)
		r.With(authz.RequireDuty(DutyCutoverManager)).Post("/cutover/plans/generate", s.generatePlan)
		r.With(authz.RequireDuty(DutyCutoverManager)).Get("/cutover/plans", s.listPlans)
		r.With(authz.RequireDuty(DutyCutoverManager)).Get("/cutover/plans/{id}", s.getPlan)
		r.With(authz.RequireDuty(DutyCutoverManager)).Get("/cutover/plans/{id}/opening-targets", s.listOpeningTargets)
		r.With(authz.RequireDuty(DutyCutoverManager)).Put("/cutover/plans/{id}/opening-targets/{system}/{company}", s.putOpeningTarget)
		r.With(authz.RequireDuty(DutyOpeningTargetRelease)).Post("/migration/opening-targets/{system}/{company}/release", s.releaseOpeningTarget)
		r.With(authz.RequireDuty(DutyStepOwner)).Post("/cutover/plans/{id}/steps/{seq}/start", s.startStep)
		r.With(authz.RequireDuty(DutyStepOwner)).Post("/cutover/plans/{id}/steps/{seq}/complete", s.completeStep)
		r.With(authz.RequireDuty(DutyCutoverManager)).Get("/cutover/plans/{id}/go-no-go", s.readiness)
		r.With(authz.RequireDuty(DutyCutoverManager)).Post("/cutover/plans/{id}/go-no-go", s.recordDecision)
		r.With(authz.RequireDuty(DutyCutoverManager)).Post("/cutover/plans/{id}/rollback", s.rollback)
		r.With(authz.RequireDuty(DutyCutoverManager)).Get("/cutover/plans/{id}/rekeying/{file}", s.rekeyFile)
		r.With(authz.RequireDuty(DutyCutoverManager)).Post("/cutover/plans/{id}/rollback/compensations", s.postCompensations)
		r.With(authz.RequireDuty(DutyCutoverManager)).Get("/cutover/plans/{id}/rollback/compensations", s.getCompensations)
		r.With(authz.RequireDuty(DutyCutoverManager)).Get("/cutover/plans/{id}/export/{file}", s.exportFile)
	})
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
	}
	return p, ok
}

// approverID is the person an approval is recorded under (CUT-1605): the signed-in user. A call made
// with a service token carries the calling service's duties, not the user's, and a background service
// principal is no person, so neither approves anything: the empty id is refused by the domain.
func approverID(p tenant.Principal) string {
	if p.Service != "" || tenant.IsServiceActor(p.UserID) {
		return ""
	}
	return p.UserID
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
	case errors.Is(err, store.ErrImportSourceTaken):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeImportSourceTaken, err.Error()))
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
