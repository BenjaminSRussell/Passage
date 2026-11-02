package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/migration/internal/connector"
	"github.com/BenjaminSRussell/passage/services/migration/internal/domain"
	"github.com/BenjaminSRussell/passage/services/migration/internal/probe"
	"github.com/BenjaminSRussell/passage/services/migration/internal/store"
)

const (
	bindingsPath          = "/v1/integration/entity-bindings"
	CodeExtractNotConfig  = "MIG-EXTRACT-NOT-CONFIGURED"
	CodeProfileDisabled   = "MIG-CONNECTION-DISABLED"
	maxPreviewUploadBytes = 4 << 20
	maxPreviewBody        = 6 << 20
)

// WithExtraction turns on the preview routes: env carries the egress guard and the drop root extraction reads through
// (nil keeps the preview routes at 501 MIG-EXTRACT-NOT-CONFIGURED; binding CRUD needs neither).
func (a *API) WithExtraction(env connector.Env) *API {
	a.env = &env
	return a
}

type createBindingRequest struct {
	ConnectionProfileID string         `json:"connectionProfileId"`
	Entity              string         `json:"entity"`
	Source              map[string]any `json:"source"`
	KeyFields           []string       `json:"keyFields"`
	CursorField         string         `json:"cursorField"`
	CursorType          string         `json:"cursorType"`
	MappingSpecID       string         `json:"mappingSpecId"`
}

type updateBindingRequest struct {
	Version       *int            `json:"version"`
	Source        *map[string]any `json:"source"`
	KeyFields     *[]string       `json:"keyFields"`
	CursorField   *string         `json:"cursorField"`
	CursorType    *string         `json:"cursorType"`
	MappingSpecID *string         `json:"mappingSpecId"`
	Status        *string         `json:"status"`
}

type bindingPreviewUpload struct {
	Name          string `json:"name"`
	ContentBase64 string `json:"contentBase64"`
}

// bindingPreviewRequest is the body of both preview routes. The draft route (no stored binding) also takes the binding's own
// fields, the stored route takes them from the binding.
type bindingPreviewRequest struct {
	createBindingRequest
	Limit *int `json:"limit"`
	// Mapping (inline JSON) or MappingSpecID (published spec; defaults to the binding's) selects the rules to run. With
	// neither the preview shows the source rows only.
	Mapping json.RawMessage       `json:"mapping"`
	Upload  *bindingPreviewUpload `json:"upload"`
}

type bindingPreviewResponse struct {
	OK      bool                   `json:"ok"`
	Failure *probe.Failure         `json:"failure,omitempty"`
	Mapping *bindingPreviewMapping `json:"mapping,omitempty"`
	*connector.PreviewResult
	DurationMs int64 `json:"durationMs"`
}

type bindingPreviewMapping struct {
	Entity  string `json:"entity"`
	Version int    `json:"version"`
	SpecID  string `json:"mappingSpecId,omitempty"`
}

func (a *API) registerBindings(r chi.Router) {
	r.With(authz.RequireDuty(dutyConnectionWrite)).Post(bindingsPath, a.createBinding)
	r.With(authz.RequireDuty(dutyRun)).Get(bindingsPath, a.listBindings)
	r.With(authz.RequireDuty(dutyRun)).Get(bindingsPath+"/{id}", a.getBinding)
	r.With(authz.RequireDuty(dutyConnectionWrite)).Patch(bindingsPath+"/{id}", a.updateBinding)
	r.With(authz.RequireDuty(dutyConnectionWrite)).Post(bindingsPath+":preview", a.previewDraftBinding)
	r.With(authz.RequireDuty(dutyConnectionWrite)).Post(bindingsPath+"/{id}:preview", a.previewBinding)
}

func (a *API) createBinding(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req createBindingRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	profile, ok := a.bindingProfile(w, r, req.ConnectionProfileID)
	if !ok {
		return
	}
	b := store.EntityBinding{ConnectionProfileID: profile.ID, Entity: req.Entity, Source: req.Source, KeyFields: req.KeyFields,
		CursorField: req.CursorField, CursorType: req.CursorType, MappingSpecID: req.MappingSpecID, Status: domain.BindingActive}
	if b.Source == nil {
		b.Source = map[string]any{}
	}
	if bad := validateBinding(b, profile.Kind); len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	if !a.bindingSpecFits(w, r, b.MappingSpecID, b.Entity) {
		return
	}
	saved, created, err := a.store.SaveEntityBinding(r.Context(), b, r.Header.Get(idempotencyHeader))
	if err != nil {
		a.logFailure(r, "create entity binding failed", err)
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", bindingsPath+"/"+saved.ID)
	}
	httpx.JSON(w, status, saved)
}

