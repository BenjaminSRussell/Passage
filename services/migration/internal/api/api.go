package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/connector"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	readyTimeout        = 500 * time.Millisecond
	runTimeout          = 35 * time.Minute
	idempotencyHeader   = "Idempotency-Key"
	defaultPage         = 50
	dutyRun             = "migration.run"
	dutySpecWrite       = "migration.spec.write"
	runsPath            = "/v1/integration/migration-runs"
	specsPath           = "/v1/integration/mapping-specs"
	validationDetail    = "request failed validation"
	CodeNotConfigured   = "MIG-BACKEND-NOT-CONFIGURED"
	CodeDeferred        = "MIG-PARALLEL-RUN-DEFERRED"
	CodeSpecUnknown     = "MIG-SPEC-UNKNOWN"
	CodeTemplateUnknown = "MIG-TEMPLATE-UNKNOWN"
	CodeProjectUnknown  = "MIG-PROJECT-UNKNOWN"
	CodeProjectType     = "MIG-PROJECT-NOT-MIGRATE"
	codeKeyReuse        = "idempotency_key_reuse"
)

var (
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
	packages PackageFactory
	keys     *fieldcrypt.Keys
	prober   Prober
	env      *connector.Env
	targets  TargetFactory
	runs     atomic.Int64
	failed   atomic.Int64
	loaded   atomic.Int64
	rejected atomic.Int64
}

type createRunRequest struct {
	SourceSystem  string          `json:"sourceSystem"`
	Mode          string          `json:"mode"`
	DryRun        bool            `json:"dryRun"`
	Mapping       json.RawMessage `json:"mapping"`
	MappingSpecID string          `json:"mappingSpecId"`
	// DataTemplateID applies a published data template's defaults before mapping (FR-10.18).
	DataTemplateID string `json:"dataTemplateId"`
	// ProjectID ties the run to a migrate project (BR-10.2: one active run per project).
	ProjectID string `json:"projectId"`
}

type publishSpecRequest struct {
	Mapping json.RawMessage `json:"mapping"`
}

type specView struct {
	ID         string          `json:"id"`
	Entity     string          `json:"entity"`
	Version    int             `json:"version"`
	Checksum   string          `json:"checksum"`
	FieldCount int             `json:"fieldCount"`
	TestCount  int             `json:"testCount"`
	CreatedAt  time.Time       `json:"createdAt"`
	Mapping    json.RawMessage `json:"mapping,omitempty"`
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
	MappingSpecID  string                  `json:"mappingSpecId,omitempty"`
	DataTemplateID string                  `json:"dataTemplateId,omitempty"`
	ProjectID      string                  `json:"projectId,omitempty"`
	BindingID      string                  `json:"bindingId,omitempty"`
	Watermark      string                  `json:"watermark,omitempty"`
	ParentRunID    string                  `json:"parentRunId,omitempty"`
	Counts         counts                  `json:"counts"`
	Rejects        []domain.RowReject      `json:"rejects"`
	Warnings       []domain.RowWarning     `json:"warnings"`
	Absent         []string                `json:"absent"`
	Reconciliation *domain.ReconcileReport `json:"reconciliation,omitempty"`
	CreatedAt      time.Time               `json:"createdAt"`
}

type counts struct {
	Extracted  int `json:"extracted"`
	Mapped     int `json:"mapped"`
	Created    int `json:"created"`
	Updated    int `json:"updated"`
	Unchanged  int `json:"unchanged"`
	Templated  int `json:"templated"`
	Conflicted int `json:"conflicted"`
	Rejected   int `json:"rejected"`
	Warned     int `json:"warned"`
}

func New(s store.Store, backends BackendFactory) *API {
	return &API{store: s, backends: backends, packages: PackagesNotConfigured}
}

