package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/domain"
	"github.com/BenjaminSRussell/passage/services/productcost/internal/store"
)

// Slice 5: the persisted cost structure (FR-39.1) and costing versions
// (FR-39.3, BR-39.2). The handlers decode and validate, then call the store,
// which writes the record, its Idempotency-Key and its outbox event in one
// tenant transaction.

const (
	headerIdempotencyKey = "Idempotency-Key"
	headerReplayed       = "Idempotent-Replayed"
	maxIdempotencyKey    = 200
	defaultPageSize      = 100
	// codeStoreUnavailable answers the persisted routes when no database is attached.
	codeStoreUnavailable = "CST-0799"
)

// Store is the persistence the stored routes need; *store.Postgres implements it.
type Store interface {
	ListComponents(ctx context.Context) ([]store.Component, error)
	CreateComponent(ctx context.Context, in domain.CostComponent, key string) (store.Component, bool, error)
	ListGroups(ctx context.Context) ([]store.Group, error)
	CreateGroup(ctx context.Context, in domain.CostGroup, key string) (store.Group, bool, error)
	ListVersions(ctx context.Context, f store.VersionFilter, after string, limit int) (store.VersionPage, error)
	GetVersion(ctx context.Context, id string) (store.Version, error)
	CreateVersion(ctx context.Context, spec domain.VersionSpec, key string) (store.Version, bool, error)
	TransitionVersion(ctx context.Context, id string, expected int, action string) (store.Version, error)
}

// WithStore attaches the repository. Without one the stored routes answer 503.
func (a *API) WithStore(s Store) *API {
	a.store = s
	return a
}

// storeReady checks the caller is a tenant principal and a store is attached.
func (a *API) storeReady(w http.ResponseWriter, r *http.Request) bool {
	if !guard(w, r) {
		return false
	}
	if a.store == nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, codeStoreUnavailable, "the costing store is not configured"))
		return false
	}
	return true
}

// storeFailure maps a store error: coded domain rules are 422, problems keep
// their status, anything else is a 500.
func storeFailure(w http.ResponseWriter, r *http.Request, err error) {
	var ce *domain.CodedError
	if errors.As(err, &ce) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, ce.Code, ce.Detail))
		return
	}
	httpx.Fail(w, r, err)
}

// idempotencyKey reads the required Idempotency-Key header into fields.
func idempotencyKey(r *http.Request, fields map[string]string) string {
	key := strings.TrimSpace(r.Header.Get(headerIdempotencyKey))
	switch {
	case key == "":
		fields[headerIdempotencyKey] = "is required"
	case len(key) > maxIdempotencyKey:
		fields[headerIdempotencyKey] = "must be at most 200 characters"
	}
	return key
}

// created answers a create: 201 with the record, flagged when it was replayed.
func created(w http.ResponseWriter, v any, replayed bool) {
	if replayed {
		w.Header().Set(headerReplayed, "true")
	}
	httpx.JSON(w, http.StatusCreated, v)
}

type listResponse[T any] struct {
	Items []T `json:"items"`
}

func (a *API) listComponents(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	items, err := a.store.ListComponents(r.Context())
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Component]{Items: items})
}

// createComponent adds a cost component of the split (FR-39.1).
func (a *API) createComponent(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	var in domain.CostComponent
	if !decode(w, r, &in) {
		return
	}
	in = in.Normalize()
	fields := in.Validate()
	key := idempotencyKey(r, fields)
	if !validation(w, r, fields) {
		return
	}
	c, replayed, err := a.store.CreateComponent(r.Context(), in, key)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	if !replayed {
		a.structureChanges.Add(1)
	}
	created(w, c, replayed)
}

func (a *API) listGroups(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	items, err := a.store.ListGroups(r.Context())
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, listResponse[store.Group]{Items: items})
}

// createGroup adds a cost group mapped to a cost component (FR-39.1).
func (a *API) createGroup(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	var in domain.CostGroup
	if !decode(w, r, &in) {
		return
	}
	in = in.Normalize()
	fields := in.Validate()
	key := idempotencyKey(r, fields)
	if !validation(w, r, fields) {
		return
	}
	g, replayed, err := a.store.CreateGroup(r.Context(), in, key)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	if !replayed {
		a.structureChanges.Add(1)
	}
	created(w, g, replayed)
}

var (
	versionStatuses = map[string]bool{domain.VersionOpen: true, domain.VersionBlocked: true, domain.VersionActive: true, domain.VersionClosed: true}
	versionTypes    = map[string]bool{domain.VersionStandard: true, domain.VersionPlanned: true, domain.VersionSimulation: true, domain.VersionConversion: true}
)

