package api

import (
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	dutyHierarchy      = "org.hierarchy.write"
	dutyPublish        = "org.hierarchy.publish"
	dutyBackdate       = "org.hierarchy.backdate"
	hierarchyTypes     = "/v1/org/hierarchy-types"
	hierarchyVersions  = "/v1/org/hierarchy-versions"
	maxNodeShapeErrors = 20
)

var hierarchyPurposes = []string{"reporting", "procurement", "expenditure", "security", "centralized_payments", "custom"}

type createHierarchyTypeRequest struct {
	Code            string   `json:"code"`
	Name            string   `json:"name"`
	Purposes        []string `json:"purposes"`
	BackdateAllowed *bool    `json:"backdateAllowed"`
}

type replaceHierarchyNodesRequest struct {
	Version *int `json:"version"`
	Nodes   []struct {
		OrgUnitID       string  `json:"orgUnitId"`
		ParentOrgUnitID *string `json:"parentOrgUnitId"`
		SortOrder       *int    `json:"sortOrder"`
	} `json:"nodes"`
}

type publishHierarchyVersionRequest struct {
	Version       *int   `json:"version"`
	EffectiveFrom string `json:"effectiveFrom"`
	Reason        string `json:"reason"`
}

func (a *API) registerHierarchies(r chi.Router) {
	r.With(authz.RequireDuty(dutyHierarchy)).Post(hierarchyTypes, a.createHierarchyType)
	r.With(authz.RequireDuty(dutyHierarchy)).Post(hierarchyTypes+"/{id}/versions", hierarchyRoute(store.CodeHierarchyTypeNotFound, "hierarchy type not found", a.createHierarchyDraft))
	r.With(authz.RequireDuty(dutyHierarchy)).Put(hierarchyVersions+"/{id}/nodes", hierarchyRoute(store.CodeHierarchyVersionNotFound, "hierarchy version not found", a.replaceHierarchyNodes))
	r.With(authz.RequireDuty(dutyPublish)).Post(hierarchyVersions+"/{id}:publish", hierarchyRoute(store.CodeHierarchyVersionNotFound, "hierarchy version not found", a.publishHierarchyVersion))
	r.With(authz.RequireDuty(dutyRead)).Get(hierarchyTypes+"/{id}/as-of", hierarchyRoute(store.CodeHierarchyTypeNotFound, "hierarchy type not found", a.hierarchyAsOf))
}

func hierarchyRoute(notFoundCode, notFoundDetail string, next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !validTenant(w, r) {
			return
		}
		id := chi.URLParam(r, "id")
		if !canonicalUUID(id) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, notFoundCode, notFoundDetail))
			return
		}
		next(w, r, id)
	}
}

func validatePurposes(purposes []string) string {
	if len(purposes) == 0 {
		return "must contain at least one purpose"
	}
	for i, p := range purposes {
		if !slices.Contains(hierarchyPurposes, p) {
			return "must use only " + strings.Join(hierarchyPurposes, ", ")
		}
		if slices.Contains(purposes[:i], p) {
			return "must not contain duplicates"
		}
	}
	return ""
}