func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyRun)).Post(runsPath, a.createRun)
	r.With(authz.RequireDuty(dutyRun)).Get(runsPath, a.listRuns)
	r.With(authz.RequireDuty(dutySpecWrite)).Post(specsPath, a.publishSpec)
	r.With(authz.RequireDuty(dutyRun)).Get(specsPath, a.listSpecs)
	r.With(authz.RequireDuty(dutyRun)).Get(specsPath+"/{id}", a.getSpec)
	r.With(authz.RequireDuty(dutyRun)).Get(runsPath+"/{id}", a.getRun)
	r.With(authz.RequireDuty(dutyRun)).Post(runsPath+"/{id}:rerun", a.rerun)
	r.With(authz.RequireDuty(dutyRun)).Post("/v1/integration/parallel-run-diffs", a.parallelRunDiffs)
	r.With(authz.RequireDuty(dutyRun)).Get(runsPath+"/{id}/ledger", a.runLedger)
	a.registerTemplates(r)
	a.registerXref(r)
	a.registerPackages(r)
	a.registerProjects(r)
	a.registerConnections(r)
	a.registerBindings(r)
	a.registerSync(r)
	a.registerOutbound(r)
	a.registerLookup(r)
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
		fail(w, r, err)
		return
	}
	if fields := validateRun(req); len(fields) > 0 {
		p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
		p.Errors = fields
		fail(w, r, p)
		return
	}
	raw, specID := req.Mapping, ""
	if req.MappingSpecID != "" {
		spec, err := a.store.GetMappingSpec(r.Context(), req.MappingSpecID)
		if err != nil {
			var p *httpx.Problem
			if errors.As(err, &p) && p.Status == http.StatusNotFound {
				p = httpx.NewProblem(http.StatusUnprocessableEntity, CodeSpecUnknown, "mappingSpecId names no published mapping spec")
				p.Errors = map[string]string{"mappingSpecId": "no published mapping spec has this id"}
				err = p
			} else {
				a.logFailure(r, "load mapping spec for run failed", err)
			}
			fail(w, r, err)
			return
		}
		raw, specID = spec.Spec, spec.ID
	}
	mapping, err := domain.ParseMapping(raw)
	if err != nil {
		fail(w, r, problemFrom(err))
		return
	}
	if failures := mapping.RunTests(); len(failures) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeMapTestFail, fmt.Sprintf("%d mapping tests failed for %s.", len(failures), mapping.Entity))
		p.Errors = make(map[string]string, len(failures))
		for _, f := range failures {
			p.Errors[f.Name] = f.Reason
		}
		a.logFailure(r, "mapping tests failed", p)
		fail(w, r, p)
		return
	}
	var tmpl *domain.DataTemplate
	templateID := ""
	if req.DataTemplateID != "" {
		stored, err := a.store.GetDataTemplate(r.Context(), req.DataTemplateID)
		if err != nil {
			var p *httpx.Problem
			if errors.As(err, &p) && p.Status == http.StatusNotFound {
				p = httpx.NewProblem(http.StatusUnprocessableEntity, CodeTemplateUnknown, "dataTemplateId names no published data template")
				p.Errors = map[string]string{"dataTemplateId": "no published data template has this id"}
				err = p
			} else {
				a.logFailure(r, "load data template for run failed", err)
			}
			fail(w, r, err)
			return
		}
		t, err := templateFor(stored, mapping)
		if err != nil {
			fail(w, r, problemFrom(err))
			return
		}
		tmpl, templateID = &t, stored.ID
	}
	if req.ProjectID != "" {
		if !a.checkRunProject(w, r, req) {
			return
		}
	}
	a.execute(w, r, store.Run{SourceSystem: req.SourceSystem, Mode: req.Mode, Entity: mapping.Entity, DryRun: req.DryRun, MappingVersion: mapping.Version, Mapping: raw, MappingSpecID: specID, DataTemplateID: templateID, ProjectID: req.ProjectID}, mapping, tmpl, nil)
}

// checkRunProject answers false after writing the problem when the project cannot take this run: unknown (422), not
// a migrate project (422) or reading from another source system (400). An archived project is refused by the claim.
func (a *API) checkRunProject(w http.ResponseWriter, r *http.Request, req createRunRequest) bool {
	pr, err := a.store.GetProject(r.Context(), req.ProjectID)
	if err != nil {
		var p *httpx.Problem
		if errors.As(err, &p) && p.Status == http.StatusNotFound {
			p = httpx.NewProblem(http.StatusUnprocessableEntity, CodeProjectUnknown, "projectId names no migration project")
			p.Errors = map[string]string{"projectId": "no migration project has this id"}
			err = p
		} else {
			a.logFailure(r, "load project for run failed", err)
		}
		fail(w, r, err)
		return false
	}
	if pr.Type != domain.ProjectMigrate {
		fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeProjectType, "only a migrate project takes extract-and-load runs"))
		return false
	}
	if pr.SourceSystem != req.SourceSystem {
		fail(w, r, validation(map[string]string{"sourceSystem": "must be the project's source system, " + pr.SourceSystem}))
		return false
	}
	return true
}

