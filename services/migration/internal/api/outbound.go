package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

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
	outboundPath       = "/v1/integration/outbound-mappings"
	xrefRecordPath     = "/v1/integration/xref:record"
	dutyOutboundRender = store.DutyOutboundRender
	dutyXrefRecord     = store.DutyXrefRecord
	// HubService is the only caller of :render and xref:record (its service token names it as the issuer).
	HubService = store.HubService
	// CodeOutboundLegalEntity: the event names another legal entity than the service token the hub signed for it.
	CodeOutboundLegalEntity = "MIG-OUTBOUND-LEGAL-ENTITY"
)

type createOutboundRequest struct {
	Name         string          `json:"name"`
	SourceSystem string          `json:"sourceSystem"`
	Definition   json.RawMessage `json:"definition"`
}

type updateOutboundRequest struct {
	Version    *int            `json:"version"`
	Name       *string         `json:"name"`
	Status     *string         `json:"status"`
	Definition json.RawMessage `json:"definition"`
}

type renderRequest struct {
	Event   json.RawMessage `json:"event"`
	Version int             `json:"version"`
}

type outboundPreviewRequest struct {
	Event        json.RawMessage `json:"event"`
	Version      int             `json:"version"`
	SourceSystem string          `json:"sourceSystem"`
	Definition   json.RawMessage `json:"definition"`
}

type xrefRecordRequest struct {
	OutboundMappingID string               `json:"outboundMappingId"`
	Refs              []domain.OutboundRef `json:"refs"`
	LegacyKey         string               `json:"legacyKey"`
	EventID           string               `json:"eventId"`
}

func (a *API) registerOutbound(r chi.Router) {
	r.With(authz.RequireDuty(dutyProjectWrite)).Post(outboundPath, a.createOutbound)
	r.With(authz.RequireDuty(dutyRun)).Get(outboundPath, a.listOutbound)
	r.With(authz.RequireDuty(dutyRun)).Post(outboundPath+":preview", a.previewDraft)
	r.With(authz.RequireDuty(dutyRun)).Get(outboundPath+"/{id}", a.getOutbound)
	r.With(authz.RequireDuty(dutyRun)).Get(outboundPath+"/{id}/versions", a.listOutboundVersions)
	r.With(authz.RequireDuty(dutyProjectWrite)).Patch(outboundPath+"/{id}", a.updateOutbound)
	r.With(authz.RequireDuty(dutyRun)).Post(outboundPath+"/{id}:preview", a.previewOutbound)
	r.With(authz.RequireDuty(dutyOutboundRender)).Post(outboundPath+"/{id}:render", a.renderOutbound)
	r.With(authz.RequireDuty(dutyXrefRecord)).Post(xrefRecordPath, a.recordXref)
}

// fromHub admits only the integration hub's service token. The duties are service_only, but the issuer is checked as
// well: this API is the hub's contract, and nothing else was granted it.
func fromHub(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || p.TenantID == "" {
		fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return p, false
	}
	if p.Service != HubService {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "this route is for the "+HubService+" service"))
		return p, false
	}
	return p, true
}

func parseOutboundDefinition(raw json.RawMessage, version int) (domain.OutboundDefinition, []byte, error) {
	if len(raw) == 0 {
		return domain.OutboundDefinition{}, nil, validation(map[string]string{"definition": "is required"})
	}
	def, err := domain.ParseOutboundDefinition(raw, version)
	if err != nil {
		return def, nil, definitionProblem(err)
	}
	canonical, err := json.Marshal(def)
	return def, canonical, err
}

func definitionProblem(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
	}
	return err
}

func validOutboundName(name string) bool {
	return strings.TrimSpace(name) == name && name != "" && len(name) <= 120
}

