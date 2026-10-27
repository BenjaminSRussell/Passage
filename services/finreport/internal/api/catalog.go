package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/domain"
	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

// --- standard definitions (FR-15.5, FR-15.14) --------------------------------

type standardDTO struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Rows    int    `json:"rows"`
	Columns int    `json:"columns"`
}

// listStandard lists the statutory definitions the service ships.
func (a *API) listStandard(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	out := []standardDTO{}
	for _, def := range domain.StandardDefinitions() {
		out = append(out, standardDTO{Code: def.Code, Name: def.Name, Kind: string(def.Kind), Rows: len(def.Rows), Columns: len(def.Columns)})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

// installStandard publishes, as version 1, every shipped definition whose code
// the tenant does not use yet. Codes in use are skipped, so the action is safe
// to repeat and never replaces a tenant's own edits.
func (a *API) installStandard(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	defs := domain.StandardDefinitions()
	for _, def := range defs {
		if issues := def.Validate(); len(issues) > 0 {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, issues[0].Code, def.Code+": "+issues[0].Field+": "+issues[0].Detail))
			return
		}
	}
	out, err := a.store.InstallStandard(r.Context(), defs, p.UserID)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

// --- derived hierarchies (FR-15.13) ------------------------------------------

// hierarchyVersion reads ?version=draft|latest|n.
func hierarchyVersion(r *http.Request) (int, error) {
	switch v := r.URL.Query().Get("version"); v {
	case "", "latest":
		return store.Latest, nil
	case "draft":
		return store.Draft, nil
	default:
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return 0, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "version must be draft, latest or a positive integer")
		}
		return n, nil
	}
}

func (a *API) listHierarchies(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	items, err := a.store.ListHierarchies(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getHierarchy(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	version, err := hierarchyVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	h, rev, err := a.store.GetHierarchy(r.Context(), chi.URLParam(r, "code"), version)
	if err != nil {
		fail(w, r, err)
		return
	}
	if version == store.Draft {
		w.Header().Set("ETag", etag(rev))
	}
	httpx.JSON(w, http.StatusOK, h)
}

// putHierarchy saves the draft hierarchy. The body is also the JSON a GET
// returns, so a hierarchy exported from one environment imports into another
// (FR-15.13 "JSON import/export like FR-15.11"). When strict is omitted it
// defaults to true for account and category hierarchies and false for
// dimension hierarchies (PRD parameter hierarchy.strict).
func (a *API) putHierarchy(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	code := chi.URLParam(r, "code")
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "unreadable body"))
		return
	}
	var h domain.Hierarchy
	if err := decodeStrict(raw, &h); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", err.Error()))
		return
	}
	var probe struct {
		Strict *bool `json:"strict"`
	}
	if err := json.Unmarshal(raw, &probe); err == nil && probe.Strict == nil {
		h.Strict = h.Subject != domain.SubjectDimension
	}
	if h.Code == "" {
		h.Code = code
	}
	if h.Code != code {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0001", "body code does not match the path"))
		return
	}
	if issues := h.Validate(); len(issues) > 0 {
		httpx.Fail(w, r, issueProblem(issues))
		return
	}
	ifMatch, err := parseIfMatch(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	saved, rev, err := a.store.SaveHierarchy(r.Context(), h, ifMatch, p.UserID)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(rev))
	httpx.JSON(w, http.StatusOK, saved)
}

type publishHierarchyRequest struct {
	Members []domain.Member `json:"members"`
}

// hierarchyAction dispatches POST /hierarchies/{code}:publish. The body lists
// the members to classify: until the chart of accounts and dimension services
// publish their master data to this service (wave 11 and wave 6 handoffs), the
// caller supplies them. Publish fails (REPORT-0013, REPORT-0014) rather than
// storing a membership that breaks the hierarchy rules.
func (a *API) hierarchyAction(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	rest := chi.URLParam(r, "*")
	i := strings.LastIndex(rest, ":")
	if i < 0 || rest[i+1:] != "publish" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "COM-0404", "unknown hierarchy action"))
		return
	}
	code := rest[:i]
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "unreadable body"))
		return
	}
	var body publishHierarchyRequest
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := decodeStrict(raw, &body); err != nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", err.Error()))
			return
		}
	}
	h, m, err := a.store.PublishHierarchy(r.Context(), code, p.UserID, body.Members)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"hierarchy": h, "membership": m})
}

// hierarchyMembers returns the materialized membership of a published version
// (latest by default) and the version it belongs to.
func (a *API) hierarchyMembers(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	version, err := hierarchyVersion(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if version == store.Draft {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "membership exists only for published versions"))
		return
	}
	m, v, err := a.store.HierarchyMembership(r.Context(), chi.URLParam(r, "code"), version)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"version": v, "assigned": m.Assigned, "unassigned": m.Unassigned})
}
