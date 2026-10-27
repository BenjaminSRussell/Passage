package api

import (
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"slices"
	"strconv"
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
	r.With(authz.RequireDuty(dutyRead)).Get(hierarchyTypes, a.listHierarchyTypes)
	r.With(authz.RequireDuty(dutyRead)).Get(hierarchyTypes+"/{id}", hierarchyRoute(store.CodeHierarchyTypeNotFound, "hierarchy type not found", a.getHierarchyType))
	r.With(authz.RequireDuty(dutyRead)).Get(hierarchyVersions+"/{id}", hierarchyRoute(store.CodeHierarchyVersionNotFound, "hierarchy version not found", a.getHierarchyVersion))
	r.With(authz.RequireDuty(dutyHierarchy)).Delete(hierarchyVersions+"/{id}", hierarchyRoute(store.CodeHierarchyVersionNotFound, "hierarchy version not found", a.deleteHierarchyDraft))
	r.With(authz.RequireDuty(dutyRead)).Get(hierarchyTypes+"/{id}/versions", hierarchyRoute(store.CodeHierarchyTypeNotFound, "hierarchy type not found", a.listHierarchyVersions))
	r.With(authz.RequireDuty(dutyRead)).Get(hierarchyTypes+"/{id}/as-of", hierarchyRoute(store.CodeHierarchyTypeNotFound, "hierarchy type not found", a.hierarchyAsOf))
	r.With(authz.RequireDuty(dutyRead)).Get(hierarchyTypes+"/{id}/compare", hierarchyRoute(store.CodeHierarchyTypeNotFound, "hierarchy type not found", a.compareHierarchyVersions))
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
	idemKey, keyOK := idempotencyKeyOf(w, r)
	if !keyOK {
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
	created, err := a.store.CreateHierarchyType(r.Context(), in, idemKey)
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
	idemKey, keyOK := idempotencyKeyOf(w, r)
	if !keyOK {
		return
	}
	principal, _ := tenant.FromContext(r.Context())
	draft, err := a.store.CreateHierarchyDraft(r.Context(), id, idemKey, principal.UserID)
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
	principal, _ := tenant.FromContext(r.Context())
	updated, err := a.store.ReplaceHierarchyNodes(r.Context(), id, *req.Version, nodes, principal.UserID)
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
	idemKey, keyOK := idempotencyKeyOf(w, r)
	if !keyOK {
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
	published, err := a.store.PublishHierarchyVersion(r.Context(), id, *req.Version, effectiveFrom, reason, idemKey, principal.UserID, backdated)
	if err != nil {
		slog.Error("publish hierarchy version failed", "err", err, "versionId", id, "correlationId", middleware.GetReqID(r.Context()))
		var problem *httpx.Problem
		if errors.As(err, &problem) && problem.Code == store.CodeHierarchyMakerChecker {
			a.hierarchyMakerCheckerRefused.Add(1)
		}
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

func (a *API) compareHierarchyVersions(w http.ResponseWriter, r *http.Request, id string) {
	query := r.URL.Query()
	var versionNos [2]int
	for i, field := range []string{"from", "to"} {
		n, err := strconv.Atoi(query.Get(field))
		if err != nil || n < 1 {
			badQuery(w, r, field, "must be a positive integer")
			return
		}
		versionNos[i] = n
	}
	result, err := a.store.CompareHierarchyVersions(r.Context(), id, versionNos[0], versionNos[1])
	if err != nil {
		slog.Error("compare hierarchy versions failed", "err", err, "typeId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, result)
}

func (a *API) listHierarchyTypes(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, after, err := pageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.HierarchyTypeFilter{Purpose: q.Get("purpose"), Status: q.Get("status")}
	for _, c := range []struct {
		field, rule string
		invalid     bool
	}{
		{"purpose", "must be one of " + strings.Join(hierarchyPurposes, ", "), f.Purpose != "" && !slices.Contains(hierarchyPurposes, f.Purpose)},
		{"status", "must be active or inactive", f.Status != "" && !validStatus(f.Status)},
	} {
		if c.invalid {
			badQuery(w, r, c.field, c.rule)
			return
		}
	}
	page, err := a.store.ListHierarchyTypes(r.Context(), f, after, limit)
	if err != nil {
		slog.Error("list hierarchy types failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	body := struct {
		Items      []store.HierarchyType `json:"items"`
		NextCursor string                `json:"nextCursor,omitempty"`
	}{Items: page.Items}
	if page.NextCode != "" {
		body.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(page.NextCode))
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) listHierarchyVersions(w http.ResponseWriter, r *http.Request, id string) {
	limit, cursor, err := pageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	after := 0
	if cursor != "" {
		if after, err = strconv.Atoi(cursor); err != nil || after < 1 {
			badQuery(w, r, "cursor", "must be a positive version number cursor")
			return
		}
	}
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains([]string{"draft", "published", "superseded"}, status) {
		badQuery(w, r, "status", "must be draft, published or superseded")
		return
	}
	page, err := a.store.ListHierarchyVersions(r.Context(), id, store.HierarchyVersionFilter{Status: status}, after, limit)
	if err != nil {
		slog.Error("list hierarchy versions failed", "err", err, "typeId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	body := struct {
		Items      []store.HierarchyVersionSummary `json:"items"`
		NextCursor string                          `json:"nextCursor,omitempty"`
	}{Items: page.Items}
	if page.NextVersionNo > 0 {
		body.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(page.NextVersionNo)))
	}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) getHierarchyType(w http.ResponseWriter, r *http.Request, id string) {
	t, err := a.store.GetHierarchyType(r.Context(), id)
	if err != nil {
		slog.Error("get hierarchy type failed", "err", err, "typeId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, t)
}

func (a *API) getHierarchyVersion(w http.ResponseWriter, r *http.Request, id string) {
	v, err := a.store.GetHierarchyVersion(r.Context(), id)
	if err != nil {
		slog.Error("get hierarchy version failed", "err", err, "versionId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

func (a *API) deleteHierarchyDraft(w http.ResponseWriter, r *http.Request, id string) {
	if rejectScopedCaller(w, r, "delete hierarchy versions") {
		return
	}
	version, err := strconv.Atoi(r.URL.Query().Get("version"))
	if err != nil {
		badQuery(w, r, "version", "must be an integer")
		return
	}
	if err := a.store.DeleteHierarchyDraft(r.Context(), id, version); err != nil {
		slog.Error("delete hierarchy draft failed", "err", err, "versionId", id, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	a.hierarchyDraftsDeleted.Add(1)
	w.WriteHeader(http.StatusNoContent)
}
