package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	projectsPath        = "/v1/integration/projects"
	dutyProjectWrite    = "migration.project.write"
	CodeProjectNotCopy  = "MIG-PROJECT-NOT-COPY"
	maxPreviewKeys      = 200
	maxPreviewKeyLength = 200
)

type createProjectRequest struct {
	Name                string `json:"name"`
	ProjectType         string `json:"projectType"`
	SourceSystem        string `json:"sourceSystem"`
	SourceLegalEntityID string `json:"sourceLegalEntityId"`
	TargetLegalEntityID string `json:"targetLegalEntityId"`
	// ConnectionProfileID is the connection profile a migrate project reads from (optional).
	ConnectionProfileID string `json:"connectionProfileId"`
	CutoverAt           string `json:"cutoverAt"`
	ScheduleCron        string `json:"scheduleCron"`
}

// updateProjectRequest changes only the mutable fields; an empty cutoverAt or scheduleCron clears it, an omitted (or
// null) field stays as it is.
type updateProjectRequest struct {
	Version      *int    `json:"version"`
	Name         *string `json:"name"`
	Status       *string `json:"status"`
	CutoverAt    *string `json:"cutoverAt"`
	ScheduleCron *string `json:"scheduleCron"`
	// ConnectionProfileID attaches a connection profile of the project's source system; empty detaches it.
	ConnectionProfileID *string `json:"connectionProfileId"`
}

type replaceRulesRequest struct {
	Version *int                `json:"version"`
	Rules   []domain.KeyRewrite `json:"rules"`
}

type previewRequest struct {
	Rules *[]domain.KeyRewrite `json:"rules"`
	Keys  []string             `json:"keys"`
}

type previewRow struct {
	Key       string `json:"key"`
	Rewritten string `json:"rewritten"`
	Changed   bool   `json:"changed"`
}

func (a *API) registerProjects(r chi.Router) {
	r.With(authz.RequireDuty(dutyProjectWrite)).Post(projectsPath, a.createProject)
	r.With(authz.RequireDuty(dutyRun)).Get(projectsPath, a.listProjects)
	r.With(authz.RequireDuty(dutyRun)).Get(projectsPath+"/{id}", a.getProject)
	r.With(authz.RequireDuty(dutyProjectWrite)).Patch(projectsPath+"/{id}", a.updateProject)
	r.With(authz.RequireDuty(dutyRun)).Get(projectsPath+"/{id}/key-rewrite-rules", a.getRules)
	r.With(authz.RequireDuty(dutyProjectWrite)).Put(projectsPath+"/{id}/key-rewrite-rules", a.putRules)
	r.With(authz.RequireDuty(dutyRun)).Post(projectsPath+"/{id}/key-rewrite-rules:preview", a.previewRules)
}

func parseCutover(raw string) (*time.Time, bool) {
	if raw == "" {
		return nil, true
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, false
	}
	t = t.UTC()
	return &t, true
}