func (a *API) rerun(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	prev, err := a.store.GetRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "load run for rerun failed", err)
		fail(w, r, err)
		return
	}
	mapping, err := domain.ParseMapping(prev.Mapping)
	if err != nil {
		a.logFailure(r, "stored mapping no longer parses", err)
		fail(w, r, problemFrom(err))
		return
	}
	var tmpl *domain.DataTemplate
	if prev.DataTemplateID != "" {
		stored, err := a.store.GetDataTemplate(r.Context(), prev.DataTemplateID)
		if err == nil {
			var t domain.DataTemplate
			if t, err = templateFor(stored, mapping); err == nil {
				tmpl = &t
			}
		}
		if err != nil {
			a.logFailure(r, "stored data template no longer applies", err)
			fail(w, r, problemFrom(err))
			return
		}
	}
	next := store.Run{SourceSystem: prev.SourceSystem, Mode: prev.Mode, Entity: prev.Entity, DryRun: prev.DryRun, MappingVersion: prev.MappingVersion, Mapping: prev.Mapping, MappingSpecID: prev.MappingSpecID, DataTemplateID: prev.DataTemplateID, ProjectID: prev.ProjectID, ParentRunID: prev.ID}
	a.execute(w, r, next, mapping, tmpl, &prev.Report)
}

