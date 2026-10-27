// Package api exposes the field-service HTTP routes under /v1/service.
package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/domain"
	"github.com/BenjaminSRussell/passage/services/fieldservice/internal/store"
)

const (
	idempotencyKey   = "Idempotency-Key"
	validationDetail = "request failed validation"
	maxDescription   = 4000
	maxLines         = 200
	dateLayout       = "2006-01-02"
	replayedHeader   = "Idempotent-Replayed"
	problemCodeValid = "COM-0001"
	maxHolidays      = 400
	defaultPageSize  = 50
	maxPageSize      = 200

	// Duties follow the PRD names (8.45.17); OPA maps them to privileges.
	dutyAgreement  = "SvcAgreementMaintain"
	dutyCaseCreate = "SvcCaseCreate"
	dutyWorkOrder  = "SvcWoMaintain"
	dutyBilling    = "SvcBillingPost"
	dutyInquire    = "SvcInquire"
)

var (
	profileCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,19}$`)
	currency    = regexp.MustCompile(`^[A-Z]{3}$`)
)

// API holds the store and the counters exported on /metrics.
type API struct {
	store    *store.Postgres
	now      func() time.Time
	opened   atomic.Int64
	posted   atomic.Int64
	replayed atomic.Int64
}

// New returns the API over s.
func New(s *store.Postgres) *API {
	return &API{store: s, now: func() time.Time { return time.Now().UTC() }}
}

// Register mounts the routes on r.
func (a *API) Register(r chi.Router) {
	r.With(authz.RequireDuty(dutyAgreement)).Post("/v1/service/sla-profiles", a.createSLAProfile)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/sla-profiles", a.listSLAProfiles)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/sla-profiles/{id}", a.getSLAProfile)
	r.With(authz.RequireDuty(dutyAgreement)).Post("/v1/service/agreements", a.createAgreement)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/agreements", a.listAgreements)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/agreements/{id}", a.getAgreement)
	r.With(authz.RequireDuty(dutyCaseCreate)).Post("/v1/service/cases", a.openCase)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/cases", a.listCases)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/cases/{id}", a.getCase)
	r.With(authz.RequireDuty(dutyWorkOrder)).Post("/v1/service/work-orders", a.createWorkOrder)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/work-orders", a.listWorkOrders)
	r.With(authz.RequireDuty(dutyInquire)).Get("/v1/service/work-orders/{id}", a.getWorkOrder)
	r.With(authz.RequireDuty(dutyWorkOrder)).Post("/v1/service/work-orders/{id}:transition", a.transitionWorkOrder)
	r.With(authz.RequireDuty(dutyBilling)).Post("/v1/service/work-orders/{id}:post", a.postWorkOrder)
}

// Metrics serves the service counters in Prometheus text format.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "# HELP service_cases_opened_total Cases opened; idempotent replays are not counted.\n# TYPE service_cases_opened_total counter\nservice_cases_opened_total %d\n", a.opened.Load())
		fmt.Fprintf(w, "# HELP service_workorders_posted_total Work orders posted; idempotent replays are not counted.\n# TYPE service_workorders_posted_total counter\nservice_workorders_posted_total %d\n", a.posted.Load())
		fmt.Fprintf(w, "# HELP service_idempotent_replays_total Requests answered from a stored Idempotency-Key.\n# TYPE service_idempotent_replays_total counter\nservice_idempotent_replays_total %d\n", a.replayed.Load())
	})
}

type profileRequest struct {
	Code         string          `json:"code"`
	Name         string          `json:"name"`
	TimeZone     string          `json:"timeZone"`
	AllDay       bool            `json:"allDay"`
	OpenMinute   int             `json:"openMinute"`
	CloseMinute  int             `json:"closeMinute"`
	PauseOnParts bool            `json:"pauseOnParts"`
	Holidays     []string        `json:"holidays"`
	Targets      []domain.Target `json:"targets"`
}

func (a *API) createSLAProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req profileRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	if !profileCode.MatchString(req.Code) {
		fields["code"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -"
	}
	if n := len([]rune(req.Name)); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if _, err := time.LoadLocation(req.TimeZone); err != nil || req.TimeZone == "" || req.TimeZone == "Local" {
		fields["timeZone"] = "must be an IANA time zone name"
	}
	if !req.AllDay && !(req.OpenMinute >= 0 && req.OpenMinute < req.CloseMinute && req.CloseMinute <= 24*60) {
		fields["openMinute"] = "business hours must satisfy 0 <= openMinute < closeMinute <= 1440"
	}
	if len(req.Holidays) > maxHolidays {
		fields["holidays"] = fmt.Sprintf("must hold at most %d dates", maxHolidays)
	}
	for _, h := range req.Holidays {
		if _, ok := parseDate(h); !ok {
			fields["holidays"] = "must be dates as YYYY-MM-DD"
		}
	}
	if len(req.Targets) < 1 || len(req.Targets) > 4 {
		fields["targets"] = "must hold one to four priority targets"
	}
	seen := map[int]bool{}
	for _, t := range req.Targets {
		switch {
		case t.Priority < 1 || t.Priority > 4 || seen[t.Priority]:
			fields["targets"] = "priorities must be distinct values 1 to 4"
		case t.ResponseMinutes <= 0 || t.ResolutionMinutes < t.ResponseMinutes:
			fields["targets"] = "resolution target must be at least the response target, both positive"
		}
		seen[t.Priority] = true
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	in := store.SLAProfile{Code: req.Code, Name: strings.TrimSpace(req.Name), TimeZone: req.TimeZone, AllDay: req.AllDay,
		OpenMinute: req.OpenMinute, CloseMinute: req.CloseMinute, PauseOnParts: req.PauseOnParts, Holidays: req.Holidays, Targets: req.Targets}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	created, replayed, err := a.store.CreateSLAProfile(r.Context(), in, key, hashOf(req))
	if err != nil {
		a.fail(w, r, "create SLA profile", err)
		return
	}
	a.respond(w, replayed, http.StatusCreated, created)
}

type agreementRequest struct {
	CustomerPartyID string                 `json:"customerPartyId"`
	LegalEntityID   string                 `json:"legalEntityId"`
	ValidFrom       string                 `json:"validFrom"`
	ValidTo         *string                `json:"validTo"`
	SLAProfileID    string                 `json:"slaProfileId"`
	BillingModel    string                 `json:"billingModel"`
	PriorityRank    int                    `json:"priorityRank"`
	Status          string                 `json:"status"`
	Lines           []domain.AgreementLine `json:"lines"`
}

func (a *API) createAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req agreementRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.Status == "" {
		req.Status = "DRAFT"
	}
	fields := map[string]string{}
	uuidField(fields, "customerPartyId", req.CustomerPartyID)
	uuidField(fields, "legalEntityId", req.LegalEntityID)
	uuidField(fields, "slaProfileId", req.SLAProfileID)
	from, fromOK := parseDate(req.ValidFrom)
	if !fromOK {
		fields["validFrom"] = "must be a date as YYYY-MM-DD"
	}
	if req.ValidTo != nil {
		if to, ok := parseDate(*req.ValidTo); !ok {
			fields["validTo"] = "must be a date as YYYY-MM-DD"
		} else if fromOK && to.Before(from) {
			fields["validTo"] = "must not be before validFrom"
		}
	}
	if !oneOf(req.BillingModel, "PREPAID", "PER_VISIT", "SUBSCRIPTION") {
		fields["billingModel"] = "must be PREPAID, PER_VISIT or SUBSCRIPTION"
	}
	if req.PriorityRank < 1 || req.PriorityRank > 100 {
		fields["priorityRank"] = "must be 1 to 100"
	}
	if !oneOf(req.Status, "DRAFT", "ACTIVE") {
		fields["status"] = "must be DRAFT or ACTIVE on create"
	}
	if len(req.Lines) < 1 || len(req.Lines) > maxLines {
		fields["lines"] = fmt.Sprintf("must hold 1 to %d lines", maxLines)
	}
	for i, l := range req.Lines {
		key := fmt.Sprintf("lines[%d]", i)
		if l.ServiceObjectID != "" && l.CategoryID != "" {
			fields[key] = "names either a service object or a category, not both"
		}
		if l.ServiceObjectID != "" && !ids.Valid(l.ServiceObjectID) || l.CategoryID != "" && !ids.Valid(l.CategoryID) {
			fields[key] = "service object and category must be UUIDs"
		}
		if l.IncludedHours.IsNegative() || l.IncludedVisits < 0 {
			fields[key] = "included hours and visits must not be negative"
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	in := store.Agreement{CustomerPartyID: req.CustomerPartyID, LegalEntityID: req.LegalEntityID, ValidFrom: req.ValidFrom,
		ValidTo: req.ValidTo, SLAProfileID: req.SLAProfileID, BillingModel: req.BillingModel, PriorityRank: req.PriorityRank,
		Status: req.Status, Lines: req.Lines}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	created, replayed, err := a.store.CreateAgreement(r.Context(), in, key, hashOf(req))
	if err != nil {
		a.fail(w, r, "create agreement", err)
		return
	}
	a.respond(w, replayed, http.StatusCreated, created)
}

func (a *API) getAgreement(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeAgreementNotFound, "service agreement not found")
	if !ok {
		return
	}
	agr, err := a.store.GetAgreement(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get agreement", err)
		return
	}
	httpx.JSON(w, http.StatusOK, agr)
}

type openCaseRequest struct {
	CustomerPartyID      string     `json:"customerPartyId"`
	LegalEntityID        string     `json:"legalEntityId"`
	ServiceObjectID      *string    `json:"serviceObjectId"`
	Channel              string     `json:"channel"`
	Priority             int        `json:"priority"`
	Description          string     `json:"description"`
	OpenedAt             *time.Time `json:"openedAt"`
	FallbackSLAProfileID *string    `json:"fallbackSlaProfileId"`
}

func (a *API) openCase(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req openCaseRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	uuidField(fields, "customerPartyId", req.CustomerPartyID)
	uuidField(fields, "legalEntityId", req.LegalEntityID)
	optionalUUID(fields, "serviceObjectId", req.ServiceObjectID)
	optionalUUID(fields, "fallbackSlaProfileId", req.FallbackSLAProfileID)
	if !oneOf(req.Channel, "PHONE", "EMAIL", "PORTAL", "IOT", "INTERNAL") {
		fields["channel"] = "must be PHONE, EMAIL, PORTAL, IOT or INTERNAL"
	}
	if req.Priority < 1 || req.Priority > 4 {
		fields["priority"] = "must be 1 to 4"
	}
	if len([]rune(req.Description)) > maxDescription {
		fields["description"] = fmt.Sprintf("must be at most %d characters", maxDescription)
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	opened := a.now()
	if req.OpenedAt != nil {
		opened = req.OpenedAt.UTC()
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	in := store.OpenCaseInput{CustomerPartyID: req.CustomerPartyID, LegalEntityID: req.LegalEntityID, ServiceObjectID: req.ServiceObjectID,
		Channel: req.Channel, Priority: req.Priority, Description: req.Description, OpenedAt: opened, FallbackSLAProfileID: req.FallbackSLAProfileID}
	created, replayed, err := a.store.OpenCase(r.Context(), in, key, hashOf(req))
	if err != nil {
		a.fail(w, r, "open case", err)
		return
	}
	if !replayed {
		a.opened.Add(1)
	}
	a.respond(w, replayed, http.StatusCreated, created)
}

func (a *API) getCase(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeCaseUnknown, "service case not found")
	if !ok {
		return
	}
	c, err := a.store.GetCase(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get case", err)
		return
	}
	httpx.JSON(w, http.StatusOK, c)
}

type workOrderRequest struct {
	CustomerPartyID    string            `json:"customerPartyId"`
	LegalEntityID      string            `json:"legalEntityId"`
	TerritoryID        string            `json:"territoryId"`
	CaseID             *string           `json:"caseId"`
	AgreementID        *string           `json:"agreementId"`
	IncidentTypeID     *string           `json:"incidentTypeId"`
	EstDurationMinutes int               `json:"estDurationMinutes"`
	Currency           string            `json:"currency"`
	Lines              []domain.WorkLine `json:"lines"`
}

func (a *API) createWorkOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req workOrderRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	fields := map[string]string{}
	uuidField(fields, "customerPartyId", req.CustomerPartyID)
	uuidField(fields, "legalEntityId", req.LegalEntityID)
	uuidField(fields, "territoryId", req.TerritoryID)
	optionalUUID(fields, "caseId", req.CaseID)
	optionalUUID(fields, "agreementId", req.AgreementID)
	optionalUUID(fields, "incidentTypeId", req.IncidentTypeID)
	if req.EstDurationMinutes < 1 || req.EstDurationMinutes > 1440 {
		fields["estDurationMinutes"] = "must be 1 to 1440"
	}
	if !currency.MatchString(req.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code"
	}
	if len(req.Lines) > maxLines {
		fields["lines"] = fmt.Sprintf("must hold at most %d lines", maxLines)
	}
	// Fingerprint the body as sent, before server-generated line ids are filled
	// in, so a retry of the same request replays instead of failing as a reuse.
	requestHash := hashOf(req)
	for i := range req.Lines {
		fields = validateLine(fields, i, &req.Lines[i])
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	in := store.WorkOrder{CustomerPartyID: req.CustomerPartyID, LegalEntityID: req.LegalEntityID, TerritoryID: req.TerritoryID,
		CaseID: req.CaseID, AgreementID: req.AgreementID, IncidentTypeID: req.IncidentTypeID,
		EstDurationMinutes: req.EstDurationMinutes, Currency: req.Currency, Lines: req.Lines}
	created, replayed, err := a.store.CreateWorkOrder(r.Context(), in, key, requestHash)
	if err != nil {
		a.fail(w, r, "create work order", err)
		return
	}
	setETag(w, created.Version)
	a.respond(w, replayed, http.StatusCreated, created)
}

func validateLine(fields map[string]string, i int, l *domain.WorkLine) map[string]string {
	key := fmt.Sprintf("lines[%d]", i)
	if l.ID == "" {
		l.ID = ids.New()
	}
	if !ids.Valid(l.ID) {
		fields[key] = "id must be a UUID"
	}
	if !oneOf(string(l.Kind), "LABOR", "PART", "TRAVEL", "FEE") {
		fields[key] = "kind must be LABOR, PART, TRAVEL or FEE"
	}
	if !l.Qty.IsPositive() {
		fields[key] = "qty must be positive"
	}
	if l.UnitPrice.IsNegative() || l.UnitCost.IsNegative() {
		fields[key] = "unit price and unit cost must not be negative"
	}
	return fields
}

func (a *API) getWorkOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeWOUnknown, "service work order not found")
	if !ok {
		return
	}
	wo, err := a.store.GetWorkOrder(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get work order", err)
		return
	}
	setETag(w, wo.Version)
	httpx.JSON(w, http.StatusOK, wo)
}

type transitionRequest struct {
	To string `json:"to"`
}

func (a *API) transitionWorkOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeWOUnknown, "service work order not found")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var req transitionRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if !oneOf(req.To, "SCHEDULED", "DISPATCHED", "TRAVELING", "IN_PROGRESS", "ON_HOLD", "COMPLETED", "CANCELED") {
		validationFailed(w, r, map[string]string{"to": "must be a status that can be requested by hand"})
		return
	}
	wo, err := a.store.TransitionWorkOrder(r.Context(), id, version, domain.WOStatus(req.To))
	if err != nil {
		a.fail(w, r, "transition work order", err)
		return
	}
	setETag(w, wo.Version)
	httpx.JSON(w, http.StatusOK, wo)
}

func (a *API) postWorkOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeWOUnknown, "service work order not found")
	if !ok {
		return
	}
	version, ok := ifMatch(w, r)
	if !ok {
		return
	}
	key, ok := idempotencyKeyOf(w, r)
	if !ok {
		return
	}
	wo, replayed, err := a.store.PostWorkOrder(r.Context(), id, version, key)
	if err != nil {
		a.fail(w, r, "post work order", err)
		return
	}
	if !replayed {
		a.posted.Add(1)
	}
	setETag(w, wo.Version)
	a.respond(w, replayed, http.StatusOK, wo)
}

func (a *API) listSLAProfiles(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.store.ListSLAProfiles(r.Context())
	if err != nil {
		a.fail(w, r, "list SLA profiles", err)
		return
	}
	httpx.JSON(w, http.StatusOK, store.Page[store.SLAProfile]{Items: items})
}

func (a *API) getSLAProfile(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, "id", store.CodeProfileNotFound, "SLA profile not found")
	if !ok {
		return
	}
	p, err := a.store.GetSLAProfile(r.Context(), id)
	if err != nil {
		a.fail(w, r, "get SLA profile", err)
		return
	}
	httpx.JSON(w, http.StatusOK, p)
}

var (
	agreementStatuses = []string{"DRAFT", "ACTIVE", "SUSPENDED", "EXPIRED", "CANCELED"}
	caseStatuses      = []string{"NEW", "IN_PROGRESS", "WAITING_CUSTOMER", "WAITING_PARTS", "RESOLVED", "CLOSED", "CANCELED"}
	slaStates         = []string{domain.SLARunning, domain.SLAPaused, domain.SLAMet, domain.SLABreached}
	woStatuses        = []string{"UNSCHEDULED", "SCHEDULED", "DISPATCHED", "TRAVELING", "IN_PROGRESS", "ON_HOLD", "COMPLETED", "POSTED", "CANCELED"}
)

// listFilter reads the shared list query parameters. Each named enum
// parameter is checked against its allowed values.
func listFilter(w http.ResponseWriter, r *http.Request, enums map[string][]string, uuids ...string) (store.ListFilter, bool) {
	q := r.URL.Query()
	fields := map[string]string{}
	for name, allowed := range enums {
		if v := q.Get(name); v != "" && !oneOf(v, allowed...) {
			fields[name] = "must be one of " + strings.Join(allowed, ", ")
		}
	}
	for _, name := range append(uuids, "cursor") {
		if v := q.Get(name); v != "" && !ids.Valid(v) {
			fields[name] = "must be a UUID"
		}
	}
	limit := defaultPageSize
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPageSize {
			fields["limit"] = fmt.Sprintf("must be 1 to %d", maxPageSize)
		}
		limit = n
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return store.ListFilter{}, false
	}
	return store.ListFilter{Status: q.Get("status"), SLAState: q.Get("slaState"), Customer: q.Get("customerPartyId"),
		Territory: q.Get("territoryId"), CaseID: q.Get("caseId"), Before: q.Get("cursor"), Limit: limit}, true
}

func (a *API) listAgreements(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f, ok := listFilter(w, r, map[string][]string{"status": agreementStatuses}, "customerPartyId")
	if !ok {
		return
	}
	page, err := a.store.ListAgreements(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list agreements", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) listCases(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f, ok := listFilter(w, r, map[string][]string{"status": caseStatuses, "slaState": slaStates}, "customerPartyId")
	if !ok {
		return
	}
	page, err := a.store.ListCases(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list cases", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

func (a *API) listWorkOrders(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	f, ok := listFilter(w, r, map[string][]string{"status": woStatuses}, "customerPartyId", "territoryId", "caseId")
	if !ok {
		return
	}
	page, err := a.store.ListWorkOrders(r.Context(), f)
	if err != nil {
		a.fail(w, r, "list work orders", err)
		return
	}
	httpx.JSON(w, http.StatusOK, page)
}

// --- helpers ---

// setETag publishes the aggregate version that If-Match must echo back.
func setETag(w http.ResponseWriter, version int) {
	w.Header().Set("ETag", fmt.Sprintf("%q", strconv.Itoa(version)))
}

func (a *API) fail(w http.ResponseWriter, r *http.Request, op string, err error) {
	slog.Error(op+" failed", "err", err, "correlationId", r.Header.Get("X-Request-Id"))
	httpx.Fail(w, r, err)
}

func (a *API) respond(w http.ResponseWriter, replayed bool, status int, body any) {
	if replayed {
		a.replayed.Add(1)
		w.Header().Set(replayedHeader, "true")
		status = http.StatusOK
	}
	httpx.JSON(w, status, body)
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

func idempotencyKeyOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get(idempotencyKey)
	if key == "" || len(key) > 255 {
		validationFailed(w, r, map[string]string{idempotencyKey: "is required and must be at most 255 characters"})
		return "", false
	}
	return key, true
}

// ifMatch reads the required If-Match version (conventions: 428 when absent).
func ifMatch(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.Trim(r.Header.Get("If-Match"), `"`)
	if raw == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusPreconditionRequired, "precondition_required", "If-Match with the current version is required"))
		return 0, false
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 {
		validationFailed(w, r, map[string]string{"If-Match": "must be the current version number"})
		return 0, false
	}
	return v, true
}

func pathID(w http.ResponseWriter, r *http.Request, param, code, detail string) (string, bool) {
	id := chi.URLParam(r, param)
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, code, detail))
		return "", false
	}
	return id, true
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, problemCodeValid, validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

func uuidField(fields map[string]string, name, value string) {
	if !ids.Valid(value) {
		fields[name] = "must be a UUID"
	}
}

func optionalUUID(fields map[string]string, name string, value *string) {
	if value != nil && !ids.Valid(*value) {
		fields[name] = "must be a UUID"
	}
}

func parseDate(value string) (time.Time, bool) {
	d, err := time.Parse(dateLayout, value)
	return d, err == nil
}

func oneOf(value string, allowed ...string) bool {
	for _, a := range allowed {
		if value == a {
			return true
		}
	}
	return false
}

// hashOf fingerprints a request body so a reused Idempotency-Key with a different body is refused.
func hashOf(v any) string {
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