func encodeProjectCursor(c store.ProjectCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeProjectCursor(s string) (*store.ProjectCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	var c store.ProjectCursor
	if json.Unmarshal(raw, &c) != nil || !ids.Valid(c.ID) {
		return nil, false
	}
	return &c, true
}

func (a *API) createProject(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req createProjectRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	cutover, ok := parseCutover(req.CutoverAt)
	p := domain.Project{
		Name: req.Name, Type: req.ProjectType, SourceSystem: req.SourceSystem, SourceLegalEntityID: req.SourceLegalEntityID,
		TargetLegalEntityID: req.TargetLegalEntityID, ConnectionProfileID: req.ConnectionProfileID, Status: domain.ProjectActive, CutoverAt: cutover, ScheduleCron: req.ScheduleCron,
	}
	bad := domain.ValidateProject(p, ids.Valid)
	if !ok {
		bad["cutoverAt"] = "must be an RFC 3339 timestamp"
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	if p.ConnectionProfileID != "" {
		if err := a.checkProjectProfile(r, p.ConnectionProfileID, p.SourceSystem); err != nil {
			fail(w, r, err)
			return
		}
	}
	saved, created, err := a.store.SaveProject(r.Context(), store.Project{
		Name: p.Name, Type: p.Type, SourceSystem: p.SourceSystem, SourceLegalEntityID: p.SourceLegalEntityID,
		TargetLegalEntityID: p.TargetLegalEntityID, ConnectionProfileID: p.ConnectionProfileID, Status: p.Status, CutoverAt: p.CutoverAt, ScheduleCron: p.ScheduleCron,
	}, r.Header.Get(idempotencyHeader))
	if err != nil {
		a.logFailure(r, "create project failed", err)
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", projectsPath+"/"+saved.ID)
	}
	httpx.JSON(w, status, saved)
}

func (a *API) listProjects(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	limit, ok := parseLimit(w, r)
	if !ok {
		return
	}
	if limit == 0 {
		limit = defaultPage
	}
	q := r.URL.Query()
	bad := map[string]string{}
	if v := q.Get("projectType"); v != "" && !slices.Contains([]string{domain.ProjectMigrate, domain.ProjectCopy, domain.ProjectConfigPackage}, v) {
		bad["projectType"] = "must be one of migrate, copy, config_package"
	}
	if v := q.Get("status"); v != "" && v != domain.ProjectActive && v != domain.ProjectArchived {
		bad["status"] = "must be active or archived"
	}
	var after *store.ProjectCursor
	if v := q.Get("cursor"); v != "" {
		c, ok := decodeProjectCursor(v)
		if !ok {
			bad["cursor"] = "is not a cursor returned by this endpoint"
		}
		after = c
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	rows, err := a.store.ListProjects(r.Context(), store.ProjectFilter{Type: q.Get("projectType"), Status: q.Get("status"), After: after, Limit: limit + 1})
	if err != nil {
		a.logFailure(r, "list projects failed", err)
		fail(w, r, err)
		return
	}
	body := map[string]any{}
	if len(rows) > limit {
		rows = rows[:limit]
		body["nextCursor"] = encodeProjectCursor(store.ProjectCursor{Name: rows[limit-1].Name, ID: rows[limit-1].ID})
	}
	if rows == nil {
		rows = []store.Project{}
	}
	body["items"] = rows
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) getProject(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	p, err := a.store.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "get project failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

func (a *API) updateProject(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req updateProjectRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	bad := map[string]string{}
	if req.Version == nil || *req.Version < 1 {
		bad["version"] = "is required and must be at least 1"
	}
	var cutover *time.Time
	if req.CutoverAt != nil {
		t, ok := parseCutover(*req.CutoverAt)
		if !ok {
			bad["cutoverAt"] = "must be an RFC 3339 timestamp, or empty to clear"
		}
		cutover = t
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	var profile store.ConnectionProfile
	if req.ConnectionProfileID != nil && *req.ConnectionProfileID != "" {
		if !ids.Valid(*req.ConnectionProfileID) {
			fail(w, r, validation(map[string]string{"connectionProfileId": "must be a UUID"}))
			return
		}
		var err error
		if profile, err = a.store.GetConnectionProfile(r.Context(), *req.ConnectionProfileID); err != nil {
			a.logFailure(r, "load connection profile for project failed", err)
			fail(w, r, unknownProfile(err))
			return
		}
	}
	saved, err := a.store.UpdateProject(r.Context(), chi.URLParam(r, "id"), *req.Version, func(p *store.Project) error {
		// An archived project is frozen: the only change it takes is reactivation (with or without other edits).
		reactivating := req.Status != nil && *req.Status == domain.ProjectActive
		if p.Status == domain.ProjectArchived && !reactivating && (req.Name != nil || req.CutoverAt != nil || req.ScheduleCron != nil) {
			return store.ProjectArchived()
		}
		if req.Name != nil {
			p.Name = *req.Name
		}
		if req.Status != nil {
			p.Status = *req.Status
		}
		if req.CutoverAt != nil {
			p.CutoverAt = cutover
		}
		if req.ScheduleCron != nil {
			p.ScheduleCron = *req.ScheduleCron
		}
		if req.ConnectionProfileID != nil {
			p.ConnectionProfileID = *req.ConnectionProfileID
		}
		fields := domain.ValidateProject(domain.Project{
			Name: p.Name, Type: p.Type, SourceSystem: p.SourceSystem, SourceLegalEntityID: p.SourceLegalEntityID,
			TargetLegalEntityID: p.TargetLegalEntityID, ConnectionProfileID: p.ConnectionProfileID, Status: p.Status, CutoverAt: p.CutoverAt, ScheduleCron: p.ScheduleCron,
		}, ids.Valid)
		if len(fields) > 0 {
			return validation(fields)
		}
		if profile.ID != "" {
			return profileFits(profile, p.SourceSystem)
		}
		return nil
	})
	if err != nil {
		a.logFailure(r, "update project failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

// copyProject loads a project and answers 422 unless it is a copy project, the only type that has rewrite rules.
func (a *API) copyProject(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	p, err := a.store.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "get project failed", err)
		fail(w, r, err)
		return store.Project{}, false
	}
	if p.Type != domain.ProjectCopy {
		fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, CodeProjectNotCopy, "key rewrite rules belong to copy projects"))
		return store.Project{}, false
	}
	return p, true
}

func (a *API) getRules(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	p, ok := a.copyProject(w, r)
	if !ok {
		return
	}
	rules, err := a.store.RewriteRules(r.Context(), p.ID)
	if err != nil {
		a.logFailure(r, "get rewrite rules failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"version": p.Version, "items": rules})
}

func (a *API) putRules(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req replaceRulesRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	bad := domain.ValidateRewriteRules(req.Rules)
	if req.Version == nil || *req.Version < 1 {
		bad["version"] = "is required and must be at least 1"
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	p, ok := a.copyProject(w, r)
	if !ok {
		return
	}
	saved, err := a.store.ReplaceRewriteRules(r.Context(), p.ID, *req.Version, req.Rules)
	if err != nil {
		a.logFailure(r, "replace rewrite rules failed", err)
		fail(w, r, err)
		return
	}
	rules := req.Rules
	if rules == nil {
		rules = []domain.KeyRewrite{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"version": saved.Version, "items": rules})
}

// previewRules shows what a copy would do to sample record keys: the rules in the request, or the stored ones.
// It writes nothing.
func (a *API) previewRules(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req previewRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	bad := map[string]string{}
	if len(req.Keys) == 0 || len(req.Keys) > maxPreviewKeys {
		bad["keys"] = "needs 1 to 200 keys"
	}
	for _, k := range req.Keys {
		if k == "" || len(k) > maxPreviewKeyLength || strings.TrimSpace(k) != k {
			bad["keys"] = "every key is required, at most 200 characters, without surrounding spaces"
		}
	}
	if req.Rules != nil {
		for k, v := range domain.ValidateRewriteRules(*req.Rules) {
			bad[k] = v
		}
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	p, ok := a.copyProject(w, r)
	if !ok {
		return
	}
	var rules []domain.KeyRewrite
	if req.Rules != nil {
		rules = *req.Rules
	} else {
		var err error
		if rules, err = a.store.RewriteRules(r.Context(), p.ID); err != nil {
			a.logFailure(r, "get rewrite rules failed", err)
			fail(w, r, err)
			return
		}
	}
	records := make([]domain.Record, len(req.Keys))
	for i, k := range req.Keys {
		records[i] = domain.Record{Key: k}
	}
	out := make([]previewRow, len(records))
	for i, rec := range domain.RewriteKeys(records, rules) {
		out[i] = previewRow{Key: req.Keys[i], Rewritten: rec.Key, Changed: rec.Key != req.Keys[i]}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func unknownProfile(err error) error {
	var p *httpx.Problem
	if errors.As(err, &p) && p.Status == http.StatusNotFound {
		return store.ConnectionUnknown()
	}
	return err
}

// profileFits refuses a disabled profile and one of another source system than the project's.
func profileFits(c store.ConnectionProfile, sourceSystem string) error {
	switch {
	case c.Status != domain.ConnActive:
		return validation(map[string]string{"connectionProfileId": "the connection profile is disabled"})
	case c.SourceSystem != sourceSystem:
		return validation(map[string]string{"connectionProfileId": "the connection profile is for " + c.SourceSystem + ", not " + sourceSystem})
	}
	return nil
}

// checkProjectProfile loads the profile a new project names and checks it fits the project.
func (a *API) checkProjectProfile(r *http.Request, profileID, sourceSystem string) error {
	c, err := a.store.GetConnectionProfile(r.Context(), profileID)
	if err != nil {
		a.logFailure(r, "load connection profile for project failed", err)
		return unknownProfile(err)
	}
	return profileFits(c, sourceSystem)
}