// listVersions pages through the stored versions in creation order, filtered
// by legalEntityId, status and type.
func (a *API) listVersions(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.VersionFilter{LegalEntityID: q.Get("legalEntityId"), Status: q.Get("status"), Type: q.Get("type")}
	after := q.Get("after")
	fields := map[string]string{}
	if f.LegalEntityID != "" && !ids.Valid(f.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if f.Status != "" && !versionStatuses[f.Status] {
		fields["status"] = "must be Open, Blocked, Active or Closed"
	}
	if f.Type != "" && !versionTypes[f.Type] {
		fields["type"] = "must be Standard, Planned, Simulation or Conversion"
	}
	if after != "" && !ids.Valid(after) {
		fields["after"] = "must be the next cursor of the previous page"
	}
	limit := defaultPageSize
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxPageSize {
			fields["limit"] = "must be an integer from 1 to 500"
		}
		limit = n
	}
	if !validation(w, r, fields) {
		return
	}
	page, err := a.store.ListVersions(r.Context(), f, after, limit)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

// versionID reads the {id} path parameter; a malformed id cannot exist (404).
func versionID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeVersionNotFound, "costing version "+id+" not found"))
		return "", false
	}
	return id, true
}

func (a *API) getVersion(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	id, ok := versionID(w, r)
	if !ok {
		return
	}
	v, err := a.store.GetVersion(r.Context(), id)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

// inScope reports whether the caller's legal-entity scope (a comma list, or
// empty or * for every entity) covers legalEntityID.
func inScope(p tenant.Principal, legalEntityID string) bool {
	if p.LegalEntityID == "" || p.LegalEntityID == "*" {
		return true
	}
	for _, le := range strings.Split(p.LegalEntityID, ",") {
		if strings.EqualFold(strings.TrimSpace(le), legalEntityID) {
			return true
		}
	}
	return false
}

// createVersion stores a new Open costing version (FR-39.3). Idempotency-Key
// is required (PRD 7.39.5).
func (a *API) createVersion(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	var spec domain.VersionSpec
	if !decode(w, r, &spec) {
		return
	}
	spec = spec.Normalize()
	fields, rule := spec.Validate()
	if fields == nil {
		fields = map[string]string{}
	}
	key := idempotencyKey(r, fields)
	if !validation(w, r, fields) {
		return
	}
	if rule != nil {
		domainFailure(w, r, rule, "fallback")
		return
	}
	if p, _ := tenant.FromContext(r.Context()); !inScope(p, spec.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, domain.CodeLegalEntityScope, "legal entity "+spec.LegalEntityID+" is outside your scope"))
		return
	}
	v, replayed, err := a.store.CreateVersion(r.Context(), spec, key)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	created(w, v, replayed)
}

type storedMoveRequest struct {
	Action  string `json:"action"`
	Version *int   `json:"version"`
}

// storedMove applies action to the stored version named by {id}, under the
// optimistic counter the body carries.
func (a *API) storedMove(w http.ResponseWriter, r *http.Request, action string, fields map[string]string, expected *int) {
	id, ok := versionID(w, r)
	if !ok {
		return
	}
	if expected == nil || *expected < 1 {
		fields["version"] = "is required and must be at least 1"
	}
	if !validation(w, r, fields) {
		return
	}
	v, err := a.store.TransitionVersion(r.Context(), id, *expected, action)
	if err != nil {
		storeFailure(w, r, err)
		return
	}
	a.versionTransitions.Add(1)
	httpx.JSON(w, http.StatusOK, v)
}

// transitionStoredVersion blocks, unblocks or closes a stored version (BR-39.2,
// duty CostingMaintain). Activation is its own route under CostingActivate.
func (a *API) transitionStoredVersion(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	var req storedMoveRequest
	if !decode(w, r, &req) {
		return
	}
	fields := map[string]string{}
	switch req.Action {
	case domain.ActionBlock, domain.ActionUnblock, domain.ActionClose:
	case domain.ActionActivate:
		fields["action"] = "activate with POST /v1/costing/versions/{id}/activate (duty CostingActivate)"
	default:
		fields["action"] = "must be block, unblock or close"
	}
	a.storedMove(w, r, req.Action, fields, req.Version)
}

type storedActivateRequest struct {
	Version *int `json:"version"`
}

// activateStoredVersion moves a stored Open or Blocked version to Active
// (BR-39.2, duty CostingActivate). Simulation versions cannot (CST-0711).
func (a *API) activateStoredVersion(w http.ResponseWriter, r *http.Request) {
	if !a.storeReady(w, r) {
		return
	}
	var req storedActivateRequest
	if !decode(w, r, &req) {
		return
	}
	a.storedMove(w, r, domain.ActionActivate, map[string]string{}, req.Version)
}
