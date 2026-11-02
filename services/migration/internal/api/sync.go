package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	syncPath              = "/v1/integration/sync-status"
	conflictsPath         = "/v1/integration/sync-conflicts"
	CodeAcceptFailed      = "MIG-SYNC-ACCEPT-FAILED"
	CodeAcceptUnavailable = "MIG-SYNC-ACCEPT-UNAVAILABLE"
)

// registerSync wires the sync monitor: reads and conflict resolution need migration.run (resolving applies legacy
// values to Passage records, as a run does); pause, resume and source of truth edit the schedule and need
// migration.project.write.
func (a *API) registerSync(r chi.Router) {
	r.With(authz.RequireDuty(dutyRun)).Get(syncPath, a.listSync)
	r.With(authz.RequireDuty(dutyRun)).Get(syncPath+"/{id}", a.getSync)
	r.With(authz.RequireDuty(dutyProjectWrite)).Patch(syncPath+"/{id}", a.updateSync)
	r.With(authz.RequireDuty(dutyRun)).Get(syncPath+"/{id}/absent", a.listAbsent)
	r.With(authz.RequireDuty(dutyRun)).Get(conflictsPath, a.listConflicts)
	r.With(authz.RequireDuty(dutyRun)).Post(conflictsPath+"/{id}:resolve", a.resolveConflict)
}

type updateSyncRequest struct {
	Version       *int    `json:"version"`
	Paused        *bool   `json:"paused"`
	SourceOfTruth *string `json:"sourceOfTruth"`
	KeyCheckHours *int    `json:"keyCheckHours"`
	KeyCheckNow   bool    `json:"keyCheckNow"`
	RunNow        bool    `json:"runNow"`
}

