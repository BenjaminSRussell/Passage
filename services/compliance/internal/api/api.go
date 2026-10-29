// Package api exposes the compliance HTTP surface for segregation of duties:
// rule listing, pre-save simulation, evaluation of a user's effective roles,
// the conflict listing and the mitigation and risk-acceptance workflow
// (FR-48.4, FR-48.5, BR-48.1).
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/store"
)

// Duties (PRD §9.48.5 and §9.48.6).
const (
	DutySoDReview     = "SoDReview"
	DutySoDApprove    = "SoDApprove"
	DutySoDAdmin      = "SoDAdmin"
	DutySecurityAdmin = "SecurityAdmin"
)

const (
	readyTimeout = 500 * time.Millisecond
	maxRoles     = 200
	maxDuties    = 500
	maxText      = 2000
	codeInvalid  = "COM-0002"
)

// Store is the persistence the handlers need (implemented by store.Postgres).
type Store interface {
	Ping(ctx context.Context) error
	Evaluate(ctx context.Context, userID string, roles []domain.Role, now time.Time) ([]store.Conflict, error)
	KnownStates(ctx context.Context, userID string, now time.Time) (map[string]string, error)
	List(ctx context.Context, f store.Filter, now time.Time) (store.Page, error)
	Get(ctx context.Context, id string, now time.Time) (store.ConflictDetail, error)
	Mitigate(ctx context.Context, id string, expected int, in store.MitigationInput, tenantCapDays int, key string, now time.Time) (store.ConflictDetail, error)
	AcceptRisk(ctx context.Context, id string, expected int, reason string, now time.Time) (store.ConflictDetail, error)
}

// Config carries the tenant policies the service reads from its environment
// until per-tenant settings exist (sod.block_high, sod.max_mitigation_days).
type Config struct {
	BlockHigh         bool
	MaxMitigationDays int
}

// API holds the handler dependencies and counters.
type API struct {
	store  Store
	cfg    Config
	rules  func() []domain.Rule
	now    func() time.Time
	sims   atomic.Int64
	blocks atomic.Int64
}

// New builds the API.
func New(s Store, cfg Config) *API {
	if cfg.MaxMitigationDays <= 0 || cfg.MaxMitigationDays > domain.MaxMitigationDaysCeiling {
		cfg.MaxMitigationDays = domain.DefaultMaxMitigationDays
	}
	return &API{store: s, cfg: cfg, rules: domain.SeedRules, now: func() time.Time { return time.Now().UTC() }}
}

// SimulateRequest is the body of POST /v1/sod/simulate. Roles carry their
// duties inline until the wave-2 role catalogue can be read.
type SimulateRequest struct {
	UserID        string        `json:"userId"`
	CurrentRoles  []domain.Role `json:"currentRoles"`
	ProposedRoles []domain.Role `json:"proposedRoles"`
}

// SimulateResponse reports the conflicts a save would introduce and whether
// the save is blocked.
type SimulateResponse struct {
	NewConflicts    []domain.Conflict `json:"newConflicts"`
	Blocked         bool              `json:"blocked"`
	BlockingRuleIDs []string          `json:"blockingRuleIds"`
	Code            string            `json:"code,omitempty"`
}

// EvaluateRequest is the body of POST /v1/sod/evaluations: a user's effective
// roles after an assignment change.
type EvaluateRequest struct {
	UserID string        `json:"userId"`
	Roles  []domain.Role `json:"roles"`
}

// EvaluateResponse lists every conflict recorded for the user after the evaluation.
type EvaluateResponse struct {
	UserID    string           `json:"userId"`
	Conflicts []store.Conflict `json:"conflicts"`
}

// AcceptRiskRequest is the body of POST /v1/sod/conflicts/{id}/accept-risk.
type AcceptRiskRequest struct {
	Reason string `json:"reason"`
}

// Register mounts the routes on r.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(DutySoDReview)).Get("/v1/sod/rules", a.listRules)
	r.With(authz.RequireDuty(DutySecurityAdmin)).Post("/v1/sod/simulate", a.simulate)
	r.With(authz.RequireDuty(DutySecurityAdmin)).Post("/v1/sod/evaluations", a.evaluate)
	r.With(authz.RequireDuty(DutySoDReview)).Get("/v1/sod/conflicts", a.listConflicts)
	r.With(authz.RequireDuty(DutySoDReview)).Get("/v1/sod/conflicts/{id}", a.getConflict)
	r.With(authz.RequireDuty(DutySoDApprove)).Post("/v1/sod/conflicts/{id}/mitigations", a.mitigate)
	r.With(authz.RequireDuty(DutySoDAdmin)).Post("/v1/sod/conflicts/{id}/accept-risk", a.acceptRisk)
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (a *API) listRules(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": a.rules()})
}

func (a *API) simulate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req SimulateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := validateRoles(req.UserID, append(append([]domain.Role{}, req.CurrentRoles...), req.ProposedRoles...)); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	// Only mitigations the service recorded can exempt a conflict; callers cannot claim one.
	known, err := a.store.KnownStates(r.Context(), strings.ToLower(req.UserID), a.now())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	res := domain.Simulate(strings.ToLower(req.UserID), a.rules(), req.CurrentRoles, req.ProposedRoles, known, a.cfg.BlockHigh)
	a.sims.Add(1)
	out := SimulateResponse{NewConflicts: res.NewConflicts, Blocked: res.Blocked, BlockingRuleIDs: res.BlockingRuleIDs}
	if res.Blocked {
		a.blocks.Add(1)
		out.Code = domain.CodeBlocked
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) evaluate(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req EvaluateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if err := validateRoles(req.UserID, req.Roles); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	user := strings.ToLower(req.UserID)
	conflicts, err := a.store.Evaluate(r.Context(), user, req.Roles, a.now())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, EvaluateResponse{UserID: user, Conflicts: conflicts})
}

