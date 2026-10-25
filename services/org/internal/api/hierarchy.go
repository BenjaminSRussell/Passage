package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/org/internal/store"
)

const (
	dutyHierarchy      = "org.hierarchy.write"
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

func (a *API) registerHierarchies(r chi.Router) {
	r.With(authz.RequireDuty(dutyHierarchy)).Post(hierarchyTypes, a.createHierarchyType)
	r.With(authz.RequireDuty(dutyHierarchy)).Post(hierarchyTypes+"/{id}/versions", hierarchyRoute(store.CodeHierarchyTypeNotFound, "hierarchy type not found", a.createHierarchyDraft))
	r.With(authz.RequireDuty(dutyHierarchy)).Put(hierarchyVersions+"/{id}/nodes", hierarchyRoute(store.CodeHierarchyVersionNotFound, "hierarchy version not found", a.replaceHierarchyNodes))
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
			if nodes[i].SortOrder = *n.SortOrder; nodes[i].SortOrder < 0 {
				reject(key+"sortOrder", "must be 0 or greater")
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