func (a *API) execute(w http.ResponseWriter, r *http.Request, run store.Run, mapping domain.Mapping, tmpl *domain.DataTemplate, prev *domain.Report) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), runTimeout)
	defer cancel()
	key := r.Header.Get(idempotencyHeader)
	if key != "" {
		existing, found, err := a.store.RunByKey(ctx, key)
		if err != nil {
			a.logFailure(r, "idempotency lookup failed", err)
			fail(w, r, err)
			return
		}
		if found {
			if !sameRequest(existing, run) {
				fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, codeKeyReuse, "Idempotency-Key was already used with a different request"))
				return
			}
			respond(w, existing)
			return
		}
	}
	backend, err := a.backend(ctx, run)
	if err != nil {
		a.failed.Add(1)
		a.logFailure(r, "migration backend unavailable", err)
		if errors.Is(err, ErrNotConfigured) {
			fail(w, r, httpx.NewProblem(http.StatusNotImplemented, CodeNotConfigured, "extract and load backends are not configured for this deployment"))
			return
		}
		fail(w, r, err)
		return
	}
	if p := unknownTargets(backend.Loader, mapping); p != nil {
		fail(w, r, p)
		return
	}
	if run.ProjectID != "" {
		// The run's id is fixed now so the claim names it (the 409 for a second start carries it) and only this
		// run can release it. The deferred release runs on every return and on a panic, on a context the run
		// deadline cannot cancel; a process that dies mid-run leaves a claim that lapses after the abandonment window.
		run.ID = ids.New()
		if _, err := a.store.BeginProjectRun(ctx, run.ProjectID, run.ID); err != nil {
			var p *httpx.Problem
			if !errors.As(err, &p) { // a refusal (404, 409) is the caller's to handle, not a service failure
				a.logFailure(r, "claiming project for run failed", err)
			}
			fail(w, r, err)
			return
		}
		defer func() {
			if err := a.store.EndProjectRun(context.WithoutCancel(ctx), run.ProjectID, run.ID); err != nil {
				a.logFailure(r, "releasing project claim failed", err)
			}
		}()
	}
	started := time.Now()
	slog.InfoContext(ctx, "migration run started", "correlationId", middleware.GetReqID(ctx), "entity", run.Entity, "sourceSystem", run.SourceSystem, "mode", run.Mode, "dryRun", run.DryRun, "parentRunId", run.ParentRunID)
	pipeline := domain.Pipeline{Extractor: backend.Extractor, Loader: backend.Loader, Reader: backend.Reader, Xref: a.store.Xref(), IDGen: ids.New}
	spec := domain.RunSpec{SourceSystem: run.SourceSystem, Entity: run.Entity, Mapping: mapping, DryRun: run.DryRun, Template: tmpl}
	if prev != nil {
		run.Report, err = pipeline.Rerun(ctx, spec, *prev)
	} else {
		run.Report, err = pipeline.Run(ctx, spec)
	}
	if err != nil {
		a.failed.Add(1)
		a.logFailure(r, "migration run aborted", err)
		fail(w, r, err)
		return
	}
	saved, err := a.store.SaveRun(ctx, run, key)
	if err != nil {
		a.failed.Add(1)
		a.logFailure(r, "saving migration run failed", err)
		fail(w, r, err)
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
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view(run))
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	q := r.URL.Query()
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	var after *store.RunCursor
	if v := q.Get("cursor"); v != "" {
		if after, ok = runCursor(v); !ok {
			fail(w, r, cursorProblem())
			return
		}
	}
	if st := q.Get("status"); st != "" && !slices.Contains([]string{domain.StatusSucceeded, domain.StatusPartial, domain.StatusFailed}, st) {
		fail(w, r, validation(map[string]string{"status": "must be one of succeeded, partial, failed"}))
		return
	}
	if pid := q.Get("projectId"); pid != "" && !ids.Valid(pid) {
		fail(w, r, validation(map[string]string{"projectId": "must be a UUID"}))
		return
	}
	if bid := q.Get("bindingId"); bid != "" && !ids.Valid(bid) {
		fail(w, r, validation(map[string]string{"bindingId": "must be a UUID"}))
		return
	}
	runs, err := a.store.ListRuns(r.Context(), store.RunFilter{Entity: q.Get("entity"), Status: q.Get("status"), ProjectID: q.Get("projectId"), BindingID: q.Get("bindingId"), After: after, Limit: limit + 1})
	if err != nil {
		a.logFailure(r, "list migration runs failed", err)
		fail(w, r, err)
		return
	}
	runs, more := trimPage(runs, limit)
	items := make([]runView, 0, len(runs))
	for _, run := range runs {
		items = append(items, view(run))
	}
	body := map[string]any{"items": items}
	if more {
		body["nextCursor"] = encodePageCursor(store.RunCursor{CreatedAt: runs[limit-1].CreatedAt, ID: runs[limit-1].ID})
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) publishSpec(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req publishSpecRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if len(req.Mapping) == 0 {
		fail(w, r, validation(map[string]string{"mapping": "is required"}))
		return
	}
	mapping, err := domain.ParseMapping(req.Mapping)
	if err != nil {
		fail(w, r, problemFrom(err))
		return
	}
	// A published spec carries at least one test case (PRD MappingSpec.tests: "at least 1 case").
	if len(mapping.Tests) == 0 {
		fail(w, r, problemFrom(&domain.Error{Code: domain.CodeMapInvalid, Detail: "a published mapping spec needs at least one test case"}))
		return
	}
	if failures := mapping.RunTests(); len(failures) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeMapTestFail, fmt.Sprintf("%d mapping tests failed for %s.", len(failures), mapping.Entity))
		p.Errors = make(map[string]string, len(failures))
		for _, f := range failures {
			p.Errors[f.Name] = f.Reason
		}
		fail(w, r, p)
		return
	}
	canonical, sum, err := canonicalize(req.Mapping)
	if err != nil {
		fail(w, r, problemFrom(&domain.Error{Code: domain.CodeMapInvalid, Detail: "mapping JSON: " + err.Error()}))
		return
	}
	saved, created, err := a.store.SaveMappingSpec(r.Context(), store.MappingSpec{
		Entity: mapping.Entity, Version: mapping.Version, Checksum: sum, FieldCount: len(mapping.Fields), TestCount: len(mapping.Tests), Spec: canonical,
	}, r.Header.Get(idempotencyHeader))
	if err != nil {
		a.logFailure(r, "publish mapping spec failed", err)
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", specsPath+"/"+saved.ID)
	}
	httpx.JSON(w, status, specToView(saved, false))
}

