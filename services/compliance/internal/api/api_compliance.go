package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/domain"
	"github.com/BenjaminSRussell/passage/services/compliance/internal/store"
)

// Privacy, certification and export governance duties (PRD §9.48.5, §9.48.17).
const (
	DutyPrivacyIntake  = "PrivacyIntake"
	DutyPrivacyOfficer = "PrivacyOfficer"
	DutyComplianceView = "ComplianceView"
)

const (
	maxHolds          = 50
	maxAttributes     = 20
	maxRecordFields   = 200
	maxCampaignLines  = 5000
	maxReadFields     = 1000
	maxReadRules      = 500
	maxPolicyClasses  = 10
	maxPolicyActions  = 6
	maxNotifiedRegime = 20
)

// ErasurePreviewRequest is the body of POST /v1/privacy/requests/{id}/erasure-preview:
// one record of the subject as a domain holds it.
type ErasurePreviewRequest struct {
	Record map[string]string `json:"record"`
}

// CampaignPlanRequest is the body of POST /v1/sod/campaigns:plan. WindowDays 0
// takes the 14-day default (certification.window_days).
type CampaignPlanRequest struct {
	StartsAt   time.Time `json:"startsAt"`
	WindowDays int       `json:"windowDays"`
}

// CampaignPlanResponse carries the close instant of a planned campaign.
type CampaignPlanResponse struct {
	StartsAt time.Time `json:"startsAt"`
	ClosesAt time.Time `json:"closesAt"`
}

// CampaignCloseRequest is the body of POST /v1/sod/campaigns:close: the
// decisions so far and the campaign's close instant. Status is computed at the
// server time.
type CampaignCloseRequest struct {
	Entries    []domain.CampaignEntry `json:"entries"`
	AutoRevoke bool                   `json:"autoRevoke"`
	ClosesAt   time.Time              `json:"closesAt"`
}

// CampaignCloseResponse is the close result at AsOf.
type CampaignCloseResponse struct {
	domain.CampaignCloseResult
	AsOf time.Time `json:"asOf"`
}

// IncidentTransitionResponse is the incident step at AsOf.
type IncidentTransitionResponse struct {
	domain.IncidentTransitionOutcome
	AsOf time.Time `json:"asOf"`
}

// ReadClassifyRequest is the body of POST /v1/compliance/read-log:classify. Rules
// default to the shipped read-log policy; retentionDays 0 means 730.
type ReadClassifyRequest struct {
	Fields        []string          `json:"fields"`
	Rules         []domain.ReadRule `json:"rules,omitempty"`
	RetentionDays int               `json:"retentionDays"`
}

var (
	privacyActions = map[domain.PrivacyAction]bool{
		domain.ActionVerify: true, domain.ActionDiscover: true, domain.ActionReleaseHold: true, domain.ActionApprove: true,
		domain.ActionDeliver: true, domain.PrivacyActionClose: true, domain.ActionReject: true,
	}
)

// registerCompliance mounts the subject request, retention, certification,
// incident and export governance routes. Subject requests are stored; the
// others decide from the request body and the server time alone.
func (a *API) registerCompliance(r chi.Router) {
	r.With(authz.RequireDuty(DutyPrivacyIntake)).Post("/v1/privacy/requests", a.intakeRequest)
	r.With(authz.RequireDuty(DutyPrivacyOfficer)).Get("/v1/privacy/requests", a.listRequests)
	r.With(authz.RequireDuty(DutyPrivacyOfficer)).Get("/v1/privacy/requests/{id}", a.getRequest)
	r.With(authz.RequireDuty(DutyPrivacyOfficer)).Post("/v1/privacy/requests/{id}/actions", a.actOnRequest)
	r.With(authz.RequireDuty(DutyPrivacyOfficer)).Post("/v1/privacy/requests/{id}/erasure-preview", a.previewErasure)
	r.With(authz.RequireDuty(DutyPrivacyOfficer)).Post("/v1/privacy/retention:purge-check", a.purgeCheck)
	r.With(authz.RequireDuty(DutySoDAdmin)).Post("/v1/sod/campaigns:plan", a.planCampaign)
	r.With(authz.RequireDuty(DutySoDAdmin)).Post("/v1/sod/campaigns:close", a.closeCampaign)
	r.With(authz.RequireDuty(DutyIncidentManage)).Post("/v1/security/incidents:transition", a.transitionIncident)
	r.With(authz.RequireDuty(DutyComplianceView)).Post("/v1/compliance/export-governance:evaluate", a.evaluateExportGovernance)
	r.With(authz.RequireDuty(DutyComplianceView)).Post("/v1/compliance/read-log:classify", a.classifyReadLog)
}