func validState(s string) bool {
	switch s {
	case domain.StateOpen, domain.StateMitigated, domain.StateResolved, domain.StateAcceptedRisk:
		return true
	}
	return false
}

func validSeverity(s string) bool {
	switch s {
	case domain.SeverityHigh, domain.SeverityMedium, domain.SeverityLow:
		return true
	}
	return false
}

func (a *API) listConflicts(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.Filter{State: q.Get("state"), Severity: q.Get("severity"), UserID: strings.ToLower(q.Get("userId")), RuleID: q.Get("ruleId"), Cursor: q.Get("cursor")}
	fields := map[string]string{}
	if f.State != "" && !validState(f.State) {
		fields["state"] = "must be OPEN, MITIGATED, RESOLVED or ACCEPTED_RISK"
	}
	if f.Severity != "" && !validSeverity(f.Severity) {
		fields["severity"] = "must be HIGH, MEDIUM or LOW"
	}
	if f.UserID != "" && !ids.Valid(f.UserID) {
		fields["userId"] = "must be a UUID"
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxPageSize {
			fields["limit"] = fmt.Sprintf("must be 1..%d", store.MaxPageSize)
		}
		f.Limit = n
	}
	if f.Cursor != "" {
		if _, _, err := store.DecodeCursor(f.Cursor); err != nil {
			fields["cursor"] = "is malformed"
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	page, err := a.store.List(r.Context(), f, a.now())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getConflict(w http.ResponseWriter, r *http.Request) {
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	c, err := a.store.Get(r.Context(), id, a.now())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, c.Version)
	httpx.JSON(w, http.StatusOK, c)
}

func (a *API) mitigate(w http.ResponseWriter, r *http.Request) {
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		httpx.Fail(w, r, invalid(map[string]string{"Idempotency-Key": "is required (1..200 characters)"}))
		return
	}
	var in store.MitigationInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if strings.TrimSpace(in.ControlRef) == "" || len(in.ControlRef) > maxText {
		fields["controlRef"] = "is required"
	}
	if strings.TrimSpace(in.Reason) == "" || len(in.Reason) > maxText {
		fields["reason"] = "is required"
	}
	if in.ExpiresAt.IsZero() {
		fields["expiresAt"] = "is required (RFC 3339)"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	c, err := a.store.Mitigate(r.Context(), id, version, in, a.cfg.MaxMitigationDays, key, a.now())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, c.Version)
	httpx.JSON(w, http.StatusCreated, c)
}

func (a *API) acceptRisk(w http.ResponseWriter, r *http.Request) {
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req AcceptRiskRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if strings.TrimSpace(req.Reason) == "" || len(req.Reason) > maxText {
		httpx.Fail(w, r, invalid(map[string]string{"reason": "is required"}))
		return
	}
	c, err := a.store.AcceptRisk(r.Context(), id, version, req.Reason, a.now())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, c.Version)
	httpx.JSON(w, http.StatusOK, c)
}

func validateRoles(userID string, roles []domain.Role) error {
	fields := map[string]string{}
	if !ids.Valid(userID) {
		fields["userId"] = "must be a UUID"
	}
	if len(roles) > maxRoles {
		fields["roles"] = fmt.Sprintf("at most %d roles", maxRoles)
	}
	for _, role := range roles {
		if strings.TrimSpace(role.Name) == "" {
			fields["roles"] = "every role needs a name"
		}
		if len(role.Duties) > maxDuties {
			fields["duties"] = fmt.Sprintf("at most %d duties per role", maxDuties)
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return invalid(fields)
}

func invalid(fields map[string]string) error {
	p := httpx.NewProblem(http.StatusBadRequest, codeInvalid, "request failed validation")
	p.Errors = fields
	return p
}

// domainProblem maps a domain rule violation to 422 with its SOD code.
func domainProblem(err error) error {
	var de *domain.Error
	if errors.As(err, &de) {
		return httpx.NewProblem(http.StatusUnprocessableEntity, de.Code, de.Detail)
	}
	return err
}

func conflictID(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !validTenant(w, r) {
		return "", false
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, invalid(map[string]string{"id": "must be a UUID"}))
		return "", false
	}
	return id, true
}

func setETag(w http.ResponseWriter, version int) {
	w.Header().Set("ETag", fmt.Sprintf("%q", strconv.Itoa(version)))
}

// ifMatch reads the required If-Match version: missing is 428, malformed 400,
// stale is reported by the store as 412.
func ifMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, store.CodeVersionConflict, "If-Match with the current ETag is required"))
		return 0, false
	}
	v, err := strconv.Atoi(strings.Trim(strings.TrimPrefix(raw, "W/"), `"`))
	if err != nil || v < 1 {
		httpx.Fail(w, r, invalid(map[string]string{"If-Match": `must be an ETag such as "3"`}))
		return 0, false
	}
	return v, true
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

// Metrics serves the admin-port counters.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# TYPE compliance_sod_simulations_total counter\ncompliance_sod_simulations_total %d\n", a.sims.Load())
		fmt.Fprintf(w, "# TYPE compliance_sod_blocked_total counter\ncompliance_sod_blocked_total %d\n", a.blocks.Load())
	})
}