func (a *API) listSpecs(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	var after *store.SpecCursor
	if v := r.URL.Query().Get("cursor"); v != "" {
		if after, ok = specCursor(v); !ok {
			fail(w, r, cursorProblem())
			return
		}
	}
	specs, err := a.store.ListMappingSpecs(r.Context(), r.URL.Query().Get("entity"), after, limit+1)
	if err != nil {
		a.logFailure(r, "list mapping specs failed", err)
		fail(w, r, err)
		return
	}
	specs, more := trimPage(specs, limit)
	items := make([]specView, 0, len(specs))
	for _, s := range specs {
		items = append(items, specToView(s, false))
	}
	body := map[string]any{"items": items}
	if more {
		body["nextCursor"] = encodePageCursor(store.SpecCursor{Entity: specs[limit-1].Entity, Version: specs[limit-1].Version})
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) getSpec(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	spec, err := a.store.GetMappingSpec(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "get mapping spec failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, specToView(spec, true))
}

func specToView(s store.MappingSpec, withMapping bool) specView {
	v := specView{ID: s.ID, Entity: s.Entity, Version: s.Version, Checksum: s.Checksum, FieldCount: s.FieldCount, TestCount: s.TestCount, CreatedAt: s.CreatedAt}
	if withMapping {
		v.Mapping = s.Spec
	}
	return v
}

// canonicalize re-encodes the mapping with sorted keys so formatting and key order never change the checksum.
func canonicalize(raw json.RawMessage) (json.RawMessage, string, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, "", err
	}
	out, err := json.Marshal(v)
	if err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(out)
	return out, hex.EncodeToString(sum[:]), nil
}

func parseLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 200 {
		fail(w, r, validation(map[string]string{"limit": "must be an integer from 1 to 200"}))
		return 0, false
	}
	return n, true
}

func validation(fields map[string]string) error {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	return p
}

func (a *API) parallelRunDiffs(w http.ResponseWriter, r *http.Request) {
	fail(w, r, httpx.NewProblem(http.StatusNotImplemented, CodeDeferred, "parallel-run diffs belong to wave 49"))
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
		MappingVersion: run.MappingVersion, MappingSpecID: run.MappingSpecID, DataTemplateID: run.DataTemplateID, ProjectID: run.ProjectID, BindingID: run.BindingID, Watermark: rep.Watermark, ParentRunID: run.ParentRunID, Rejects: rep.Rejects, Warnings: rep.Warnings, Absent: rep.Absent,
		Reconciliation: rep.Reconciliation, CreatedAt: run.CreatedAt,
		Counts: counts{Extracted: rep.Extracted, Mapped: rep.Mapped, Created: rep.Created, Updated: rep.Updated, Unchanged: rep.Unchanged, Templated: rep.Templated, Conflicted: rep.Conflicted, Rejected: len(rep.Rejects), Warned: len(rep.Warnings)},
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
	return a.SourceSystem == b.SourceSystem && a.Mode == b.Mode && a.Entity == b.Entity && a.DryRun == b.DryRun && a.ParentRunID == b.ParentRunID &&
		a.MappingVersion == b.MappingVersion && a.MappingSpecID == b.MappingSpecID && a.DataTemplateID == b.DataTemplateID && a.ProjectID == b.ProjectID
}

func validateRun(req createRunRequest) map[string]string {
	fields := map[string]string{}
	if !domain.ValidSourceSystem(req.SourceSystem) {
		fields["sourceSystem"] = domain.SourceSystemHint
	}
	if !slices.Contains(modes, req.Mode) {
		fields["mode"] = "must be one of rehearsal, cutover, delta"
	}
	switch {
	case len(req.Mapping) == 0 && req.MappingSpecID == "":
		fields["mapping"] = "is required unless mappingSpecId is given"
	case len(req.Mapping) > 0 && req.MappingSpecID != "":
		fields["mapping"] = "give either mapping or mappingSpecId, not both"
	case req.MappingSpecID != "" && !ids.Valid(req.MappingSpecID):
		fields["mappingSpecId"] = "must be a UUID"
	}
	if req.DataTemplateID != "" && !ids.Valid(req.DataTemplateID) {
		fields["dataTemplateId"] = "must be a UUID"
	}
	if req.ProjectID != "" && !ids.Valid(req.ProjectID) {
		fields["projectId"] = "must be a UUID"
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
		fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	if !tenant.IsTenantAdmin(p) {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "migrations need the "+tenant.DutyTenantAdmin+" duty and a request without a legal-entity scope"))
		return false
	}
	return true
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
	// Unresolved lists, for MIG-XREF-UNRESOLVED, every lookup that found no legacy key.
	Unresolved []domain.UnresolvedRef `json:"unresolved,omitempty"`
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	var p *httpx.Problem
	if !errors.As(err, &p) {
		slog.Error("unhandled error", "err", err, "path", r.URL.Path, "correlationId", middleware.GetReqID(r.Context()))
		p = httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "internal error")
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(problemBody{
		Type: p.Type, Title: p.Title, Status: p.Status, Detail: p.Detail, Instance: r.URL.Path,
		Code: p.Code, ServiceCode: p.Code, TraceID: middleware.GetReqID(r.Context()), Errors: p.Errors,
	})
}