// bindingProfile loads the profile a binding names: 422 MIG-CONNECTION-UNKNOWN when this tenant has none with the id,
// 409 MIG-CONNECTION-DISABLED when it is disabled.
func (a *API) bindingProfile(w http.ResponseWriter, r *http.Request, id string) (store.ConnectionProfile, bool) {
	if id == "" {
		fail(w, r, validation(map[string]string{"connectionProfileId": "is required"}))
		return store.ConnectionProfile{}, false
	}
	c, err := a.store.GetConnectionProfile(r.Context(), id)
	if err != nil {
		var p *httpx.Problem
		if errors.As(err, &p) && p.Status == http.StatusNotFound {
			err = store.ConnectionUnknown()
		} else {
			a.logFailure(r, "load connection profile for binding failed", err)
		}
		fail(w, r, err)
		return store.ConnectionProfile{}, false
	}
	if c.Status != domain.ConnActive {
		fail(w, r, httpx.NewProblem(http.StatusConflict, CodeProfileDisabled, "the connection profile is disabled"))
		return store.ConnectionProfile{}, false
	}
	return c, true
}

// bindingSpecFits checks a binding's default mapping spec: 400 when the id is not a UUID, 422 MIG-SPEC-UNKNOWN when
// this tenant has no such spec, 400 when the spec maps another entity. An empty id (no default) fits.
func (a *API) bindingSpecFits(w http.ResponseWriter, r *http.Request, specID, entity string) bool {
	if specID == "" {
		return true
	}
	if !ids.Valid(specID) {
		fail(w, r, validation(map[string]string{"mappingSpecId": "must be a UUID"}))
		return false
	}
	spec, err := a.store.GetMappingSpec(r.Context(), specID)
	if err != nil {
		var p *httpx.Problem
		if errors.As(err, &p) && p.Status == http.StatusNotFound {
			err = store.SpecUnknown()
		} else {
			a.logFailure(r, "load mapping spec for binding failed", err)
		}
		fail(w, r, err)
		return false
	}
	if spec.Entity != entity {
		fail(w, r, validation(map[string]string{"mappingSpecId": "is a mapping for entity " + spec.Entity + ", the binding is for " + entity}))
		return false
	}
	return true
}

func validateBinding(b store.EntityBinding, kind string) map[string]string {
	return domain.ValidateBinding(domain.EntityBinding{Entity: b.Entity, Source: b.Source, KeyFields: b.KeyFields, CursorField: b.CursorField, CursorType: b.CursorType}, kind)
}

