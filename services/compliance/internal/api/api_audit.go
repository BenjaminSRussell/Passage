package api

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
)

// Audit and break-glass duties (PRD §9.48 duties). BreakGlassRequest stands for
// "any user with an eligible role mapping" until the wave-2 role mapping exists.
const (
	DutyAuditPolicyRun    = "AuditPolicyRun"
	DutyAuditCaseManage   = "AuditCaseManage"
	DutyBreakGlassRequest = "BreakGlassRequest"
)

const (
	maxAuditDocuments  = 100000
	maxAuditViolations = 10000
	maxAuditListItems  = 1000
	maxIDLength        = 200
	// codeAuditViolations is the AUD-0701 warning: {n} documents violate policy {policy}.
	codeAuditViolations = "AUD-0701"
)

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// AuditEvaluateRequest is the body of POST /v1/compliance/audit-policies:evaluate.
// The caller supplies the policy version and the document window it selected;
// nothing is stored.
type AuditEvaluateRequest struct {
	PolicyID  string                 `json:"policyId"`
	Version   int                    `json:"version"`
	Spec      domain.AuditSpec       `json:"spec"`
	Documents []domain.AuditDocument `json:"documents"`
}

// AuditEvaluateResponse lists the findings; each Key is idempotent on (policy
// version, document). Code is AUD-0701 when any document violates the policy.
type AuditEvaluateResponse struct {
	Count    int              `json:"count"`
	Findings []domain.Finding `json:"findings"`
	Code     string           `json:"code,omitempty"`
}

// GroupCasesRequest is the body of POST /v1/compliance/audit-cases:group.
type GroupCasesRequest struct {
	Violations []domain.Violation `json:"violations"`
}

// GroupCasesResponse lists the case drafts to create.
type GroupCasesResponse struct {
	Cases []domain.CaseDraft `json:"cases"`
}

// CaseTransitionRequest is the body of POST /v1/compliance/audit-cases:transition:
// the case's current status, resolution and closing time, and the action. For
// RESOLVE_* the resolution and recovery are the inputs of the action; for CLOSE
// and ARCHIVE they are the values already on the case.
type CaseTransitionRequest struct {
	Status         domain.CaseStatus `json:"status"`
	Action         domain.CaseAction `json:"action"`
	ResolutionCode string            `json:"resolutionCode,omitempty"`
	RecoveryAmount decimal.Decimal   `json:"recoveryAmount"`
	Currency       string            `json:"currency,omitempty"`
	ClosedAt       *time.Time        `json:"closedAt,omitempty"`
}

// CaseTransitionResponse is the case state after the action, at AsOf.
type CaseTransitionResponse struct {
	Status         domain.CaseStatus `json:"status"`
	ResolutionCode string            `json:"resolutionCode,omitempty"`
	RecoveryAmount decimal.Decimal   `json:"recoveryAmount"`
	Currency       string            `json:"currency,omitempty"`
	ClosedAt       *time.Time        `json:"closedAt,omitempty"`
	AsOf           time.Time         `json:"asOf"`
}

// ElevatedPlanRequest is the body of POST /v1/security/elevated-sessions:plan.
// The requester is the calling user; TargetUserID defaults to the requester.
type ElevatedPlanRequest struct {
	TargetUserID     string   `json:"targetUserId,omitempty"`
	Roles            []string `json:"roles"`
	ReasonCode       string   `json:"reasonCode"`
	TicketRef        string   `json:"ticketRef"`
	RequestedMinutes int      `json:"requestedMinutes,omitempty"`
}

// ElevatedPlanResponse is the session the request would open.
type ElevatedPlanResponse struct {
	RequesterID  string `json:"requesterId"`
	TargetUserID string `json:"targetUserId"`
	domain.ElevatedPlan
}

var (
	caseStatuses = map[domain.CaseStatus]bool{
		domain.CaseOpen: true, domain.CaseInReview: true, domain.CaseResolvedNoIssue: true,
		domain.CaseResolvedIssue: true, domain.CaseClosed: true, domain.CaseArchived: true,
	}
	caseActions = map[domain.CaseAction]bool{
		domain.ActionStartReview: true, domain.ActionResolveNoIssue: true, domain.ActionResolveIssue: true,
		domain.ActionClose: true, domain.ActionArchive: true,
	}
	// datedRules read the document date; a document without one cannot be judged.
	datedRules = map[string]bool{domain.AuditRuleWeekendHoliday: true, domain.AuditRuleAggregate: true, domain.AuditRuleDuplicate: true}
)

func validID(s string) bool { return strings.TrimSpace(s) != "" && len(s) <= maxIDLength }