// intakeRequest records a subject request (FR-48.10) with its deadline.
func (a *API) intakeRequest(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || len(key) > 200 {
		httpx.Fail(w, r, invalid(map[string]string{"Idempotency-Key": "is required (1..200 characters)"}))
		return
	}
	var in store.IntakeInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	at := a.now()
	fields := map[string]string{}
	if !domain.ValidPrivacyType(in.Type) {
		fields["type"] = "must be ACCESS, PORTABILITY, ERASURE, RECTIFICATION or RESTRICTION"
	}
	if !domain.ValidRegulation(in.Regulation) {
		fields["regulation"] = "must be GDPR or CCPA"
	}
	if !validID(in.SubjectRef) {
		fields["subjectRef"] = fmt.Sprintf("is required (1..%d characters)", maxIDLength)
	}
	if in.ReceivedAt.After(at) {
		fields["receivedAt"] = "must not be in the future"
	}
	if in.DeadlineDays < 0 {
		fields["deadlineDays"] = "must be positive"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	out, err := a.store.Intake(r.Context(), in, key, at)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusCreated, out)
}

func (a *API) listRequests(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.RequestFilter{State: q.Get("state"), Type: q.Get("type"), Cursor: q.Get("cursor")}
	fields := map[string]string{}
	if f.State != "" && !domain.ValidPrivacyState(domain.PrivacyState(f.State)) {
		fields["state"] = "is not a subject request state"
	}
	if f.Type != "" && !domain.ValidPrivacyType(domain.PrivacyRequestType(f.Type)) {
		fields["type"] = "must be ACCESS, PORTABILITY, ERASURE, RECTIFICATION or RESTRICTION"
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
	page, err := a.store.ListRequests(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) getRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	out, err := a.store.GetRequest(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

// actOnRequest moves a stored request one step (verify, discover, release hold,
// approve, deliver, close, reject) under If-Match.
func (a *API) actOnRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var in store.RequestActionInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !privacyActions[in.Action] {
		fields["action"] = "must be VERIFY, DISCOVER, RELEASE_HOLD, APPROVE, DELIVER, CLOSE or REJECT"
	}
	if len(in.HoldIDs) > maxHolds || !allValidIDs(in.HoldIDs) {
		fields["holdIds"] = fmt.Sprintf("at most %d hold ids of 1..%d characters", maxHolds, maxIDLength)
	}
	if len(in.MatchedAttributes) > maxAttributes || !allValidIDs(in.MatchedAttributes) {
		fields["matchedAttributes"] = fmt.Sprintf("at most %d attribute names", maxAttributes)
	}
	if len(in.Justification) > maxText {
		fields["justification"] = fmt.Sprintf("at most %d characters", maxText)
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	out, err := a.store.ActOnRequest(r.Context(), id, version, in, a.now())
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	setETag(w, out.Version)
	httpx.JSON(w, http.StatusOK, out)
}

// previewErasure shows what an ERASURE does to one record of the subject
// (BR-48.5): the stored request must be past discovery and free of holds
// (AC-48.7). Nothing is written; the domains apply it in the fan-out.
func (a *API) previewErasure(w http.ResponseWriter, r *http.Request) {
	id, ok := conflictID(w, r)
	if !ok {
		return
	}
	var in ErasurePreviewRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if len(in.Record) == 0 || len(in.Record) > maxRecordFields {
		httpx.Fail(w, r, invalid(map[string]string{"record": fmt.Sprintf("give 1..%d fields", maxRecordFields)}))
		return
	}
	req, err := a.store.GetRequest(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Type != domain.RequestErasure {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodePrivacyTransition, "only an ERASURE request pseudonymizes records"))
		return
	}
	switch req.State {
	case domain.PrivacyDiscovered, domain.PrivacyApproved, domain.PrivacyHeld:
	default:
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, domain.CodePrivacyTransition, fmt.Sprintf("an erasure can be previewed once scope is discovered, not in %s", req.State)))
		return
	}
	out, err := domain.PseudonymizeSubject(domain.PseudonymizeInput{SubjectRef: req.SubjectRef, HoldIDs: req.HoldIDs, Record: in.Record})
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// purgeCheck applies BR-48.6 to one record the monthly retention purge considers.
func (a *API) purgeCheck(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var in domain.PurgeCandidate
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !validID(in.RecordClass) {
		fields["recordClass"] = "is required"
	}
	if len(in.HoldIDs) > maxHolds || !allValidIDs(in.HoldIDs) {
		fields["holdIds"] = fmt.Sprintf("at most %d hold ids", maxHolds)
	}
	if len(in.OpenDocumentRefs) > maxAuditListItems || !allValidIDs(in.OpenDocumentRefs) {
		fields["openDocumentRefs"] = fmt.Sprintf("at most %d document references", maxAuditListItems)
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	httpx.JSON(w, http.StatusOK, domain.PurgeCheck(in))
}

func (a *API) planCampaign(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req CampaignPlanRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	closes, err := domain.PlanCampaign(req.StartsAt, req.WindowDays)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, CampaignPlanResponse{StartsAt: req.StartsAt.UTC(), ClosesAt: closes})
}

// closeCampaign applies BR-48.9 at the server time: before closesAt it reports
// progress only.
func (a *API) closeCampaign(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req CampaignCloseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	switch {
	case len(req.Entries) == 0:
		fields["entries"] = "list at least one manager, user and role"
	case len(req.Entries) > maxCampaignLines:
		fields["entries"] = fmt.Sprintf("at most %d lines", maxCampaignLines)
	}
	for _, e := range req.Entries {
		if len(e.ManagerID) > maxIDLength || len(e.UserID) > maxIDLength || len(e.RoleName) > maxIDLength {
			fields["entries"] = fmt.Sprintf("ids and role names are at most %d characters", maxIDLength)
			break
		}
	}
	if req.ClosesAt.IsZero() {
		fields["closesAt"] = "is required (RFC 3339)"
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	at := a.now()
	res, err := domain.CloseCampaign(domain.CampaignCloseInput{Entries: req.Entries, AutoRevoke: req.AutoRevoke, ClosesAt: req.ClosesAt, Now: at})
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, CampaignCloseResponse{CampaignCloseResult: res, AsOf: at})
}

// transitionIncident checks one incident lifecycle step at the server time
// (FR-48.29, BR-48.18). The caller supplies the incident's current status;
// nothing is stored yet.
func (a *API) transitionIncident(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var in domain.IncidentTransitionInput
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	at := a.now()
	fields := map[string]string{}
	if in.PersonalDataBreach {
		switch {
		case in.AwareAt.IsZero():
			fields["awareAt"] = "is required for a personal-data breach (RFC 3339)"
		case in.AwareAt.After(at):
			fields["awareAt"] = "must not be in the future"
		}
	}
	if len(in.Regimes) > maxRegimes {
		fields["regimes"] = fmt.Sprintf("at most %d regimes", maxRegimes)
	}
	if len(in.NotifiedAt) > maxNotifiedRegime {
		fields["notifiedAt"] = fmt.Sprintf("at most %d regimes", maxNotifiedRegime)
	}
	for _, sent := range in.NotifiedAt {
		if sent.After(at) {
			fields["notifiedAt"] = "a notification cannot be sent in the future"
		}
	}
	if len(in.Justification) > maxText {
		fields["justification"] = fmt.Sprintf("at most %d characters", maxText)
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	in.Now = at
	out, err := domain.ApplyIncidentTransition(in)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, IncidentTransitionResponse{IncidentTransitionOutcome: out, AsOf: at})
}

func (a *API) evaluateExportGovernance(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var in domain.ExportRequest
	if err := httpx.Decode(r, &in); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if len(in.Reason) > maxText {
		fields["reason"] = fmt.Sprintf("at most %d characters", maxText)
	}
	if len(in.Policy) > maxPolicyClasses {
		fields["policy"] = fmt.Sprintf("at most %d classifications", maxPolicyClasses)
	}
	for _, actions := range in.Policy {
		if len(actions) > maxPolicyActions {
			fields["policy"] = fmt.Sprintf("at most %d actions per classification", maxPolicyActions)
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	out, err := domain.DecideExport(in)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) classifyReadLog(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req ReadClassifyRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if len(req.Fields) == 0 || len(req.Fields) > maxReadFields || !allValidIDs(req.Fields) {
		fields["fields"] = fmt.Sprintf("give 1..%d field names", maxReadFields)
	}
	if len(req.Rules) > maxReadRules {
		fields["rules"] = fmt.Sprintf("at most %d rules", maxReadRules)
	}
	for _, rule := range req.Rules {
		if !validID(rule.FieldFQN) {
			fields["rules"] = "every rule needs a fieldFqn"
		}
	}
	if len(fields) > 0 {
		httpx.Fail(w, r, invalid(fields))
		return
	}
	out, err := domain.ClassifyRead(req.Fields, req.Rules, req.RetentionDays)
	if err != nil {
		httpx.Fail(w, r, domainProblem(err))
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func allValidIDs(xs []string) bool {
	for _, x := range xs {
		if !validID(x) {
			return false
		}
	}
	return true
}
