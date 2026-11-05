// Package api exposes the projects HTTP surface. The contract is api/openapi.yaml;
// every handler follows decode → validate → tenant → store → httpx.JSON/Fail.
package api

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/projects/internal/domain"
	"github.com/BenjaminSRussell/passage/services/projects/internal/store"
)

// Problem codes raised by the HTTP layer (see the service README).
const (
	codeValidation      = "COM-0001"
	codePreconditionRq  = "COM-0428"
	codeUnauthenticated = "AUTH-0001"
	codeForbiddenScope  = "AUTH-0003"
	validationDetail    = "request failed validation"

	idempotencyHeader = "Idempotency-Key"
	maxIdempotencyKey = 255
	ifMatchHeader     = "If-Match"
)

// Duties (PRD §8.41.17) guarding the routes.
const (
	DutyMaintain = "projects.maintain"
	DutyManage   = "projects.manage"
	DutyInquire  = "projects.inquire"
	DutyPlan     = "projects.plan"
	DutySetup    = "projects.setup.admin"
	// DutyChangeOrderApprove guards approve, reject and apply of change orders.
	DutyChangeOrderApprove = "projects.change-order.approve"
)

// API holds the HTTP handlers and their store.
type API struct {
	store  store.Store
	gl     store.GL
	ledger ledgerclient.Ledger
}

// New builds the handlers over a store.
func New(s store.Store) *API { return &API{store: s} }

// Register mounts the /v1/projects routes on r and replaces the kernel's static
// /readyz with one that pings the database (/healthz stays the kernel's).
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.Route("/v1/projects", func(r chi.Router) {
		r.With(authz.RequireDuty(DutyMaintain)).Post("/", a.create)
		r.With(authz.RequireDuty(DutyInquire)).Get("/", a.list)
		r.With(authz.RequireDuty(DutyInquire)).Get("/{id}", a.get)
		r.With(authz.RequireDuty(DutyManage)).Post("/{id}/transitions", a.transition)

		// Setup: project groups and posting profiles (FR-41.1, FR-41.5). These
		// static paths win over /{id}; project ids are capitals, so they never clash.
		r.With(authz.RequireDuty(DutyInquire)).Get("/groups", a.listGroups)
		r.With(authz.RequireDuty(DutySetup)).Put("/groups/{groupId}", a.putGroup)
		r.With(authz.RequireDuty(DutyInquire)).Get("/posting-profiles", a.listProfiles)
		r.With(authz.RequireDuty(DutySetup)).Put("/posting-profiles", a.putProfile)
		r.With(authz.RequireDuty(DutyInquire)).Post("/posting-profiles/resolve", a.resolveProfile)

		// Plan versions and WBS (FR-41.2).
		r.With(authz.RequireDuty(DutyInquire)).Get("/{id}/plans", a.listPlans)
		r.With(authz.RequireDuty(DutyPlan)).Post("/{id}/plans", a.createPlan)
		r.With(authz.RequireDuty(DutyInquire)).Get("/{id}/plans/{ver}/wbs", a.getWBS)
		r.With(authz.RequireDuty(DutyPlan)).Put("/{id}/plans/{ver}/wbs", a.putWBS)
		r.With(authz.RequireDuty(DutyPlan)).Post("/{id}/plans/{ver}/schedule", a.scheduleWBS)
		// Simulation transfer into WORKING (FR-41.17, BR-41.14).
		r.With(authz.RequireDuty(DutyInquire)).Get("/{id}/plans/{ver}/transfer", a.previewTransfer)
		r.With(authz.RequireDuty(DutyPlan)).Post("/{id}/plans/{ver}/transfer", a.transferPlan)

		// Change orders and claims (FR-41.18) and the risk register (FR-41.20).
		a.registerChangeOrders(r)
		a.registerRisks(r)
		a.registerForecasts(r)

		// Transactions, invoice proposals and the billing explanation (FR-41.4, FR-41.6).
		a.registerBilling(r)

		// General ledger postings and their reconciliation (FR-41.4, FR-41.9).
		a.registerGL(r)
	})
}

