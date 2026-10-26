package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	readyTimeout      = 500 * time.Millisecond
	runTimeout        = 35 * time.Minute
	idempotencyHeader = "Idempotency-Key"
	dutyRun           = "migration.run"
	runsPath          = "/v1/integration/migration-runs"
	validationDetail  = "request failed validation"
	CodeNotConfigured = "MIG-BACKEND-NOT-CONFIGURED"
	CodeDeferred      = "MIG-PARALLEL-RUN-DEFERRED"
	codeKeyReuse      = "idempotency_key_reuse"
)

var (
	sourceSystems    = []string{"ax2012", "d365fo", "sap_ecc", "sap_s4"}
	modes            = []string{"rehearsal", "cutover", "delta"}
	ErrNotConfigured = errors.New("migration backend is not configured")
)

type Backend struct {
	Extractor domain.Extractor
	Loader    domain.TargetLoader
	Reader    domain.TargetReader
}

type BackendFactory func(ctx context.Context, sourceSystem string) (Backend, error)

func NotConfigured(context.Context, string) (Backend, error) { return Backend{}, ErrNotConfigured }

type API struct {
	store    store.Store
	backends BackendFactory
	runs     atomic.Int64
	failed   atomic.Int64
	loaded   atomic.Int64
	rejected atomic.Int64
}

type createRunRequest struct {
	SourceSystem string          `json:"sourceSystem"`
	Mode         string          `json:"mode"`
	DryRun       bool            `json:"dryRun"`
	Mapping      json.RawMessage `json:"mapping"`
}

type runResponse struct {
	JobID                      string  `json:"jobId"`
	StatusURL                  string  `json:"statusUrl"`
	EstimatedCompletionSeconds int     `json:"estimatedCompletionSeconds"`
	Run                        runView `json:"run"`
}

type runView struct {
	ID             string                  `json:"id"`
	SourceSystem   string                  `json:"sourceSystem"`
	Mode           string                  `json:"mode"`
	Entity         string                  `json:"entity"`
	Status         string                  `json:"status"`
	DryRun         bool                    `json:"dryRun"`
	MappingVersion int                     `json:"mappingVersion"`
	ParentRunID    string                  `json:"parentRunId,omitempty"`
	Counts         counts                  `json:"counts"`
	Rejects        []domain.RowReject      `json:"rejects"`
	Warnings       []domain.RowWarning     `json:"warnings"`
	Absent         []string                `json:"absent"`
	Reconciliation *domain.ReconcileReport `json:"reconciliation,omitempty"`
	CreatedAt      time.Time               `json:"createdAt"`
}

type counts struct {
	Extracted int `json:"extracted"`
	Mapped    int `json:"mapped"`
	Created   int `json:"created"`
	Updated   int `json:"updated"`
	Unchanged int `json:"unchanged"`
	Rejected  int `json:"rejected"`
	Warned    int `json:"warned"`
}

func New(s store.Store, backends BackendFactory) *API { return &API{store: s, backends: backends} }

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyRun)).Post(runsPath, a.createRun)
	r.With(authz.RequireDuty(dutyRun)).Get(runsPath+"/{id}", a.getRun)
	r.With(authz.RequireDuty(dutyRun)).Post(runsPath+"/{id}:rerun", a.rerun)
	r.With(authz.RequireDuty(dutyRun)).Post("/v1/integration/parallel-run-diffs", a.parallelRunDiffs)
}

func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		for _, m := range []struct {
			name, help string
			value      int64
		}{
			{"migration_runs_created_total", "Migration runs recorded.", a.runs.Load()},
			{"migration_runs_failed_total", "Migration runs that failed before or while executing.", a.failed.Load()},
			{"migration_rows_loaded_total", "Rows created or updated in target contexts.", a.loaded.Load()},
			{"migration_rows_rejected_total", "Rows sent to the exception queue.", a.rejected.Load()},
		} {
			fmt.Fprintf(w, "# HELP %[1]s %[2]s\n# TYPE %[1]s counter\n%[1]s %[3]d\n", m.name, m.help, m.value)
		}
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

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req createRunRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := validateRun(req); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = fields
		httpx.Fail(w, r, p)
		return
	}
	mapping, err := domain.ParseMapping(req.Mapping)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	if failures := mapping.RunTests(); len(failures) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeMapTestFail, fmt.Sprintf("%d mapping tests failed for %s.", len(failures), mapping.Entity))
		p.Errors = make(map[string]string, len(failures))
		for _, f := range failures {
			p.Errors[f.Name] = f.Reason
		}
		a.logFailure(r, "mapping tests failed", p)
		httpx.Fail(w, r, p)
		return
	}
	a.execute(w, r, store.Run{SourceSystem: req.SourceSystem, Mode: req.Mode, Entity: mapping.Entity, DryRun: req.DryRun, MappingVersion: mapping.Version, Mapping: req.Mapping}, mapping, nil)
}

func (a *API) rerun(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	prev, err := a.store.GetRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "load run for rerun failed", err)
		httpx.Fail(w, r, err)
		return
	}
	mapping, err := domain.ParseMapping(prev.Mapping)
	if err != nil {
		a.logFailure(r, "stored mapping no longer parses", err)
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	next := store.Run{SourceSystem: prev.SourceSystem, Mode: prev.Mode, Entity: prev.Entity, DryRun: prev.DryRun, MappingVersion: prev.MappingVersion, Mapping: prev.Mapping, ParentRunID: prev.ID}
	a.execute(w, r, next, mapping, &prev.Report)
}