func (a *API) createHierarchyType(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) || rejectScopedCaller(w, r, "create hierarchy types") {
		return
	}
	var req createHierarchyTypeRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	in := store.HierarchyType{Code: strings.ToUpper(req.Code), Name: strings.TrimSpace(req.Name), Purposes: req.Purposes}
	fields := validateShared(&in.Name, nil, nil, nil)
	if !operatingUnitCode.MatchString(in.Code) {
		fields["code"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -, starting with A-Z or 0-9"
	}
	if msg := validatePurposes(in.Purposes); msg != "" {
		fields["purposes"] = msg
	}
	if req.BackdateAllowed != nil {
		in.BackdateAllowed = *req.BackdateAllowed
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	created, err := a.store.CreateHierarchyType(r.Context(), in, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("create hierarchy type failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.hierarchyTypeCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, created)
}

func (a *API) createHierarchyDraft(w http.ResponseWriter, r *http.Request, id string) {
	if rejectScopedCaller(w, r, "create hierarchy versions") {
		return
	}
	draft, err := a.store.CreateHierarchyDraft(r.Context(), id, r.Header.Get(idempotencyKey))
	if err != nil {
		slog.Error("create hierarchy draft failed", "err", err, "typeId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.hierarchyDraftCreated.Add(1)
	httpx.JSON(w, http.StatusCreated, draft)
}

func (a *API) replaceHierarchyNodes(w http.ResponseWriter, r *http.Request, id string) {
	if rejectScopedCaller(w, r, "edit hierarchy versions") {
		return
	}
	var req replaceHierarchyNodesRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Version == nil {
		fields["version"] = "is required"
	}
	reported := 0
	reject := func(field, msg string) {
		if reported < maxNodeShapeErrors {
			fields[field] = msg
			reported++
		}
	}
	nodes := make([]store.HierarchyNode, len(req.Nodes))
	for i, n := range req.Nodes {
		key := fmt.Sprintf("nodes[%d].", i)
		nodes[i] = store.HierarchyNode{OrgUnitID: strings.ToLower(n.OrgUnitID), SortOrder: i}
		if !canonicalUUID(n.OrgUnitID) {
			reject(key+"orgUnitId", "must be a UUID")
		}
		check := map[string]string{}
		optionalID(check, key+"parentOrgUnitId", n.ParentOrgUnitID, true)
		for field, msg := range check {
			reject(field, msg)
		}
		if n.ParentOrgUnitID != nil && *n.ParentOrgUnitID != "" {
			parent := strings.ToLower(*n.ParentOrgUnitID)
			nodes[i].ParentOrgUnitID = &parent
		}
		if n.SortOrder != nil {
			if nodes[i].SortOrder = *n.SortOrder; nodes[i].SortOrder < 0 || nodes[i].SortOrder > math.MaxInt32 {
				reject(key+"sortOrder", "must be between 0 and 2147483647")
			}
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	updated, err := a.store.ReplaceHierarchyNodes(r.Context(), id, *req.Version, nodes)
	if err != nil {
		slog.Error("replace hierarchy nodes failed", "err", err, "versionId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.hierarchyNodesReplaced.Add(1)
	httpx.JSON(w, http.StatusOK, updated)
}

func (a *API) publishHierarchyVersion(w http.ResponseWriter, r *http.Request, id string) {
	if rejectScopedCaller(w, r, "publish hierarchy versions") {
		return
	}
	var req publishHierarchyVersionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if req.Version == nil {
		fields["version"] = "is required"
	}
	effectiveFrom, err := time.Parse(dateLayout, req.EffectiveFrom)
	if err != nil {
		fields["effectiveFrom"] = "must be a date in YYYY-MM-DD form"
	}
	reason := strings.TrimSpace(req.Reason)
	today := a.now().UTC().Truncate(24 * time.Hour)
	backdated := err == nil && effectiveFrom.Before(today)
	if length := utf8.RuneCountInString(reason); !backdated && reason != "" && (length < 10 || length > 500) {
		fields["reason"] = "must be 10 to 500 characters"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	principal, _ := tenant.FromContext(r.Context())
	if backdated && !principal.HasDuty(dutyBackdate) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, store.CodeHierarchyBackdateDuty, "Backdated publish requires the Backdate duty and a reason."))
		return
	}
	published, err := a.store.PublishHierarchyVersion(r.Context(), id, *req.Version, effectiveFrom, reason, r.Header.Get(idempotencyKey), principal.UserID, backdated)
	if err != nil {
		slog.Error("publish hierarchy version failed", "err", err, "versionId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	a.hierarchyVersionsPublished.Add(1)
	httpx.JSON(w, http.StatusOK, published)
}

func (a *API) hierarchyAsOf(w http.ResponseWriter, r *http.Request, id string) {
	query := r.URL.Query()
	date, err := time.Parse(dateLayout, query.Get("date"))
	if err != nil {
		badQuery(w, r, "date", "must be a date in YYYY-MM-DD form")
		return
	}
	node := query.Get("node")
	if !canonicalUUID(node) {
		badQuery(w, r, "node", "must be a UUID")
		return
	}
	result, err := a.store.HierarchyAsOf(r.Context(), id, date, strings.ToLower(node))
	if err != nil {
		slog.Error("hierarchy as-of failed", "err", err, "typeId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.hierarchyAsOfQueries.Add(1)
	httpx.JSON(w, http.StatusOK, result)
}