func (a *API) listSync(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.SyncFilter{Failing: q.Get("failing") == "true", Limit: limit + 1}
	bad := map[string]string{}
	switch v := q.Get("paused"); v {
	case "":
	case "true", "false":
		b := v == "true"
		f.Paused = &b
	default:
		bad["paused"] = "must be true or false"
	}
	if v := q.Get("failing"); v != "" && v != "true" && v != "false" {
		bad["failing"] = "must be true or false"
	}
	if v := q.Get("cursor"); v != "" {
		c, ok := decodeBindingCursor(v)
		if !ok {
			bad["cursor"] = "is not a cursor returned by this endpoint"
		}
		f.After = c
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	rows, err := a.store.ListSync(r.Context(), f)
	if err != nil {
		a.logFailure(r, "list sync status failed", err)
		fail(w, r, err)
		return
	}
	rows, more := trimPage(rows, limit)
	body := map[string]any{"items": nonNil(rows)}
	if more {
		body["nextCursor"] = encodeBindingCursor(store.BindingCursor{Entity: rows[limit-1].Entity, ID: rows[limit-1].BindingID})
	}
	httpx.JSON(w, http.StatusOK, body)
}

func nonNil[T any](rows []T) []T {
	if rows == nil {
		return []T{}
	}
	return rows
}

func (a *API) getSync(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	st, err := a.store.GetSync(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "get sync status failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}

func (a *API) updateSync(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req updateSyncRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	bad := map[string]string{}
	if req.Version == nil || *req.Version < 0 {
		bad["version"] = "is required (0 for a binding that was never scheduled)"
	}
	if req.SourceOfTruth != nil && !domain.ValidSourceOfTruth(*req.SourceOfTruth) {
		bad["sourceOfTruth"] = "must be legacy or passage"
	}
	if req.KeyCheckHours != nil && !domain.ValidKeyCheckHours(*req.KeyCheckHours) {
		bad["keyCheckHours"] = fmt.Sprintf("must be between 0 (off) and %d", domain.MaxKeyCheckHours)
	}
	if req.Paused == nil && req.SourceOfTruth == nil && req.KeyCheckHours == nil && !req.KeyCheckNow && !req.RunNow {
		bad["paused"] = "send paused, sourceOfTruth, keyCheckHours, keyCheckNow or runNow"
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	st, err := a.store.UpdateSync(r.Context(), chi.URLParam(r, "id"), *req.Version, store.SyncEdit{Paused: req.Paused, SourceOfTruth: req.SourceOfTruth, KeyCheckHours: req.KeyCheckHours, KeyCheckNow: req.KeyCheckNow, RunNow: req.RunNow})
	if err != nil {
		a.logFailure(r, "update sync failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, st)
}

// listAbsent lists the source keys a full read of the binding did not find in the legacy system. Nothing is
// deactivated in Passage: the list is for the operator to act on.
func (a *API) listAbsent(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	f := store.AbsentFilter{BindingID: chi.URLParam(r, "id"), Limit: limit + 1}
	if v := r.URL.Query().Get("cursor"); v != "" {
		k, ok := ledgerCursor(v)
		if !ok {
			fail(w, r, cursorProblem())
			return
		}
		f.After = k
	}
	rows, err := a.store.ListAbsent(r.Context(), f)
	if err != nil {
		a.logFailure(r, "list absent keys failed", err)
		fail(w, r, err)
		return
	}
	rows, more := trimPage(rows, limit)
	body := map[string]any{"items": nonNil(rows)}
	if more {
		body["nextCursor"] = encodePageCursor(struct {
			K string `json:"k"`
		}{rows[limit-1].SourceKey})
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) listConflicts(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	limit, ok := pageLimit(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := store.ConflictFilter{BindingID: q.Get("bindingId"), Status: q.Get("status"), Limit: limit + 1}
	bad := map[string]string{}
	if f.BindingID != "" && !ids.Valid(f.BindingID) {
		bad["bindingId"] = "must be a UUID"
	}
	if f.Status != "" && f.Status != domain.ConflictOpen && f.Status != domain.ConflictKept && f.Status != domain.ConflictAccepted {
		bad["status"] = "must be open, kept or accepted"
	}
	if v := q.Get("cursor"); v != "" {
		var c store.ConflictCursor
		if !decodePageCursor(v, &c) || !ids.Valid(c.ID) || c.DetectedAt.IsZero() {
			bad["cursor"] = "is not a cursor returned by this endpoint"
		} else {
			f.After = &c
		}
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	rows, err := a.store.ListConflicts(r.Context(), f)
	if err != nil {
		a.logFailure(r, "list sync conflicts failed", err)
		fail(w, r, err)
		return
	}
	rows, more := trimPage(rows, limit)
	body := map[string]any{"items": nonNil(rows)}
	if more {
		body["nextCursor"] = encodePageCursor(store.ConflictCursor{DetectedAt: rows[limit-1].DetectedAt, ID: rows[limit-1].ID})
	}
	httpx.JSON(w, http.StatusOK, body)
}

type resolveRequest struct {
	Resolution string `json:"resolution"`
	// Version is the conflict version the operator decided on; a run that replaced the held values since refuses it.
	Version *int `json:"version"`
}

// resolveConflict closes an open conflict. keep_passage leaves the Passage record as it is and remembers the legacy
// row's hash, so the same row is not reported again until it changes. accept_legacy writes the held legacy values to
// the Passage record through the loader (under the version recorded at the last load) and closes the conflict only
// when the target accepted them.
func (a *API) resolveConflict(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req resolveRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	bad := map[string]string{}
	if req.Resolution != domain.ResolveAcceptLegacy && req.Resolution != domain.ResolveKeepPassage {
		bad["resolution"] = "must be accept_legacy or keep_passage"
	}
	if req.Version == nil || *req.Version < 1 {
		bad["version"] = "is required: the version of the conflict you reviewed"
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	ctx := r.Context()
	c, err := a.store.GetConflict(ctx, chi.URLParam(r, "id"))
	if err != nil {
		fail(w, r, err)
		return
	}
	if c.Status != domain.ConflictOpen {
		fail(w, r, httpx.NewProblem(http.StatusConflict, store.CodeConflictClosed, "this conflict is already resolved"))
		return
	}
	if c.Version != *req.Version {
		// A run replaced the legacy values after the operator looked: they decide again on what is held now.
		fail(w, r, httpx.NewProblem(http.StatusConflict, store.CodeVersionConflict, fmt.Sprintf("current version is %d: the legacy row changed again; review the new values", c.Version)))
		return
	}
	status := domain.ConflictKept
	if req.Resolution == domain.ResolveAcceptLegacy {
		status = domain.ConflictAccepted
		if err := a.applyLegacy(ctx, c); err != nil {
			a.logFailure(r, "accepting legacy values failed", err)
			fail(w, r, err)
			return
		}
	}
	by := ""
	if p, ok := tenant.FromContext(ctx); ok {
		by = p.UserID
	}
	saved, err := a.store.ResolveConflict(ctx, c.ID, c.Version, status, by)
	if err != nil {
		a.logFailure(r, "resolving sync conflict failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

// applyLegacy loads the conflict's held values into the Passage record and moves the cross-reference to them.
func (a *API) applyLegacy(ctx context.Context, c store.SyncConflict) error {
	if !a.live() {
		return httpx.NewProblem(http.StatusNotImplemented, CodeAcceptUnavailable, "accepting legacy values needs the loader (MIG_TARGET_*_URL and a service signing key)")
	}
	b, err := a.store.GetEntityBinding(ctx, c.BindingID)
	if err != nil {
		return err
	}
	st, err := a.store.GetSync(ctx, c.BindingID)
	if err != nil {
		return err
	}
	profile, err := a.store.GetConnectionProfile(ctx, b.ConnectionProfileID)
	if err != nil {
		return err
	}
	legalEntity := ""
	if st.ProjectID != "" {
		if pr, err := a.store.GetProject(ctx, st.ProjectID); err == nil {
			legalEntity = pr.TargetLegalEntityID
		}
	}
	loader, _, err := a.targets(ctx, TargetRun{SourceSystem: profile.SourceSystem, Entity: b.Entity, LegalEntityID: legalEntity})
	if err != nil {
		return err
	}
	key := domain.XrefKey{SourceSystem: profile.SourceSystem, Entity: b.Entity, SourceKey: c.SourceKey}
	x, found, err := a.store.Xref().Get(ctx, key)
	if err != nil {
		return err
	}
	if !found {
		return httpx.NewProblem(http.StatusConflict, CodeAcceptFailed, "the record this row was loaded into is not in the cross-reference; run a full sync instead")
	}
	res, err := loader.LoadBatch(ctx, b.Entity, []domain.LoadItem{{TargetID: x.TargetID, SourceKey: c.SourceKey, IdempotencyKey: domain.IdempotencyKey(profile.SourceSystem, b.Entity, c.SourceKey, c.LegacyHash),
		Fields: c.LegacyFields, Existing: true, TargetVersion: x.TargetVersion}})
	if err != nil || len(res) != 1 {
		return httpx.NewProblem(http.StatusBadGateway, CodeAcceptFailed, "the target service could not be reached; try again")
	}
	if res[0].Code != "" {
		hint := ""
		if res[0].Code == domain.CodeSourceOfTruth {
			hint = " (the Passage record changed after the last load, and the loader cannot yet read its current version to overwrite it; edit it in Passage, or keep Passage)"
		}
		return httpx.NewProblem(http.StatusConflict, CodeAcceptFailed, fmt.Sprintf("the target refused the legacy values: %s %s%s", res[0].Code, res[0].Detail, hint))
	}
	version := res[0].TargetVersion
	return a.store.Xref().Put(ctx, key, domain.XrefEntry{TargetID: x.TargetID, ContentHash: c.LegacyHash, TargetVersion: version})
}