// readyTimeout bounds the readiness database ping.
const readyTimeout = 2 * time.Second

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err)
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// createRequest is the wire shape of POST /v1/projects.
type createRequest struct {
	ID              string  `json:"id"`
	LegalEntityID   string  `json:"legalEntityId"`
	ParentProjectID *string `json:"parentProjectId,omitempty"`
	ProjectGroupID  string  `json:"projectGroupId"`
	ProjectType     string  `json:"projectType"`
	CustomerPartyID *string `json:"customerPartyId,omitempty"`
	ContractID      *string `json:"contractId,omitempty"`
	Currency        string  `json:"currency"`
	ContractValue   string  `json:"contractValue,omitempty"`
	StartDate       string  `json:"startDate"`
	EndDate         *string `json:"endDate,omitempty"`
}

// transitionRequest is the wire shape of POST /v1/projects/{id}/transitions.
type transitionRequest struct {
	To string `json:"to"`
}

type listResponse struct {
	Items      []domain.Project `json:"items"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req createRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	key := strings.TrimSpace(r.Header.Get(idempotencyHeader))
	if key == "" || len(key) > maxIdempotencyKey {
		httpx.Fail(w, r, validation(map[string]string{idempotencyHeader: fmt.Sprintf("header is required, at most %d characters", maxIdempotencyKey)}))
		return
	}
	if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, codeForbiddenScope, "the project's legal entity is outside the caller's scope"))
		return
	}
	var in domain.Project
	in.ID = req.ID
	in.LegalEntityID = req.LegalEntityID
	in.ParentProjectID = req.ParentProjectID
	in.ProjectGroupID = req.ProjectGroupID
	in.ProjectType = domain.ProjectType(req.ProjectType)
	in.CustomerPartyID = req.CustomerPartyID
	in.ContractID = req.ContractID
	in.Currency = req.Currency
	in.ContractValue = req.ContractValue
	in.Stage = domain.StageCreated
	in.StartDate = req.StartDate
	in.EndDate = req.EndDate
	if fields := validateCreate(in); len(fields) > 0 {
		httpx.Fail(w, r, validation(fields))
		return
	}
	out, created, err := a.store.CreateProject(r.Context(), in, key)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	if !created {
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, out)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	out, err := a.store.GetProject(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	limit, after, err := pageParams(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.Filter{Stage: q.Get("stage"), ProjectType: q.Get("projectType")}
	if f.Stage != "" && !domain.ValidStage(domain.Stage(f.Stage)) {
		httpx.Fail(w, r, validation(map[string]string{"stage": "must be CREATED, IN_PROCESS, ON_HOLD or FINISHED"}))
		return
	}
	if f.ProjectType != "" && !domain.ValidProjectType(domain.ProjectType(f.ProjectType)) {
		httpx.Fail(w, r, validation(map[string]string{"projectType": "is not a known project type"}))
		return
	}
	page, err := a.store.ListProjects(r.Context(), f, after, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	body := listResponse{Items: page.Items, NextCursor: encodeCursor(page.NextID)}
	httpx.JSON(w, http.StatusOK, body)
}

func (a *API) transition(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	version, ok := ifMatchVersion(w, r)
	if !ok {
		return
	}
	var req transitionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	to := domain.Stage(req.To)
	if !domain.ValidStage(to) {
		httpx.Fail(w, r, validation(map[string]string{"to": "must be CREATED, IN_PROCESS, ON_HOLD or FINISHED"}))
		return
	}
	out, err := a.store.TransitionProject(r.Context(), chi.URLParam(r, "id"), version, to)
	if err != nil {
		httpx.Fail(w, r, problemFrom(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

// validateCreate returns field-level errors for a create request (empty when valid).
func validateCreate(in domain.Project) map[string]string {
	fields := domain.ValidateCreate(in)
	if !ids.Valid(in.LegalEntityID) {
		fields["legalEntityId"] = "must be a UUID"
	}
	if in.CustomerPartyID != nil && !ids.Valid(*in.CustomerPartyID) {
		fields["customerPartyId"] = "must be a UUID"
	}
	if in.ContractID != nil && !ids.Valid(*in.ContractID) {
		fields["contractId"] = "must be a UUID"
	}
	if in.ParentProjectID != nil && *in.ParentProjectID == "" {
		fields["parentProjectId"] = "must not be empty when given"
	}
	return fields
}

func validation(fields map[string]string) error {
	p := httpx.NewProblem(http.StatusBadRequest, codeValidation, validationDetail)
	p.Errors = fields
	return p
}

// problemFrom maps domain rule violations to 422 problems with their catalog code.
func problemFrom(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
	}
	return err
}

// optionalIfMatch reads If-Match on PUT routes where it is optional: absent
// returns 0 (no precondition), present is parsed like ifMatchVersion.
func optionalIfMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	if strings.TrimSpace(r.Header.Get(ifMatchHeader)) == "" {
		return 0, true
	}
	return ifMatchVersion(w, r)
}

// ifMatchVersion reads the row version from If-Match (PRD §8.41.5: custom
// methods require it; 428 when absent, 400 when malformed).
func ifMatchVersion(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.Header.Get(ifMatchHeader))
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, codePreconditionRq, "If-Match header with the current version is required"))
		return 0, false
	}
	raw = strings.TrimPrefix(raw, "W/")
	raw = strings.Trim(raw, `"`)
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		httpx.Fail(w, r, validation(map[string]string{ifMatchHeader: "must be the version as an ETag, for example \"3\""}))
		return 0, false
	}
	return n, true
}

