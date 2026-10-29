package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

// Duties of generic case management (PRD 8.45.5 and 8.45.6).
const (
	dutyCaseSetup    = "CaseSetupAdmin"
	dutyGenCreate    = "CaseCreate"
	dutyGenMaintain  = "CaseMaintain"
	defaultReopenDay = 14
	defaultPriority  = 3
	maxCaseReason    = 500
	maxCaseCodes     = 50
	codeFormat       = "must be 2 to 40 characters of A-Z, 0-9, _ or -"
)

// anyDuty allows a principal holding at least one of the duties: the setup
// lists serve both the case workers and the setup administrators.
type anyDuty []string

func (d anyDuty) Allow(_ context.Context, p tenant.Principal, _, _ string) (bool, error) {
	return slices.ContainsFunc(d, p.HasDuty), nil
}

type caseProcessRequest struct {
	Code   string         `json:"code"`
	Name   string         `json:"name"`
	Stages []domain.Stage `json:"stages"`
}

type caseCategoryRequest struct {
	Code             string   `json:"code"`
	Name             string   `json:"name"`
	CategoryType     string   `json:"categoryType"`
	ParentID         *string  `json:"parentId"`
	ProcessID        *string  `json:"processId"`
	SLAProfileID     *string  `json:"slaProfileId"`
	DefaultPriority  *int     `json:"defaultPriority"`
	ReopenWindowDays *int     `json:"reopenWindowDays"`
	AccessDuty       string   `json:"accessDuty"`
	ResolutionCodes  []string `json:"resolutionCodes"`
}

type genericCaseRequest struct {
	CategoryID      string                   `json:"categoryId"`
	LegalEntityID   string                   `json:"legalEntityId"`
	CustomerPartyID *string                  `json:"customerPartyId"`
	Subject         string                   `json:"subject"`
	Description     string                   `json:"description"`
	Priority        int                      `json:"priority"`
	Associations    []caseAssociationRequest `json:"associations"`
}

type caseStageRequest struct {
	ToStage string `json:"toStage"`
	Reason  string `json:"reason"`
}

type caseStatusRequest struct {
	To             string `json:"to"`
	ResolutionCode string `json:"resolutionCode"`
	ResolutionText string `json:"resolutionText"`
	Reason         string `json:"reason"`
}

type caseReopenRequest struct {
	Reason string `json:"reason"`
}

type caseAssociationRequest struct {
	EntityType string `json:"entityType"`
	EntityID   string `json:"entityId"`
	Role       string `json:"role"`
}

func (a *API) registerCases(r chi.Router) {
	setupRead := authz.Require(anyDuty{dutyGenMaintain, dutyGenCreate, dutyCaseSetup}, "CaseMaintain, CaseCreate or CaseSetupAdmin", "")
	r.With(authz.RequireDuty(dutyCaseSetup)).Post("/v1/case-processes", a.createCaseProcess)
	r.With(setupRead).Get("/v1/case-processes", a.listCaseProcesses)
	r.With(authz.RequireDuty(dutyCaseSetup)).Post("/v1/case-categories", a.createCaseCategory)
	r.With(setupRead).Get("/v1/case-categories", a.listCaseCategories)
	r.With(authz.RequireDuty(dutyGenCreate)).Post("/v1/cases", a.openGenericCase)
	r.With(authz.RequireDuty(dutyGenMaintain)).Get("/v1/cases", a.listGenericCases)
	r.With(authz.RequireDuty(dutyGenMaintain)).Get("/v1/cases/{id}", a.getGenericCase)
	r.With(authz.RequireDuty(dutyGenMaintain)).Post("/v1/cases/{id}/stage", a.advanceCaseStage)
	r.With(authz.RequireDuty(dutyGenMaintain)).Post("/v1/cases/{id}:transition", a.transitionGenericCase)
	r.With(authz.RequireDuty(dutyGenMaintain)).Post("/v1/cases/{id}:reopen", a.reopenGenericCase)
	r.With(authz.RequireDuty(dutyGenMaintain)).Post("/v1/cases/{id}/associations", a.associateCase)
}