// evaluateAudit runs one audit rule over the supplied documents (FR-48.14).
func (a *API) evaluateAudit(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req AuditEvaluateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !ids.Valid(req.PolicyID) {
		fields["policyId"] = "must be a UUID"
	}
	if req.Version < 1 {
		fields["version"] = "must be at least 1"
	}
	if req.Spec.Type == "" {
		fields["spec.type"] = "is required"
	}
	if len(req.Spec.Keywords) > maxAuditListItems || len(req.Spec.WatchList) > maxAuditListItems ||
		len(req.Spec.Holidays) > maxAuditListItems || len(req.Spec.Limits) > maxAuditListItems {
		fields["spec"] = fmt.Sprintf("at most %d keywords, watch-list entries, holidays or limits", maxAuditListItems)
	}
	if len(req.Documents) > maxAuditDocuments {
		fields["documents"] = fmt.Sprintf("at most %d documents per run; split the window", maxAuditDocuments)
	} else {
		seen := make(map[string]bool, len(req.Documents))
		for i, d := range req.Documents {
			switch {
			case !validID(d.ID):
				fields[fmt.Sprintf("documents[%d].id", i)] = fmt.Sprintf("is required (1..%d characters)", maxIDLength)
			case seen[d.ID]:
				fields[fmt.Sprintf("documents[%d].id", i)] = "is repeated; each document is evaluated once"
			case datedRules[req.Spec.Type] && d.Date.IsZero():
				fields[fmt.Sprintf("documents[%d].date", i)] = "is required for " + req.Spec.Type
			}
			if len(fields) > 0 {
				break
			}
			seen[d.ID] = true
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	findings, err := domain.EvaluateAudit(strings.ToLower(req.PolicyID), req.Version, req.Spec, req.Documents)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	if findings == nil {
		findings = []domain.Finding{}
	}
	out := AuditEvaluateResponse{Count: len(findings), Findings: findings}
	if out.Count > 0 {
		out.Code = codeAuditViolations
	}
	httpx.JSON(w, http.StatusOK, out)
}

// groupCases groups unassigned violations into audit case drafts (FR-48.15).
func (a *API) groupCases(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req GroupCasesRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if len(req.Violations) > maxAuditViolations {
		fields["violations"] = fmt.Sprintf("at most %d violations per grouping", maxAuditViolations)
	} else {
		seen := make(map[string]bool, len(req.Violations))
		for i, v := range req.Violations {
			at := fmt.Sprintf("violations[%d]", i)
			switch {
			case !validID(v.ID):
				fields[at+".id"] = "is required"
			case seen[v.ID]:
				fields[at+".id"] = "is repeated; a violation belongs to at most one case"
			case !validID(v.PolicyID):
				fields[at+".policyId"] = "is required"
			case !validID(v.DocumentID):
				fields[at+".documentId"] = "is required"
			case v.RuleType == domain.AuditRuleDuplicate && !validID(v.PartyID):
				fields[at+".partyId"] = "is required for DUPLICATE: duplicate cases group per vendor"
			}
			if len(fields) > 0 {
				break
			}
			seen[v.ID] = true
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	drafts, err := domain.GroupViolations(req.Violations)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, GroupCasesResponse{Cases: drafts})
}

// transitionCase applies one action of the audit case status machine (FR-48.15)
// at the server time, with the tenant's audit.case_archive_days.
func (a *API) transitionCase(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req CaseTransitionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	at := a.now()
	fields := map[string]string{}
	if !caseStatuses[req.Status] {
		fields["status"] = "must be OPEN, IN_REVIEW, RESOLVED_NO_ISSUE, RESOLVED_ISSUE, CLOSED or ARCHIVED"
	}
	if !caseActions[req.Action] {
		fields["action"] = "must be START_REVIEW, RESOLVE_NO_ISSUE, RESOLVE_ISSUE, CLOSE or ARCHIVE"
	}
	if req.Currency != "" && !currencyCode.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 code"
	}
	if req.ClosedAt != nil && req.ClosedAt.After(at) {
		fields["closedAt"] = "must not be in the future"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	state := domain.CaseState{Status: req.Status, ResolutionCode: req.ResolutionCode, RecoveryAmount: req.RecoveryAmount, Currency: req.Currency}
	if req.ClosedAt != nil {
		c := req.ClosedAt.UTC()
		state.ClosedAt = &c
	}
	next, err := domain.ApplyCaseAction(state, domain.CaseTransition{
		Action: req.Action, ResolutionCode: req.ResolutionCode, RecoveryAmount: req.RecoveryAmount,
		Currency: req.Currency, Now: at, ArchiveDays: a.cfg.CaseArchiveDays,
	})
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, CaseTransitionResponse{
		Status: next.Status, ResolutionCode: next.ResolutionCode,
		RecoveryAmount: next.RecoveryAmount, Currency: next.Currency, ClosedAt: next.ClosedAt, AsOf: at,
	})
}

// planElevated validates a break-glass request of the calling user and returns
// the session it would open (FR-48.23, BR-48.19). Nothing is stored or issued.
func (a *API) planElevated(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	p, _ := tenant.FromContext(r.Context())
	requester := strings.ToLower(p.UserID)
	if !ids.Valid(requester) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "a user identity is required"))
		return
	}
	var req ElevatedPlanRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	target := strings.ToLower(strings.TrimSpace(req.TargetUserID))
	if target == "" {
		target = requester
	}
	fields := map[string]string{}
	if !ids.Valid(target) {
		fields["targetUserId"] = "must be a UUID"
	}
	if len(req.Roles) > maxRoles {
		fields["roles"] = fmt.Sprintf("at most %d roles", maxRoles)
	}
	if len(req.TicketRef) > maxText {
		fields["ticketRef"] = fmt.Sprintf("at most %d characters", maxText)
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	plan, err := domain.PlanElevatedSession(domain.ElevatedRequest{
		RequesterID: requester, TargetUserID: target, Roles: req.Roles,
		ReasonCode: req.ReasonCode, TicketRef: strings.TrimSpace(req.TicketRef), RequestedMinutes: req.RequestedMinutes,
		TenantMaxMinutes: a.cfg.BreakGlassMaxMinutes, SelfActivate: a.cfg.BreakGlassSelfActivate,
		ReviewDays: a.cfg.BreakGlassReviewDays, Start: a.now(),
	})
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, ElevatedPlanResponse{RequesterID: requester, TargetUserID: target, ElevatedPlan: plan})
}