func setETag(w http.ResponseWriter, version int) {
	w.Header().Set("ETag", fmt.Sprintf("%q", strconv.Itoa(version)))
}

func principal(w http.ResponseWriter, r *http.Request) (tenant.Principal, bool) {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, codeUnauthenticated, "missing or malformed tenant"))
		return p, false
	}
	if p.LegalEntityID != "" && !ids.Valid(p.LegalEntityID) {
		httpx.Fail(w, r, validation(map[string]string{"legalEntityId": "must be a single UUID"}))
		return p, false
	}
	return p, true
}

// requireUser checks that the principal names a user: change orders record
// their creator and decider, and the creator-is-not-approver rule needs both.
func requireUser(w http.ResponseWriter, r *http.Request, p tenant.Principal) bool {
	if strings.TrimSpace(p.UserID) == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, codeUnauthenticated, "a user identity is required"))
		return false
	}
	return true
}

// idempotencyKey reads the required Idempotency-Key header, or returns the field error.
func idempotencyKey(r *http.Request) (string, map[string]string) {
	key := strings.TrimSpace(r.Header.Get(idempotencyHeader))
	if key == "" || len(key) > maxIdempotencyKey {
		return "", map[string]string{idempotencyHeader: fmt.Sprintf("header is required, at most %d characters", maxIdempotencyKey)}
	}
	return key, nil
}

func pageParams(r *http.Request) (int, string, error) {
	q := r.URL.Query()
	limit := store.MaxPageSize
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxPageSize {
			return 0, "", validation(map[string]string{"limit": fmt.Sprintf("must be 1 to %d", store.MaxPageSize)})
		}
		limit = n
	}
	after := ""
	if raw := q.Get("cursor"); raw != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(raw)
		if err != nil {
			return 0, "", validation(map[string]string{"cursor": "is invalid"})
		}
		after = string(decoded)
	}
	return limit, after, nil
}

// encodeCursor turns the last id of a page into the opaque nextCursor ("" when
// no page follows).
func encodeCursor(lastID string) string {
	if lastID == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(lastID))
}

// idPageParams reads limit and cursor for a list keyed by UUID rows: a cursor
// that does not decode to a UUID is 400.
func idPageParams(r *http.Request) (int, string, error) {
	limit, after, err := pageParams(r)
	if err == nil && after != "" && !ids.Valid(after) {
		err = validation(map[string]string{"cursor": "is invalid"})
	}
	return limit, after, err
}