func (a *API) execute(w http.ResponseWriter, r *http.Request, run store.Run, mapping domain.Mapping, prev *domain.Report) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), runTimeout)
	defer cancel()
	key := r.Header.Get(idempotencyHeader)
	if key != "" {
		existing, found, err := a.store.RunByKey(ctx, key)
		if err != nil {
			a.logFailure(r, "idempotency lookup failed", err)
			httpx.Fail(w, r, err)
			return
		}
		if found {
			if !sameRequest(existing, run) {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeKeyReuse, "Idempotency-Key was already used with a different request"))
				return
			}
			respond(w, existing)
			return
		}
	}
	backend, err := a.backends(ctx, run.SourceSystem)
	if err != nil {
		a.failed.Add(1)
		a.logFailure(r, "migration backend unavailable", err)
		if errors.Is(err, ErrNotConfigured) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotImplemented, CodeNotConfigured, "extract and load backends are not configured for this deployment"))
			return
		}
		httpx.Fail(w, r, err)
		return
	}
	started := time.Now()
	slog.InfoContext(ctx, "migration run started", "correlationId", middleware.GetReqID(ctx), "entity", run.Entity, "sourceSystem", run.SourceSystem, "mode", run.Mode, "dryRun", run.DryRun, "parentRunId", run.ParentRunID)
	pipeline := domain.Pipeline{Extractor: backend.Extractor, Loader: backend.Loader, Reader: backend.Reader, Xref: a.store.Xref(), IDGen: ids.New}
	spec := domain.RunSpec{SourceSystem: run.SourceSystem, Entity: run.Entity, Mapping: mapping, DryRun: run.DryRun}
	if prev != nil {
		run.Report, err = pipeline.Rerun(ctx, spec, *prev)
	} else {
		run.Report, err = pipeline.Run(ctx, spec)
	}
	if err != nil {
		a.failed.Add(1)
		a.logFailure(r, "migration run aborted", err)
		httpx.Fail(w, r, err)
		return
	}
	saved, err := a.store.SaveRun(ctx, run, key)
	if err != nil {
		a.failed.Add(1)
		a.logFailure(r, "saving migration run failed", err)
		httpx.Fail(w, r, err)
		return
	}
	slog.InfoContext(ctx, "migration run finished", "correlationId", middleware.GetReqID(ctx), "runId", saved.ID, "entity", run.Entity, "sourceSystem", run.SourceSystem, "mode", run.Mode,
		"dryRun", run.DryRun, "status", run.Report.Status, "extracted", run.Report.Extracted, "created", run.Report.Created, "updated", run.Report.Updated,
		"unchanged", run.Report.Unchanged, "rejected", len(run.Report.Rejects), "reconciliationOK", run.Report.Reconciliation == nil || run.Report.Reconciliation.OK(), "durationMs", time.Since(started).Milliseconds())
	a.runs.Add(1)
	a.loaded.Add(int64(run.Report.Created + run.Report.Updated))
	a.rejected.Add(int64(len(run.Report.Rejects)))
	respond(w, saved)
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	run, err := a.store.GetRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "get migration run failed", err)
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view(run))
}

func (a *API) parallelRunDiffs(w http.ResponseWriter, r *http.Request) {
	httpx.Fail(w, r, httpx.NewProblem(http.StatusNotImplemented, CodeDeferred, "parallel-run diffs belong to wave 49"))
}

func (a *API) logFailure(r *http.Request, msg string, err error) {
	slog.Error(msg, "err", err, "correlationId", middleware.GetReqID(r.Context()))
}

func respond(w http.ResponseWriter, run store.Run) {
	url := runsPath + "/" + run.ID
	w.Header().Set("Location", url)
	httpx.JSON(w, http.StatusAccepted, runResponse{JobID: run.ID, StatusURL: url, Run: view(run)})
}

func view(run store.Run) runView {
	rep := run.Report
	v := runView{
		ID: run.ID, SourceSystem: run.SourceSystem, Mode: run.Mode, Entity: run.Entity, Status: rep.Status, DryRun: run.DryRun,
		MappingVersion: run.MappingVersion, ParentRunID: run.ParentRunID, Rejects: rep.Rejects, Warnings: rep.Warnings, Absent: rep.Absent,
		Reconciliation: rep.Reconciliation, CreatedAt: run.CreatedAt,
		Counts: counts{Extracted: rep.Extracted, Mapped: rep.Mapped, Created: rep.Created, Updated: rep.Updated, Unchanged: rep.Unchanged, Rejected: len(rep.Rejects), Warned: len(rep.Warnings)},
	}
	if v.Rejects == nil {
		v.Rejects = []domain.RowReject{}
	}
	if v.Warnings == nil {
		v.Warnings = []domain.RowWarning{}
	}
	if v.Absent == nil {
		v.Absent = []string{}
	}
	return v
}

func sameRequest(a, b store.Run) bool {
	return a.SourceSystem == b.SourceSystem && a.Mode == b.Mode && a.Entity == b.Entity && a.DryRun == b.DryRun && a.ParentRunID == b.ParentRunID
}

func validateRun(req createRunRequest) map[string]string {
	fields := map[string]string{}
	if !slices.Contains(sourceSystems, req.SourceSystem) {
		fields["sourceSystem"] = "must be one of ax2012, d365fo, sap_ecc, sap_s4"
	}
	if !slices.Contains(modes, req.Mode) {
		fields["mode"] = "must be one of rehearsal, cutover, delta"
	}
	if len(req.Mapping) == 0 {
		fields["mapping"] = "is required"
	}
	return fields
}

func problemFrom(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
	}
	return err
}

func tenantWide(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	if p.LegalEntityID != "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "a legal-entity-scoped caller cannot run migrations"))
		return false
	}
	return true
}