func (a *API) listBindings(w http.ResponseWriter, r *http.Request) {
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
	if v := q.Get("status"); v != "" && v != domain.BindingActive && v != domain.BindingDisabled {
		bad["status"] = "must be active or disabled"
	}
	var after *store.BindingCursor
	if v := q.Get("cursor"); v != "" {
		c, ok := decodeBindingCursor(v)
		if !ok {
			bad["cursor"] = "is not a cursor returned by this endpoint"
		}
		after = c
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	rows, err := a.store.ListEntityBindings(r.Context(), store.BindingFilter{ConnectionProfileID: q.Get("connectionProfileId"), Entity: q.Get("entity"), Status: q.Get("status"), After: after, Limit: limit + 1})
	if err != nil {
		a.logFailure(r, "list entity bindings failed", err)
		fail(w, r, err)
		return
	}
	body := map[string]any{}
	rows, more := trimPage(rows, limit)
	if more {
		body["nextCursor"] = encodeBindingCursor(store.BindingCursor{Entity: rows[limit-1].Entity, ID: rows[limit-1].ID})
	}
	if rows == nil {
		rows = []store.EntityBinding{}
	}
	body["items"] = rows
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) getBinding(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	b, err := a.store.GetEntityBinding(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "get entity binding failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, b)
}

func (a *API) updateBinding(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req updateBindingRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if req.Version == nil || *req.Version < 1 {
		fail(w, r, validation(map[string]string{"version": "is required and must be at least 1"}))
		return
	}
	id := chi.URLParam(r, "id")
	cur, err := a.store.GetEntityBinding(r.Context(), id)
	if err != nil {
		a.logFailure(r, "update entity binding failed", err)
		fail(w, r, err)
		return
	}
	profile, err := a.store.GetConnectionProfile(r.Context(), cur.ConnectionProfileID)
	if err != nil {
		a.logFailure(r, "load connection profile for binding failed", err)
		fail(w, r, err)
		return
	}
	if req.MappingSpecID != nil && !a.bindingSpecFits(w, r, *req.MappingSpecID, cur.Entity) {
		return
	}
	saved, err := a.store.UpdateEntityBinding(r.Context(), id, *req.Version, func(b *store.EntityBinding) error {
		if req.Source != nil {
			b.Source = *req.Source
		}
		if req.KeyFields != nil {
			b.KeyFields = *req.KeyFields
		}
		if req.CursorField != nil {
			b.CursorField = *req.CursorField
		}
		if req.CursorType != nil {
			b.CursorType = *req.CursorType
		}
		if req.MappingSpecID != nil {
			b.MappingSpecID = *req.MappingSpecID
		}
		if req.Status != nil {
			if *req.Status != domain.BindingActive && *req.Status != domain.BindingDisabled {
				return validation(map[string]string{"status": "must be active or disabled"})
			}
			b.Status = *req.Status
		}
		if b.Source == nil {
			b.Source = map[string]any{}
		}
		if bad := validateBinding(*b, profile.Kind); len(bad) > 0 {
			return validation(bad)
		}
		return nil
	})
	if err != nil {
		a.logFailure(r, "update entity binding failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

func (a *API) previewBinding(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	b, err := a.store.GetEntityBinding(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "preview entity binding failed", err)
		fail(w, r, err)
		return
	}
	var req bindingPreviewRequest
	if err := httpx.DecodeLimit(r, &req, maxPreviewBody); err != nil {
		fail(w, r, err)
		return
	}
	if req.ConnectionProfileID != "" || req.Entity != "" || req.Source != nil || req.KeyFields != nil || req.CursorField != "" || req.CursorType != "" {
		fail(w, r, validation(map[string]string{"source": "a stored binding is previewed as stored; edit it first, or use the draft preview"}))
		return
	}
	req.createBindingRequest = createBindingRequest{ConnectionProfileID: b.ConnectionProfileID, Entity: b.Entity, Source: b.Source, KeyFields: b.KeyFields,
		CursorField: b.CursorField, CursorType: b.CursorType, MappingSpecID: b.MappingSpecID}
	a.runPreview(w, r, req, b.Status)
}

func (a *API) previewDraftBinding(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req bindingPreviewRequest
	if err := httpx.DecodeLimit(r, &req, maxPreviewBody); err != nil {
		fail(w, r, err)
		return
	}
	a.runPreview(w, r, req, domain.BindingActive)
}

// runPreview validates the binding, opens the profile's secret, extracts a few rows and runs the mapping and rules
// over them. It answers 200 with ok false when the source could not be read (the preview ran, the source did not
// answer), like the connection test. Nothing is loaded and nothing is stored.
func (a *API) runPreview(w http.ResponseWriter, r *http.Request, req bindingPreviewRequest, bindingStatus string) {
	bad := map[string]string{}
	if req.Limit != nil && (*req.Limit < 1 || *req.Limit > connector.MaxPreviewRows) {
		bad["limit"] = "must be 1 to 100"
	}
	if req.Upload != nil {
		if req.Upload.Name == "" || len(req.Upload.Name) > 255 || strings.ContainsAny(req.Upload.Name, "/\\\x00") {
			bad["upload.name"] = "is required: a file name without a path"
		}
		if req.Upload.ContentBase64 == "" {
			bad["upload.contentBase64"] = "is required"
		}
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	profile, ok := a.bindingProfile(w, r, req.ConnectionProfileID)
	if !ok {
		return
	}
	b := store.EntityBinding{Entity: req.Entity, Source: req.Source, KeyFields: req.KeyFields, CursorField: req.CursorField, CursorType: req.CursorType}
	if b.Source == nil {
		b.Source = map[string]any{}
	}
	if fields := validateBinding(b, profile.Kind); len(fields) > 0 {
		fail(w, r, validation(fields))
		return
	}
	if bindingStatus != domain.BindingActive {
		fail(w, r, httpx.NewProblem(http.StatusConflict, "MIG-BINDING-DISABLED", "the binding is disabled"))
		return
	}
	if a.env == nil {
		fail(w, r, httpx.NewProblem(http.StatusNotImplemented, CodeExtractNotConfig, "extraction is not configured on this deployment"))
		return
	}
	mapping, info, ok := a.bindingPreviewMapping(w, r, req, b.Entity)
	if !ok {
		return
	}
	spec := connector.Spec{Kind: profile.Kind, Profile: profile.Config, Source: b.Source, KeyFields: b.KeyFields, CursorField: b.CursorField, CursorType: b.CursorType}
	if req.Upload != nil {
		if profile.Kind != domain.ConnFile {
			fail(w, r, validation(map[string]string{"upload": "can only be sent for a file connection profile"}))
			return
		}
		raw, err := base64.StdEncoding.DecodeString(req.Upload.ContentBase64)
		if err != nil || len(raw) > maxPreviewUploadBytes {
			fail(w, r, validation(map[string]string{"upload.contentBase64": "must be base64 text of at most 4 MiB"}))
			return
		}
		spec.Upload = &connector.Upload{Name: req.Upload.Name, Content: raw}
	}
	if needsSecret(profile) {
		if a.keys == nil {
			fail(w, r, secretsNotConfigured())
			return
		}
		if profile.SecretSet {
			secret, err := a.openConnectionSecret(r, profile.ID)
			if err != nil {
				fail(w, r, err)
				return
			}
			spec.Secret = secret
		}
	}
	limit := 10
	if req.Limit != nil {
		limit = *req.Limit
	}
	start := time.Now()
	res, err := connector.Preview(r.Context(), *a.env, spec, mapping, limit)
	out := bindingPreviewResponse{DurationMs: time.Since(start).Milliseconds()}
	if err != nil {
		out.Failure = probe.Classify(err, spec.Secret)
		a.logFailure(r, "entity binding preview failed", errors.New(out.Failure.Code))
		httpx.JSON(w, http.StatusOK, out)
		return
	}
	out.OK, out.PreviewResult, out.Mapping = true, &res, info
	httpx.JSON(w, http.StatusOK, out)
}

// bindingPreviewMapping resolves the rules to run: an inline mapping, else the published spec named by the request or the
// binding, else none. The mapping must be for the binding's entity, and its own test cases must pass.
func (a *API) bindingPreviewMapping(w http.ResponseWriter, r *http.Request, req bindingPreviewRequest, entity string) (*domain.Mapping, *bindingPreviewMapping, bool) {
	var raw []byte
	specID := req.MappingSpecID
	switch {
	case len(req.Mapping) > 0 && string(req.Mapping) != "null":
		raw = req.Mapping
		specID = ""
	case specID != "":
		spec, err := a.store.GetMappingSpec(r.Context(), specID)
		if err != nil {
			var p *httpx.Problem
			if errors.As(err, &p) && p.Status == http.StatusNotFound {
				err = store.SpecUnknown()
			} else {
				a.logFailure(r, "load mapping spec for preview failed", err)
			}
			fail(w, r, err)
			return nil, nil, false
		}
		raw = spec.Spec
	default:
		return nil, nil, true
	}
	m, err := domain.ParseMapping(raw)
	if err != nil {
		fail(w, r, problemFrom(err))
		return nil, nil, false
	}
	if m.Entity != entity {
		fail(w, r, validation(map[string]string{"mapping": "is for entity " + m.Entity + ", the binding is for " + entity}))
		return nil, nil, false
	}
	if failures := m.RunTests(); len(failures) > 0 {
		p := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeMapTestFail, "the mapping's own tests fail; fix them before previewing")
		p.Errors = make(map[string]string, len(failures))
		for _, f := range failures {
			p.Errors[f.Name] = f.Reason
		}
		fail(w, r, p)
		return nil, nil, false
	}
	return &m, &bindingPreviewMapping{Entity: m.Entity, Version: m.Version, SpecID: specID}, true
}

func encodeBindingCursor(c store.BindingCursor) string {
	raw, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeBindingCursor(s string) (*store.BindingCursor, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	var c store.BindingCursor
	if json.Unmarshal(raw, &c) != nil || !ids.Valid(c.ID) {
		return nil, false
	}
	return &c, true
}