func (a *API) createOutbound(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req createOutboundRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	bad := map[string]string{}
	if !validOutboundName(req.Name) {
		bad["name"] = "is required, at most 120 characters, no leading or trailing space"
	}
	if !domain.ValidSourceSystem(req.SourceSystem) {
		bad["sourceSystem"] = domain.SourceSystemHint
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	def, canonical, err := parseOutboundDefinition(req.Definition, 1)
	if err != nil {
		fail(w, r, err)
		return
	}
	saved, created, err := a.store.SaveOutboundMapping(r.Context(), store.OutboundMapping{
		Name: req.Name, SourceSystem: req.SourceSystem, EventType: def.EventType, Definition: canonical,
	}, r.Header.Get(idempotencyHeader))
	if err != nil {
		a.logFailure(r, "create outbound mapping failed", err)
		fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
		w.Header().Set("Location", outboundPath+"/"+saved.ID)
	}
	httpx.JSON(w, status, saved)
}

func (a *API) listOutbound(w http.ResponseWriter, r *http.Request) {
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
	if v := q.Get("status"); v != "" && v != store.OutboundActive && v != store.OutboundArchivedStatus {
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
	rows, err := a.store.ListOutboundMappings(r.Context(), store.OutboundFilter{Status: q.Get("status"), After: after, Limit: limit + 1})
	if err != nil {
		a.logFailure(r, "list outbound mappings failed", err)
		fail(w, r, err)
		return
	}
	body := map[string]any{}
	if len(rows) > limit {
		rows = rows[:limit]
		body["nextCursor"] = encodeProjectCursor(store.ProjectCursor{Name: rows[limit-1].Name, ID: rows[limit-1].ID})
	}
	if rows == nil {
		rows = []store.OutboundMapping{}
	}
	body["items"] = rows
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) getOutbound(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	version := 0
	if v := r.URL.Query().Get("version"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			fail(w, r, validation(map[string]string{"version": "must be a positive integer"}))
			return
		}
		version = n
	}
	m, err := a.store.GetOutboundMapping(r.Context(), chi.URLParam(r, "id"), version)
	if err != nil {
		a.logFailure(r, "get outbound mapping failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, m)
}

func (a *API) listOutboundVersions(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	rows, err := a.store.ListOutboundVersions(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		a.logFailure(r, "list outbound mapping versions failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": rows})
}

// updateOutbound writes the next version. A definition replaces the whole mapping; a rename or archive carries the
// current definition into the new version, so version N always means one complete mapping.
func (a *API) updateOutbound(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req updateOutboundRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if req.Version == nil || *req.Version < 1 {
		fail(w, r, validation(map[string]string{"version": "is required and must be at least 1"}))
		return
	}
	bad := map[string]string{}
	if req.Name != nil && !validOutboundName(*req.Name) {
		bad["name"] = "is required, at most 120 characters, no leading or trailing space"
	}
	if req.Status != nil && *req.Status != store.OutboundActive && *req.Status != store.OutboundArchivedStatus {
		bad["status"] = "must be active or archived"
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	saved, err := a.store.UpdateOutboundMapping(r.Context(), chi.URLParam(r, "id"), *req.Version, func(m *store.OutboundMapping) error {
		if m.Status == store.OutboundArchivedStatus && (req.Status == nil || *req.Status == store.OutboundArchivedStatus) {
			return httpx.NewProblem(http.StatusConflict, store.CodeOutboundArchived, "an archived outbound mapping can only be reactivated")
		}
		if req.Name != nil {
			m.Name = *req.Name
		}
		if req.Status != nil {
			m.Status = *req.Status
		}
		// The new definition (or the carried one) is pinned to the version being written.
		raw := m.Definition
		if len(req.Definition) > 0 {
			raw = req.Definition
		}
		def, canonical, err := parseOutboundDefinition(raw, m.Version+1)
		if err != nil {
			return err
		}
		m.EventType, m.Definition = def.EventType, canonical
		return nil
	})
	if err != nil {
		a.logFailure(r, "update outbound mapping failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, saved)
}

type renderView struct {
	Body           map[string]any       `json:"body"`
	IdempotencyKey string               `json:"idempotencyKey"`
	Refs           []domain.OutboundRef `json:"refs"`
	Lookups        []domain.LookupUse   `json:"lookups,omitempty"`
}

// renderOutbound is the integration hub's call. It is pure: it reads the mapping and the cross-reference and writes
// nothing, so the hub can repeat it for retries and test sends. A reference with no legacy key yet is 422
// MIG-XREF-UNRESOLVED listing each one; the hub does not retry a 4xx.
func (a *API) renderOutbound(w http.ResponseWriter, r *http.Request) {
	p, ok := fromHub(w, r)
	if !ok {
		return
	}
	var req renderRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	event, err := domain.DecodeEvent(req.Event)
	if err != nil {
		a.failRender(w, r, err)
		return
	}
	// The hub signs one token per event, for the event's legal entity, and renders nothing without one. The store reads
	// tenant data with the all-entities scope, so the token is honoured only for the event it was signed for.
	if !sameLegalEntity(event, p.LegalEntityID) {
		fail(w, r, httpx.NewProblem(http.StatusForbidden, CodeOutboundLegalEntity, "the event's legalentityid must be the legal entity of the service token"))
		return
	}
	m, err := a.store.GetOutboundMapping(r.Context(), chi.URLParam(r, "id"), req.Version)
	if err != nil {
		a.logFailure(r, "render: get outbound mapping failed", err)
		fail(w, r, err)
		return
	}
	if m.Status == store.OutboundArchivedStatus {
		fail(w, r, httpx.NewProblem(http.StatusConflict, store.CodeOutboundArchived, "this outbound mapping is archived"))
		return
	}
	out, err := a.renderEvent(r, m, event)
	if err != nil {
		a.failRender(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, renderView{Body: out.Body, IdempotencyKey: out.IdempotencyKey, Refs: out.Refs, Lookups: out.Lookups})
}

// sameLegalEntity reports whether the event's CloudEvents legalentityid extension is the token's legal entity. An
// event without one, or with one that is not a UUID string, is refused.
func sameLegalEntity(event map[string]any, tokenLE string) bool {
	le, _ := event["legalentityid"].(string)
	got, ok1 := ids.Canonical(le)
	want, ok2 := ids.Canonical(tokenLE)
	return ok1 && ok2 && got == want
}

func (a *API) render(r *http.Request, m store.OutboundMapping, rawEvent json.RawMessage) (domain.Rendered, error) {
	event, err := domain.DecodeEvent(rawEvent)
	if err != nil {
		return domain.Rendered{}, err
	}
	return a.renderEvent(r, m, event)
}

func (a *API) renderEvent(r *http.Request, m store.OutboundMapping, event map[string]any) (domain.Rendered, error) {
	def, err := domain.ParseOutboundDefinition(m.Definition, m.Version)
	if err != nil {
		return domain.Rendered{}, err
	}
	return def.Render(r.Context(), m.SourceSystem, m.ID, event, a.store)
}

func (a *API) failRender(w http.ResponseWriter, r *http.Request, err error) {
	var unresolved *domain.UnresolvedError
	var rejected *domain.RejectedError
	var de *domain.Error
	switch {
	case errors.As(err, &unresolved):
		errs := map[string]string{}
		for _, u := range unresolved.Refs {
			errs[u.Name] = u.Reason
		}
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		_ = json.NewEncoder(w).Encode(problemBody{
			Type: "https://passage.dev/problems/" + domain.CodeXrefUnresolved, Title: "Unprocessable Entity", Status: http.StatusUnprocessableEntity,
			Detail: unresolved.Error(), Instance: r.URL.Path, Code: domain.CodeXrefUnresolved, ServiceCode: domain.CodeXrefUnresolved,
			TraceID: middleware.GetReqID(r.Context()), Errors: errs, Unresolved: unresolved.Refs,
		})
	case errors.As(err, &rejected):
		errs := map[string]string{}
		for _, rj := range rejected.Rejects {
			errs[rj.Field] = rj.Code + ": " + rj.Detail
		}
		p := httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodeOutboundRejected, "the mapping could not build a body from this event")
		p.Errors = errs
		fail(w, r, p)
	case errors.As(err, &de):
		fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail))
	default:
		a.logFailure(r, "render outbound mapping failed", err)
		fail(w, r, err)
	}
}

// previewView is a preview's answer. A preview that finds a problem is still 200: the problem is what the person
// asked to see.
type previewView struct {
	Status         string                 `json:"status"` // ok, unresolved, rejected or invalid
	Body           map[string]any         `json:"body,omitempty"`
	IdempotencyKey string                 `json:"idempotencyKey,omitempty"`
	Refs           []domain.OutboundRef   `json:"refs,omitempty"`
	Warnings       []domain.Warning       `json:"warnings,omitempty"`
	Lookups        []domain.LookupUse     `json:"lookups,omitempty"`
	Unresolved     []domain.UnresolvedRef `json:"unresolved,omitempty"`
	Rejects        []domain.Reject        `json:"rejects,omitempty"`
	Code           string                 `json:"code,omitempty"`
	Detail         string                 `json:"detail,omitempty"`
}

func previewOf(out domain.Rendered, err error) (previewView, error) {
	var unresolved *domain.UnresolvedError
	var rejected *domain.RejectedError
	var de *domain.Error
	switch {
	case err == nil:
		return previewView{Status: "ok", Body: out.Body, IdempotencyKey: out.IdempotencyKey, Refs: out.Refs, Warnings: out.Warnings, Lookups: out.Lookups}, nil
	case errors.As(err, &unresolved):
		return previewView{Status: "unresolved", Code: domain.CodeXrefUnresolved, Detail: unresolved.Error(), Unresolved: unresolved.Refs}, nil
	case errors.As(err, &rejected):
		return previewView{Status: "rejected", Code: domain.CodeOutboundRejected, Detail: rejected.Error(), Rejects: rejected.Rejects}, nil
	case errors.As(err, &de):
		return previewView{Status: "invalid", Code: de.Code, Detail: de.Detail}, nil
	}
	return previewView{}, err
}

// previewOutbound renders a sample event through a stored mapping (the current version unless one is asked for), for
// the people who maintain mappings. It reads only, like :render, and answers with the problem when there is one.
func (a *API) previewOutbound(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req outboundPreviewRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if len(req.Definition) > 0 || req.SourceSystem != "" {
		fail(w, r, validation(map[string]string{"definition": "a stored mapping is previewed with its own definition; use " + outboundPath + ":preview for a draft"}))
		return
	}
	m, err := a.store.GetOutboundMapping(r.Context(), chi.URLParam(r, "id"), req.Version)
	if err != nil {
		fail(w, r, err)
		return
	}
	a.answerPreview(w, r, m, req.Event)
}

// previewDraft renders a sample event through a definition that is not saved yet.
func (a *API) previewDraft(w http.ResponseWriter, r *http.Request) {
	if !tenantWide(w, r) {
		return
	}
	var req outboundPreviewRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	if !domain.ValidSourceSystem(req.SourceSystem) {
		fail(w, r, validation(map[string]string{"sourceSystem": domain.SourceSystemHint}))
		return
	}
	version := max(req.Version, 1)
	_, canonical, err := parseOutboundDefinition(req.Definition, version)
	if err != nil {
		var p *httpx.Problem
		if errors.As(err, &p) && p.Status == http.StatusUnprocessableEntity {
			httpx.JSON(w, http.StatusOK, previewView{Status: "invalid", Code: p.Code, Detail: p.Detail})
			return
		}
		fail(w, r, err)
		return
	}
	a.answerPreview(w, r, store.OutboundMapping{ID: "00000000-0000-0000-0000-000000000000", SourceSystem: req.SourceSystem, Version: version, Definition: canonical}, req.Event)
}

func (a *API) answerPreview(w http.ResponseWriter, r *http.Request, m store.OutboundMapping, event json.RawMessage) {
	view, err := previewOf(a.render(r, m, event))
	if err != nil {
		a.logFailure(r, "preview outbound mapping failed", err)
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

// recordXref stores the legacy key the legacy ERP assigned to a record the hub pushed. It is idempotent on eventId.
func (a *API) recordXref(w http.ResponseWriter, r *http.Request) {
	p, ok := fromHub(w, r)
	if !ok {
		return
	}
	var req xrefRecordRequest
	if err := httpx.Decode(r, &req); err != nil {
		fail(w, r, err)
		return
	}
	in := store.OutboundXrefRecord{MappingID: req.OutboundMappingID, EventID: req.EventID, LegacyKey: req.LegacyKey, Refs: req.Refs, Actor: p.UserID}
	bad := store.ValidateOutboundRecord(in)
	if req.OutboundMappingID == "" {
		bad["outboundMappingId"] = "is required"
	}
	if len(bad) > 0 {
		fail(w, r, validation(bad))
		return
	}
	if _, err := a.store.RecordOutboundXref(r.Context(), in); err != nil {
		a.logFailure(r, "record outbound xref failed", err)
		fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
