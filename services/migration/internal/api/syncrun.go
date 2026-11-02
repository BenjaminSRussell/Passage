package api

import (
	"context"
	"errors"
	"fmt"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
	"github.com/BenjaminSRussell/passage/services/migration/internal/syncer"
)

// ExtractSince lets the pipeline read a delta through the classified-failure wrapper.
func (s sourceFailures) ExtractSince(ctx context.Context, entity, since string) ([]domain.Record, string, error) {
	d, ok := s.Extractor.(domain.DeltaExtractor)
	if !ok {
		rows, err := s.Extract(ctx, entity)
		return rows, since, err
	}
	rows, wm, err := d.ExtractSince(ctx, entity, since)
	if err == nil {
		return rows, wm, nil
	}
	return nil, since, s.classify(err)
}

// SyncRunner returns the Runner the scheduler uses: it runs the same extractor, mapping, pipeline and loader as
// POST /migration-runs, in mode delta, since the binding's watermark.
func (a *API) SyncRunner() syncer.Runner { return syncRunner{a} }

type syncRunner struct{ a *API }

func (r syncRunner) Run(ctx context.Context, st store.SyncState, runID string) (syncer.Result, error) {
	a := r.a
	if !a.live() {
		return syncer.Result{}, syncer.ErrNotConfigured
	}
	binding, err := a.store.GetEntityBinding(ctx, st.BindingID)
	if err != nil {
		return syncer.Result{}, err
	}
	project, err := a.store.GetProject(ctx, st.ProjectID)
	if err != nil {
		return syncer.Result{}, err
	}
	raw, specID, err := a.syncMapping(ctx, binding)
	if err != nil {
		return syncer.Result{}, err
	}
	mapping, err := domain.ParseMapping(raw)
	if err != nil {
		return syncer.Result{}, fmt.Errorf("mapping spec of %s does not parse: %w", binding.Entity, err)
	}
	if failures := mapping.RunTests(); len(failures) > 0 {
		return syncer.Result{}, fmt.Errorf("%d mapping tests failed for %s", len(failures), mapping.Entity)
	}
	run := store.Run{ID: runID, SourceSystem: project.SourceSystem, Mode: "delta", Entity: binding.Entity, MappingVersion: mapping.Version, Mapping: raw,
		MappingSpecID: specID, ProjectID: project.ID, BindingID: binding.ID}
	backend, err := a.backend(ctx, run)
	if err != nil {
		return syncer.Result{}, err
	}
	if p := unknownTargets(backend.Loader, mapping); p != nil {
		return syncer.Result{}, p
	}
	kept, err := a.store.KeptConflicts(ctx, binding.ID)
	if err != nil {
		return syncer.Result{}, err
	}
	// A watermark read under another version of the binding (another table, query or cursor) means nothing: read it all.
	watermark := ""
	if st.WatermarkBindingVersion != nil && *st.WatermarkBindingVersion == binding.Version && binding.CursorField != "" {
		watermark = st.Watermark
	}
	pipeline := domain.Pipeline{Extractor: backend.Extractor, Loader: backend.Loader, Reader: backend.Reader, Xref: a.store.Xref(), IDGen: ids.New}
	run.Report, err = pipeline.Run(ctx, domain.RunSpec{SourceSystem: run.SourceSystem, Entity: run.Entity, Mapping: mapping,
		Since: domain.DeltaSince(binding.CursorType, watermark), HoldExisting: st.SourceOfTruth == domain.SourcePassage, Acknowledged: kept})
	// The extractor read from a little before the watermark, so what it returns can be smaller than what is stored.
	if run.Report.Watermark != "" || watermark != "" {
		run.Report.Watermark = domain.MaxCursor(binding.CursorType, watermark, run.Report.Watermark)
	}
	if err != nil {
		a.failed.Add(1)
		// Keep the failed attempt visible in the run list; the scheduler records the failure and keeps the watermark.
		run.Report.Status = domain.StatusFailed
		run.Report.Rejects = append(run.Report.Rejects, domain.RowReject{SourceKey: "(run)", Rejects: []domain.Reject{{Code: "MIG-SYNC-RUN-FAILED", Detail: err.Error()}}})
		if _, saveErr := a.store.SaveRun(ctx, run, ""); saveErr != nil {
			return syncer.Result{}, errors.Join(err, saveErr)
		}
		return syncer.Result{RunID: runID, Report: run.Report, BindingVersion: binding.Version}, err
	}
	if _, err := a.store.SaveRun(ctx, run, ""); err != nil {
		a.failed.Add(1)
		return syncer.Result{}, err
	}
	a.runs.Add(1)
	a.loaded.Add(int64(run.Report.Created + run.Report.Updated))
	a.rejected.Add(int64(len(run.Report.Rejects)))
	return syncer.Result{RunID: runID, Report: run.Report, BindingVersion: binding.Version}, nil
}

// syncMapping is the mapping a scheduled run applies: the binding's own spec, else the newest published spec of its entity.
func (a *API) syncMapping(ctx context.Context, b store.EntityBinding) (raw []byte, specID string, err error) {
	if b.MappingSpecID != "" {
		spec, err := a.store.GetMappingSpec(ctx, b.MappingSpecID)
		return spec.Spec, spec.ID, err
	}
	specs, err := a.store.ListMappingSpecs(ctx, b.Entity, nil, 1)
	if err != nil {
		return nil, "", err
	}
	if len(specs) == 0 {
		return nil, "", fmt.Errorf("no published mapping spec for %s: name one on the entity binding or publish one", b.Entity)
	}
	return specs[0].Spec, specs[0].ID, nil
}