// createCaseProcess defines the ordered stages of a process (BR-45.9).
func (a *API) createCaseProcess(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req caseProcessRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	for i := range req.Stages {
		for j, s := range req.Stages[i].RequiredActivities {
			req.Stages[i].RequiredActivities[j] = strings.TrimSpace(s)
		}
		if req.Stages[i].RequiredActivities == nil {
			req.Stages[i].RequiredActivities = []string{}
		}
	}
	fields := map[string]string{}
	if !domain.ValidCode(req.Code) {
		fields["code"] = codeFormat
	}
	nameField(fields, "name", req.Name)
	if problems := domain.ProcessProblems(req.Stages); len(problems) > 0 {
		fields["stages"] = strings.Join(problems, "; ")
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	p, replayed, err := a.store.CreateProcess(r.Context(), store.Process{Code: req.Code, Name: req.Name, Stages: req.Stages},
		key, hashOf(req))
	if err != nil {
		a.fail(w, r, "create case process", err)
		return
	}
	a.respond(w, replayed, http.StatusCreated, p)
}

// createCaseCategory defines a node of the case category tree (FR-45.12). SERVICE
// and CUSTOMER_SERVICE categories are accepted, but their cases open through the
// service case endpoints.
func (a *API) createCaseCategory(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req caseCategoryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	fields := map[string]string{}
	if !domain.ValidCode(req.Code) {
		fields["code"] = codeFormat
	}
	nameField(fields, "name", req.Name)
	if !domain.ValidCategoryType(req.CategoryType) {
		fields["categoryType"] = "must be one of " + strings.Join(domain.CategoryTypes, ", ")
	}
	optionalUUID(fields, "parentId", req.ParentID)
	optionalUUID(fields, "processId", req.ProcessID)
	optionalUUID(fields, "slaProfileId", req.SLAProfileID)
	days := defaultReopenDay
	if req.ReopenWindowDays != nil {
		if days = *req.ReopenWindowDays; days < 0 || days > 365 {
			fields["reopenWindowDays"] = "must be 0 to 365"
		}
	}
	priority := defaultPriority
	if req.DefaultPriority != nil {
		if priority = *req.DefaultPriority; priority < 1 || priority > 4 {
			fields["defaultPriority"] = "must be 1 to 4"
		}
	}
	if len(req.AccessDuty) > 100 || strings.TrimSpace(req.AccessDuty) != req.AccessDuty {
		fields["accessDuty"] = "must be a duty name of at most 100 characters"
	}
	if len(req.ResolutionCodes) > maxCaseCodes {
		fields["resolutionCodes"] = "at most 50 resolution codes"
	}
	seen := map[string]bool{}
	for _, rc := range req.ResolutionCodes {
		if !domain.ValidCode(rc) || seen[rc] {
			fields["resolutionCodes"] = "each resolution code must be unique and " + codeFormat
		}
		seen[rc] = true
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	codes := req.ResolutionCodes
	if codes == nil {
		codes = []string{}
	}
	c, replayed, err := a.store.CreateCategory(r.Context(), store.Category{
		Code: req.Code, Name: req.Name, CategoryType: req.CategoryType, ParentID: req.ParentID, ProcessID: req.ProcessID,
		SLAProfileID: req.SLAProfileID, DefaultPriority: priority, ReopenWindowDays: days, AccessDuty: req.AccessDuty,
		ResolutionCodes: codes,
	}, key, hashOf(req))
	if err != nil {
		a.fail(w, r, "create case category", err)
		return
	}
	a.respond(w, replayed, http.StatusCreated, c)
}

func (a *API) listCaseProcesses(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	ps, err := a.store.ListProcesses(r.Context())
	if err != nil {
		a.fail(w, r, "list case processes", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": ps})
}

func (a *API) listCaseCategories(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	cats, err := a.store.ListCategories(r.Context())
	if err != nil {
		a.fail(w, r, "list case categories", err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": cats})
}

// openGenericCase opens a case in a non-service category (FR-45.12); the caller is the owner.
func (a *API) openGenericCase(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req genericCaseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Subject = strings.TrimSpace(req.Subject)
	fields := map[string]string{}
	uuidField(fields, "categoryId", req.CategoryID)
	uuidField(fields, "legalEntityId", req.LegalEntityID)
	optionalUUID(fields, "customerPartyId", req.CustomerPartyID)
	if n := len([]rune(req.Subject)); n < 1 || n > 200 {
		fields["subject"] = "must be 1 to 200 characters"
	}
	if len([]rune(req.Description)) > maxDescription {
		fields["description"] = "must be at most 4000 characters"
	}
	if req.Priority != 0 && (req.Priority < 1 || req.Priority > 4) {
		fields["priority"] = "must be 1 to 4, or omitted for the category default"
	}
	if len(req.Associations) > maxAssociations {
		fields["associations"] = "at most 20 associations"
	}
	assoc := make([]store.CaseAssociation, 0, len(req.Associations))
	for i := range req.Associations {
		assoc = append(assoc, associationField(fields, "associations", &req.Associations[i], ""))
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	c, replayed, err := a.store.OpenGenericCase(r.Context(), store.OpenGenericInput{
		CategoryID: req.CategoryID, LegalEntityID: req.LegalEntityID, CustomerPartyID: req.CustomerPartyID, Subject: req.Subject,
		Description: req.Description, Priority: req.Priority, Associations: assoc, OpenedAt: a.now().UTC(),
	}, key, hashOf(req))
	if err != nil {
		a.fail(w, r, "open case", err)
		return
	}
	setETag(w, c.Version)
	a.respond(w, replayed, http.StatusCreated, c)
}

// listGenericCases lists the cases the caller may read (BR-45.10), newest first.
func (a *API) listGenericCases(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.GenericFilter{CategoryID: q.Get("categoryId"), StageCode: q.Get("stage"), Status: q.Get("status"),
		OwnerID: q.Get("ownerId"), EntityType: q.Get("entityType"), EntityID: q.Get("entityId")}
	fields := map[string]string{}
	if f.CategoryID != "" {
		uuidField(fields, "categoryId", f.CategoryID)
	}
	if f.Status != "" && !oneOf(f.Status, domain.GenericOpen, domain.GenericResolved, domain.GenericClosed, domain.GenericCanceled) {
		fields["status"] = "must be OPEN, RESOLVED, CLOSED or CANCELED"
	}
	if len(f.StageCode) > 40 {
		fields["stage"] = "must be a stage code"
	}
	if len(f.OwnerID) > 100 {
		fields["ownerId"] = "must be a user id"
	}
	if (f.EntityType == "") != (f.EntityID == "") {
		fields["entityType"] = "entityType and entityId go together"
	} else if f.EntityType != "" {
		if !domain.ValidAssociation(f.EntityType) {
			fields["entityType"] = "must be a registry record type"
		}
		uuidField(fields, "entityId", f.EntityID)
	}
	f.Cursor, f.Limit = pageParams(fields, q.Get("cursor"), q.Get("limit"))
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	page, err := a.store.ListGenericCases(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list cases", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

// getGenericCase returns one case with its associations and log; ETag carries the version.
func (a *API) getGenericCase(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeGenericUnknown, "case not found")
	if !ok {
		return
	}
	c, err := a.store.GetGenericCase(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get case", err)
		return
	}
	setETag(w, c.Version)
	httpx.JSON(w, http.StatusOK, c)
}

// caseMoveHeaders reads the path id, If-Match and Idempotency-Key of a case command.
func caseMoveHeaders(w http.ResponseWriter, r *http.Request, needVersion bool) (id string, version int, key string, ok bool) {
	if !validTenant(w, r) {
		return "", 0, "", false
	}
	if id, ok = pathID(w, r, "id", store.CodeGenericUnknown, "case not found"); !ok {
		return "", 0, "", false
	}
	if needVersion {
		if version, ok = ifMatch(w, r); !ok {
			return "", 0, "", false
		}
	}
	key, ok = idempotencyKeyOf(w, r)
	return id, version, key, ok
}

// requestHash fingerprints a case command with its operation and target, so a
// key reused on another case or command is refused.
func requestHash(op, id string, body any) string {
	return hashOf(struct {
		Op   string `json:"op"`
		ID   string `json:"id"`
		Body any    `json:"body"`
	}{op, id, body})
}

func reasonField(fields map[string]string, reason string, required bool) {
	n := len([]rune(strings.TrimSpace(reason)))
	if n > maxCaseReason || (required && n == 0) {
		fields["reason"] = "must be 1 to 500 characters"
		if !required {
			fields["reason"] = "must be at most 500 characters"
		}
	}
}

// advanceCaseStage moves a case to the next stage or back one (BR-45.9, AC-45.10).
func (a *API) advanceCaseStage(w http.ResponseWriter, r *http.Request) {
	id, version, key, ok := caseMoveHeaders(w, r, true)
	if !ok {
		return
	}
	var req caseStageRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	fields := map[string]string{}
	if !domain.ValidCode(req.ToStage) {
		fields["toStage"] = "must be a stage code: " + codeFormat
	}
	reasonField(fields, req.Reason, false)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	c, replayed, err := a.store.AdvanceStage(r.Context(), store.CaseMove{ID: id, Version: version, ToStage: req.ToStage,
		Reason: req.Reason, At: a.now().UTC()}, key, requestHash("stage", id, req))
	if err != nil {
		a.fail(w, r, "advance case stage", err)
		return
	}
	setETag(w, c.Version)
	a.respond(w, replayed, http.StatusOK, c)
}

// transitionGenericCase resolves, closes or cancels a case (BR-45.9 resolution rules).
func (a *API) transitionGenericCase(w http.ResponseWriter, r *http.Request) {
	id, version, key, ok := caseMoveHeaders(w, r, true)
	if !ok {
		return
	}
	var req caseStatusRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	fields := map[string]string{}
	if !oneOf(req.To, domain.GenericResolved, domain.GenericClosed, domain.GenericCanceled) {
		fields["to"] = "must be RESOLVED, CLOSED or CANCELED"
	}
	if req.ResolutionCode != "" && !domain.ValidCode(req.ResolutionCode) {
		fields["resolutionCode"] = codeFormat
	}
	if req.To != domain.GenericResolved && (req.ResolutionCode != "" || req.ResolutionText != "") {
		fields["resolutionCode"] = "only a move to RESOLVED takes a resolution"
	}
	if len([]rune(req.ResolutionText)) > maxDescription {
		fields["resolutionText"] = "must be at most 4000 characters"
	}
	reasonField(fields, req.Reason, false)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	c, replayed, err := a.store.TransitionGenericCase(r.Context(), store.CaseMove{ID: id, Version: version, ToStatus: req.To,
		ResolutionCode: req.ResolutionCode, ResolutionText: req.ResolutionText, Reason: req.Reason, At: a.now().UTC()},
		key, requestHash("transition", id, req))
	if err != nil {
		a.fail(w, r, "transition case", err)
		return
	}
	setETag(w, c.Version)
	a.respond(w, replayed, http.StatusOK, c)
}

// reopenGenericCase reopens a RESOLVED or CLOSED case (BR-45.9): inside the
// category's reopen window the same case, after it a new case linked by
// reopenedFrom (201 with created true).
func (a *API) reopenGenericCase(w http.ResponseWriter, r *http.Request) {
	id, version, key, ok := caseMoveHeaders(w, r, true)
	if !ok {
		return
	}
	var req caseReopenRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req.Reason = strings.TrimSpace(req.Reason)
	fields := map[string]string{}
	reasonField(fields, req.Reason, true)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	res, replayed, err := a.store.ReopenGenericCase(r.Context(), store.CaseMove{ID: id, Version: version, Reason: req.Reason,
		At: a.now().UTC()}, key, requestHash("reopen", id, req))
	if err != nil {
		a.fail(w, r, "reopen case", err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	setETag(w, res.Case.Version)
	a.respond(w, replayed, status, res)
}

// associateCase links any registry record to a case (FR-45.12).
func (a *API) associateCase(w http.ResponseWriter, r *http.Request) {
	id, _, key, ok := caseMoveHeaders(w, r, false)
	if !ok {
		return
	}
	var req caseAssociationRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	assoc := associationField(fields, "", &req, id)
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	c, replayed, err := a.store.AddAssociation(r.Context(), id, assoc, a.now().UTC(), key, requestHash("associate", id, req))
	if err != nil {
		a.fail(w, r, "associate case", err)
		return
	}
	setETag(w, c.Version)
	a.respond(w, replayed, http.StatusOK, c)
}

// associationField validates one association (role defaults to RELATED) under
// prefix; self is the case's own id, which a case cannot be associated with.
func associationField(fields map[string]string, prefix string, req *caseAssociationRequest, self string) store.CaseAssociation {
	name := func(f string) string {
		if prefix == "" {
			return f
		}
		return prefix
	}
	if req.Role == "" {
		req.Role = "RELATED"
	}
	if !domain.ValidAssociation(req.EntityType) {
		fields[name("entityType")] = "entityType must be a registry record type (" + strings.Join(domain.AssociableTypes, ", ") + ")"
	}
	uuidField(fields, name("entityId"), req.EntityID)
	if !oneOf(req.Role, "SUBJECT", "RELATED") {
		fields[name("role")] = "role must be SUBJECT or RELATED"
	}
	if req.EntityType == "case" && req.EntityID == self && self != "" {
		fields[name("entityId")] = "a case cannot be associated with itself"
	}
	return store.CaseAssociation{EntityType: req.EntityType, EntityID: req.EntityID, Role: req.Role}
}

// nameField checks a display name of 1 to 120 characters.
func nameField(fields map[string]string, name, v string) {
	if n := len([]rune(v)); n < 1 || n > 120 {
		fields[name] = "must be 1 to 120 characters"
	}
}
