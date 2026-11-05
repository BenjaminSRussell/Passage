// Package api exposes the EAM HTTP API (contract: api/openapi.yaml).
package api

import (
	"context"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/ledgerclient"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/eam/internal/domain"
	"github.com/BenjaminSRussell/passage/services/eam/internal/store"
)

// Duties (8.44.17). Enforced by authz.RequireDuty on each route.
const (
	DutyAssetMaintain   = "eam.asset.maintain"
	DutyCounterRegister = "eam.counter.register"
	DutyPlanAdmin       = "eam.plan.admin"
	DutyRequestCreate   = "eam.request.create"
	DutyPlanner         = "eam.work.plan"
	DutyTechnician      = "eam.work-order.execute"
	DutyInquire         = "eam.inquire"
	DutyPermitIssuer    = "eam.permit.issue"
	DutySetupAdmin      = "eam.setup.admin"
	DutyGLPost          = "eam.gl.post"
	// DutyStructureMaintain covers functional locations and asset placement.
	DutyStructureMaintain = "eam.structure.maintain"
)

var (
	assetType    = regexp.MustCompile(`^[A-Z0-9_]{2,20}$`)
	currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)
	planCode     = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,29}$`)
	// Work order types a caller may request. CAPITAL is accepted so that the
	// store can answer EAM-WO-005. ROUND orders come only from rounds
	// (POST /rounds/{id}/work-orders and ROUND plan lines), and REFURBISHMENT
	// is not enabled until refurbishment (FR-44.20) exists.
	workOrderTyp = map[string]bool{"PREVENTIVE": true, "CORRECTIVE": true, "INSPECTION": true,
		"EMERGENCY": true, "CAPITAL": true}
	assetStates   = map[string]bool{"CREATED": true, "ACTIVE": true, "INACTIVE": true, "DISPOSED": true}
	requestStates = map[string]bool{"NEW": true, "TRIAGED": true, "CONVERTED": true, "REJECTED": true, "DEFERRED": true}
	statuses      = map[domain.Status]bool{domain.StatusCreated: true, domain.StatusScheduled: true,
		domain.StatusReleased: true, domain.StatusInProgress: true, domain.StatusOnHold: true,
		domain.StatusCompleted: true, domain.StatusClosed: true, domain.StatusCanceled: true}
)

// Handler serves the EAM API.
type Handler struct {
	store  *store.Postgres
	ready  func(context.Context) error
	ledger ledgerclient.Ledger
}

// New returns the API handler. ready backs /readyz.
func New(s *store.Postgres, ready func(context.Context) error) *Handler {
	return &Handler{store: s, ready: ready}
}

// Routes returns the full HTTP handler, tenant middleware included.
func (h *Handler) Routes() http.Handler {
	return tenant.Middleware(h.router())
}

// router declares every route (checked against api/openapi.yaml by a test).
func (h *Handler) router() chi.Router {
	r := httpx.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { httpx.JSON(w, 200, map[string]string{"status": "ok"}) })
	r.Get("/readyz", h.readyz)
	r.Route("/v1/eam", func(r chi.Router) {
		r.With(authz.RequireDuty(DutyInquire)).Get("/workspace", h.workspace)
		r.With(authz.RequireDuty(DutyInquire)).Get("/assets", h.listAssets)
		r.With(authz.RequireDuty(DutyInquire)).Get("/assets/{id}", h.getAsset)
		r.With(authz.RequireDuty(DutyInquire)).Get("/counters/{id}/readings", h.listReadings)
		r.With(authz.RequireDuty(DutyInquire)).Get("/plans", h.listPlans)
		r.With(authz.RequireDuty(DutyInquire)).Get("/requests", h.listRequests)
		r.With(authz.RequireDuty(DutyInquire)).Get("/work-orders", h.listWorkOrders)
		r.With(authz.RequireDuty(DutyInquire)).Get("/work-orders/{id}", h.getWorkOrder)
		r.With(authz.RequireDuty(DutyAssetMaintain)).Post("/assets", h.createAsset)
		r.With(authz.RequireDuty(DutyAssetMaintain)).Post("/assets/{id}/activate", h.activateAsset)
		r.With(authz.RequireDuty(DutyAssetMaintain)).Put("/assets/{id}/legal-entity", h.setAssetLegalEntity)
		r.With(authz.RequireDuty(DutyAssetMaintain)).Post("/assets/{id}/counters", h.createCounter)
		r.With(authz.RequireDuty(DutyAssetMaintain)).Post("/counters/{id}/rules", h.createRule)
		r.With(authz.RequireDuty(DutyCounterRegister)).Post("/counters/{id}/readings", h.recordReading)
		r.With(authz.RequireDuty(DutyPlanAdmin)).Post("/plans", h.createPlan)
		r.With(authz.RequireDuty(DutyPlanAdmin)).Post("/plans/schedule", h.schedulePlans)
		r.With(authz.RequireDuty(DutyRequestCreate)).Post("/requests", h.createRequest)
		r.With(authz.RequireDuty(DutyPlanner)).Post("/requests/{id}/convert", h.convertRequest)
		r.With(authz.RequireDuty(DutyPlanner)).Post("/work-orders", h.createWorkOrder)
		r.With(anyDuty(DutyPlanner, DutyTechnician)).Post("/work-orders/{id}/status", h.transitionWorkOrder)
		r.With(authz.RequireDuty(DutyTechnician)).Post("/work-orders/{id}/labor", h.recordLabor)
		r.With(authz.RequireDuty(DutyInquire)).Get("/kpis", h.kpis)
		// Slice 2: lifecycle, criticality, warranties, fault catalogs and faults, downtime, triage.
		r.With(authz.RequireDuty(DutyAssetMaintain)).Post("/assets/{id}/lifecycle", h.changeLifecycle)
		r.With(authz.RequireDuty(DutyInquire)).Get("/assets/{id}/criticality", h.listAssessments)
		r.With(authz.RequireDuty(DutyAssetMaintain)).Post("/assets/{id}/criticality", h.assessCriticality)
		r.With(authz.RequireDuty(DutyInquire)).Get("/assets/{id}/warranties", h.listWarranties)
		r.With(authz.RequireDuty(DutyAssetMaintain)).Post("/assets/{id}/warranties", h.createWarranty)
		r.With(authz.RequireDuty(DutyInquire)).Get("/fault-catalogs", h.listFaultCatalogs)
		r.With(authz.RequireDuty(DutyInquire)).Get("/fault-catalogs/{assetType}", h.getFaultCatalog)
		r.With(authz.RequireDuty(DutyPlanAdmin)).Put("/fault-catalogs/{assetType}", h.putFaultCatalog)
		r.With(authz.RequireDuty(DutyTechnician)).Post("/work-orders/{id}/faults", h.registerFault)
		r.With(anyDuty(DutyPlanner, DutyTechnician)).Post("/work-orders/{id}/downtime", h.recordDowntime)
		r.With(authz.RequireDuty(DutyPlanner)).Post("/requests/{id}/triage", h.triageRequest)
		// Slice 3: permits to work and lockout/tagout (FR-44.19, BR-44.11).
		r.With(authz.RequireDuty(DutySetupAdmin)).Post("/permit-types", h.upsertPermitType)
		r.With(authz.RequireDuty(DutyInquire)).Get("/permit-types", h.listPermitTypes)
		r.With(authz.RequireDuty(DutyInquire)).Get("/job-types/{jobType}/permits", h.getJobPermits)
		r.With(authz.RequireDuty(DutySetupAdmin)).Put("/job-types/{jobType}/permits", h.setJobPermits)
		r.With(authz.RequireDuty(DutySetupAdmin)).Post("/assets/{id}/isolation-points", h.createIsolationPoint)
		r.With(authz.RequireDuty(DutyInquire)).Get("/assets/{id}/isolation-points", h.listIsolationPoints)
		r.With(authz.RequireDuty(DutyPlanner)).Post("/permits", h.requestPermit)
		r.With(authz.RequireDuty(DutyInquire)).Get("/permits", h.listPermits)
		r.With(authz.RequireDuty(DutyPermitIssuer)).Post("/permits/expire", h.expirePermits)
		r.With(authz.RequireDuty(DutyInquire)).Get("/permits/{id}", h.getPermit)
		r.With(authz.RequireDuty(DutyPermitIssuer)).Post("/permits/{id}/transition", h.transitionPermit)
		r.With(authz.RequireDuty(DutyTechnician)).Post("/permits/{id}/locks", h.recordLock)
		// Slice 4: functional locations (FR-44.1, FR-44.2) and maintenance sequences (FR-44.15).
		r.With(authz.RequireDuty(DutyStructureMaintain)).Post("/functional-locations", h.createLocation)
		r.With(authz.RequireDuty(DutyInquire)).Get("/functional-locations", h.listLocations)
		r.With(authz.RequireDuty(DutyInquire)).Get("/functional-locations/{id}", h.getLocation)
		r.With(authz.RequireDuty(DutyStructureMaintain)).Post("/assets/{id}/location", h.placeAsset)
		r.With(authz.RequireDuty(DutyInquire)).Get("/assets/{id}/location-history", h.listLocationHistory)
		r.With(authz.RequireDuty(DutyPlanAdmin)).Post("/sequences", h.createSequence)
		r.With(authz.RequireDuty(DutyInquire)).Get("/sequences", h.listSequences)
		r.With(authz.RequireDuty(DutyInquire)).Get("/sequences/{id}", h.getSequence)
		r.With(authz.RequireDuty(DutyInquire)).Get("/sequences/{id}/simulation", h.simulateSequence)
		r.With(authz.RequireDuty(DutyPlanAdmin)).Post("/sequences/{id}/occurrences", h.generateOccurrence)
		r.With(authz.RequireDuty(DutyInquire)).Get("/sequences/{id}/occurrences", h.listOccurrences)
		// Slice 5: maintenance rounds (FR-44.14, BR-44.9), reminders (BR-44.7, BR-44.15), cost and backlog KPIs (FR-44.13).
		r.With(authz.RequireDuty(DutyPlanAdmin)).Post("/rounds", h.createRound)
		r.With(authz.RequireDuty(DutyInquire)).Get("/rounds", h.listRounds)
		r.With(authz.RequireDuty(DutyInquire)).Get("/rounds/{id}", h.getRound)
		r.With(authz.RequireDuty(DutyPlanner)).Post("/rounds/{id}/work-orders", h.createRoundWorkOrder)
		r.With(authz.RequireDuty(DutyTechnician)).Post("/work-orders/{id}/stops/{seq}", h.recordStop)
		r.With(authz.RequireDuty(DutyInquire)).Get("/work-orders/emergency-overdue", h.emergencyOverdue)
		r.With(authz.RequireDuty(DutyInquire)).Get("/assets/plan-reviews", h.planReviews)
		r.With(authz.RequireDuty(DutyInquire)).Get("/kpis/cost", h.costByAsset)
		r.With(authz.RequireDuty(DutyInquire)).Get("/kpis/backlog", h.backlog)
		// Work order costs in the general ledger (FR-44.11).
		r.With(authz.RequireDuty(DutyInquire)).Get("/posting-profiles", h.listPostingProfiles)
		r.With(authz.RequireDuty(DutySetupAdmin)).Put("/posting-profiles", h.savePostingProfile)
		r.With(authz.RequireDuty(DutyGLPost)).Post("/gl-postings", h.postGL)
		r.With(authz.RequireDuty(DutyInquire)).Get("/gl-reconciliation", h.glReconciliation)
	})
	return r
}

// anyDuty passes a request holding at least one of the duties.
func anyDuty(duties ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := tenant.FromContext(r.Context())
			if !ok {
				httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
				return
			}
			for _, d := range duties {
				if p.HasDuty(d) {
					next.ServeHTTP(w, r)
					return
				}
			}
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "missing duty"))
		})
	}
}

func (h *Handler) readyz(w http.ResponseWriter, r *http.Request) {
	if err := h.ready(r.Context()); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "EAM-READY-001", "database not ready"))
		return
	}
	httpx.JSON(w, 200, map[string]string{"status": "ready"})
}

func invalid(detail string) error {
	return httpx.NewProblem(http.StatusBadRequest, store.CodeValidation, detail)
}

// idempotencyKey requires the header on creating and custom-method POSTs.
func idempotencyKey(r *http.Request) (string, error) {
	k := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if k == "" {
		return "", httpx.NewProblem(http.StatusBadRequest, "idempotency_key_required", "Idempotency-Key header is required")
	}
	if len(k) > 128 {
		return "", invalid("Idempotency-Key must be at most 128 characters")
	}
	return k, nil
}

// ifMatch reads the expected aggregate version from If-Match (428 when absent).
func ifMatch(r *http.Request) (int, error) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	if raw == "" {
		return 0, httpx.NewProblem(http.StatusPreconditionRequired, "precondition_required", "If-Match header is required")
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		return 0, invalid("If-Match must carry the version number")
	}
	return v, nil
}

func pathID(r *http.Request) (string, error) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		return "", invalid("path id must be a UUID")
	}
	return id, nil
}

func decode(r *http.Request, v any) error {
	if err := httpx.Decode(r, v); err != nil {
		return invalid(err.Error())
	}
	return nil
}

func fail(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		httpx.Fail(w, r, err)
	}
}

func (h *Handler) createAsset(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var in store.Asset
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	switch {
	case strings.TrimSpace(in.Name) == "" || len(in.Name) > 120:
		fail(w, r, invalid("name must be 1-120 characters"))
		return
	case !assetType.MatchString(in.AssetType):
		fail(w, r, invalid("assetType must match ^[A-Z0-9_]{2,20}$"))
		return
	case !ids.Valid(in.SiteID):
		fail(w, r, invalid("siteId must be a UUID"))
		return
	case in.Criticality < 1 || in.Criticality > 5:
		fail(w, r, invalid("criticality must be 1-5"))
		return
	case in.ProductionResourceID != nil && !ids.Valid(*in.ProductionResourceID):
		fail(w, r, invalid("productionResourceId must be a UUID"))
		return
	}
	out, err := h.store.CreateAsset(r.Context(), in, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) activateAsset(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ActivateAsset(r.Context(), id, version)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type counterIn struct {
	Name       string `json:"name"`
	Unit       string `json:"unit"`
	Cumulative *bool  `json:"cumulative,omitempty"`
	Source     string `json:"source,omitempty"`
}

func (h *Handler) createCounter(w http.ResponseWriter, r *http.Request) {
	id, key, in, ok := h.prepareCounter(w, r)
	if !ok {
		return
	}
	out, err := h.store.CreateCounter(r.Context(), id, in, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) prepareCounter(w http.ResponseWriter, r *http.Request) (string, string, store.Counter, bool) {
	var zero store.Counter
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return "", "", zero, false
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return "", "", zero, false
	}
	var body counterIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return "", "", zero, false
	}
	if body.Name == "" || len(body.Name) > 60 || body.Unit == "" || len(body.Unit) > 12 {
		fail(w, r, invalid("name must be 1-60 and unit 1-12 characters"))
		return "", "", zero, false
	}
	if body.Source == "" {
		body.Source = "MANUAL"
	}
	if body.Source != "MANUAL" && body.Source != "IOT" {
		fail(w, r, invalid("source must be MANUAL or IOT"))
		return "", "", zero, false
	}
	cumulative := true
	if body.Cumulative != nil {
		cumulative = *body.Cumulative
	}
	return id, key, store.Counter{Name: body.Name, Unit: body.Unit, Cumulative: cumulative, Source: body.Source}, true
}

type ruleIn struct {
	Name            string  `json:"name"`
	Operator        string  `json:"operator"`
	Threshold       float64 `json:"threshold"`
	Severity        int     `json:"severity"`
	CooldownMinutes int     `json:"cooldownMinutes,omitempty"`
}

func (h *Handler) createRule(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body ruleIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	switch {
	case body.Name == "" || len(body.Name) > 120:
		fail(w, r, invalid("name must be 1-120 characters"))
		return
	case body.Operator != ">" && body.Operator != "<":
		fail(w, r, invalid("operator must be > or <"))
		return
	case body.Severity < 1 || body.Severity > 5:
		fail(w, r, invalid("severity must be 1-5"))
		return
	case body.CooldownMinutes != 0 && (body.CooldownMinutes < 1 || body.CooldownMinutes > 1440):
		fail(w, r, invalid("cooldownMinutes must be 1-1440"))
		return
	}
	out, err := h.store.CreateRule(r.Context(), id, store.Rule{Name: body.Name, Operator: body.Operator,
		Threshold: body.Threshold, Severity: body.Severity, CooldownMinutes: body.CooldownMinutes}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type readingIn struct {
	Value     float64    `json:"value"`
	ReadingAt *time.Time `json:"readingAt,omitempty"`
	Source    string     `json:"source,omitempty"`
	Reset     bool       `json:"reset,omitempty"`
	MessageID string     `json:"messageId,omitempty"`
}

func (h *Handler) recordReading(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body readingIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if body.Source == "" {
		body.Source = "MANUAL"
	}
	if body.Source != "MANUAL" && body.Source != "IOT" {
		fail(w, r, invalid("source must be MANUAL or IOT"))
		return
	}
	if len(body.MessageID) > 128 {
		fail(w, r, invalid("messageId must be at most 128 characters"))
		return
	}
	at := time.Now().UTC()
	if body.ReadingAt != nil {
		at = body.ReadingAt.UTC()
	}
	out, replayed, err := h.store.RecordReading(r.Context(), id, store.ReadingInput{Value: body.Value,
		ReadingAt: at, Source: body.Source, Reset: body.Reset, MessageID: body.MessageID}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, out)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) createPlan(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var in store.PlanInput
	if err := decode(r, &in); err != nil {
		fail(w, r, err)
		return
	}
	switch {
	case !planCode.MatchString(in.Code):
		fail(w, r, invalid("code must match ^[A-Z0-9][A-Z0-9_-]{1,29}$"))
		return
	case in.Name == "" || len(in.Name) > 120:
		fail(w, r, invalid("name must be 1-120 characters"))
		return
	case !currencyCode.MatchString(in.Currency):
		fail(w, r, invalid("currency must be an ISO 4217 code"))
		return
	case len(in.Lines) == 0:
		fail(w, r, invalid("a plan needs at least one line"))
		return
	}
	for _, l := range in.Lines {
		if (l.AssetID != "" && !ids.Valid(l.AssetID)) || (l.RoundID != nil && !ids.Valid(*l.RoundID)) {
			fail(w, r, invalid("assetId and roundId must be UUIDs"))
			return
		}
	}
	out, err := h.store.CreatePlan(r.Context(), in, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) schedulePlans(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.SchedulePlans(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type requestIn struct {
	AssetID     string `json:"assetId"`
	Source      string `json:"source"`
	Severity    int    `json:"severity"`
	Description string `json:"description"`
}

func (h *Handler) createRequest(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body requestIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	switch {
	case !ids.Valid(body.AssetID):
		fail(w, r, invalid("assetId must be a UUID"))
		return
	case body.Source != "USER" && body.Source != "MOBILE" && body.Source != "QUALITY":
		fail(w, r, invalid("source must be USER, MOBILE or QUALITY; IoT requests come from condition rules"))
		return
	case body.Severity < 1 || body.Severity > 5:
		fail(w, r, invalid("severity must be 1-5"))
		return
	case strings.TrimSpace(body.Description) == "" || len(body.Description) > 1000:
		fail(w, r, invalid("description must be 1-1000 characters"))
		return
	}
	out, err := h.store.CreateRequest(r.Context(), store.Request{AssetID: body.AssetID, Source: body.Source,
		Severity: body.Severity, Description: body.Description}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) convertRequest(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body struct {
		Currency string `json:"currency"`
	}
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if body.Currency == "" {
		body.Currency = "USD"
	}
	if !currencyCode.MatchString(body.Currency) {
		fail(w, r, invalid("currency must be an ISO 4217 code"))
		return
	}
	out, err := h.store.ConvertRequest(r.Context(), id, body.Currency, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type workOrderIn struct {
	AssetID  string     `json:"assetId"`
	Type     string     `json:"workOrderType"`
	Priority int        `json:"priority"`
	Currency string     `json:"currency"`
	JobType  string     `json:"jobType,omitempty"`
	DueAt    *time.Time `json:"dueAt,omitempty"`
}

func (h *Handler) createWorkOrder(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body workOrderIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	if !ids.Valid(body.AssetID) || !workOrderTyp[body.Type] {
		fail(w, r, invalid("assetId must be a UUID and workOrderType one of PREVENTIVE, CORRECTIVE, INSPECTION, EMERGENCY, CAPITAL"))
		return
	}
	// Priority 0 (omitted) takes the default from the type and the asset's
	// criticality in the store (BR-44.15, BR-44.7).
	if body.Priority < 0 || body.Priority > 5 {
		fail(w, r, invalid("priority must be 1-5"))
		return
	}
	if !currencyCode.MatchString(body.Currency) {
		fail(w, r, invalid("currency must be an ISO 4217 code"))
		return
	}
	if body.JobType != "" && !assetType.MatchString(body.JobType) {
		fail(w, r, invalid("jobType must match ^[A-Z0-9_]{2,20}$"))
		return
	}
	out, err := h.store.CreateWorkOrder(r.Context(), store.WorkOrder{AssetID: body.AssetID, Type: body.Type,
		Status: "CREATED", Priority: body.Priority, Currency: body.Currency, JobType: body.JobType,
		DueAt: body.DueAt}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

type statusIn struct {
	To              string     `json:"to"`
	ActualStart     *time.Time `json:"actualStart,omitempty"`
	ActualEnd       *time.Time `json:"actualEnd,omitempty"`
	DowntimeMinutes *int       `json:"downtimeMinutes,omitempty"`
}

func (h *Handler) transitionWorkOrder(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	version, err := ifMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body statusIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	to := domain.Status(body.To)
	if !statuses[to] {
		fail(w, r, invalid("to must be a work order status"))
		return
	}
	if body.DowntimeMinutes != nil && (*body.DowntimeMinutes < 0 || to != domain.StatusCompleted) {
		fail(w, r, invalid("downtimeMinutes must be a non-negative number of minutes, given on completion"))
		return
	}
	p, _ := tenant.FromContext(r.Context())
	out, err := h.store.TransitionWorkOrder(r.Context(), id, store.Transition{To: to, Version: version,
		ActualStart: body.ActualStart, ActualEnd: body.ActualEnd, DowntimeMinutes: body.DowntimeMinutes,
		Actor: p.UserID}, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

type laborIn struct {
	Hours    string  `json:"hours"`
	Rate     string  `json:"rate"`
	WorkerID *string `json:"workerId,omitempty"`
}

func (h *Handler) recordLabor(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	var body laborIn
	if err := decode(r, &body); err != nil {
		fail(w, r, err)
		return
	}
	hours, err := decimal.NewFromString(body.Hours)
	if err != nil || !hours.IsPositive() {
		fail(w, r, invalid("hours must be a positive decimal"))
		return
	}
	rate, err := decimal.NewFromString(body.Rate)
	if err != nil || rate.IsNegative() {
		fail(w, r, invalid("rate must be a non-negative decimal"))
		return
	}
	if body.WorkerID != nil && !ids.Valid(*body.WorkerID) {
		fail(w, r, invalid("workerId must be a UUID"))
		return
	}
	out, err := h.store.RecordLabor(r.Context(), id, hours, rate, body.WorkerID, key)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, out)
}

func (h *Handler) kpis(w http.ResponseWriter, r *http.Request) {
	asset := r.URL.Query().Get("asset")
	if !ids.Valid(asset) {
		fail(w, r, invalid("asset must be a UUID"))
		return
	}
	from, to, err := window(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.AssetKPI(r.Context(), asset, from, to)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// listOptions reads the shared list query: limit (1-200, default 50), cursor
// (the previous page's nextCursor), and the optional status and asset filters.
func listOptions(r *http.Request, states map[string]bool) (store.ListOptions, error) {
	q := r.URL.Query()
	opt := store.ListOptions{Limit: 50, Status: q.Get("status"), AssetID: q.Get("asset"), Cursor: q.Get("cursor")}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			return opt, invalid("limit must be 1-200")
		}
		opt.Limit = n
	}
	if opt.Cursor != "" && !ids.Valid(opt.Cursor) {
		return opt, invalid("cursor must be the nextCursor of the previous page")
	}
	if opt.AssetID != "" && !ids.Valid(opt.AssetID) {
		return opt, invalid("asset must be a UUID")
	}
	if opt.Status != "" && !states[opt.Status] {
		return opt, invalid("status is not a known state")
	}
	return opt, nil
}

func (h *Handler) workspace(w http.ResponseWriter, r *http.Request) {
	out, err := h.store.Workspace(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) listAssets(w http.ResponseWriter, r *http.Request) {
	opt, err := listOptions(r, assetStates)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ListAssets(r.Context(), opt)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) getAsset(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.GetAsset(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) listReadings(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	opt, err := listOptions(r, nil)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ListReadings(r.Context(), id, opt.Limit)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) listPlans(w http.ResponseWriter, r *http.Request) {
	opt, err := listOptions(r, nil)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ListPlans(r.Context(), opt)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) listRequests(w http.ResponseWriter, r *http.Request) {
	opt, err := listOptions(r, requestStates)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ListRequests(r.Context(), opt)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) listWorkOrders(w http.ResponseWriter, r *http.Request) {
	states := map[string]bool{}
	for st := range statuses {
		states[string(st)] = true
	}
	opt, err := listOptions(r, states)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.ListWorkOrders(r.Context(), opt)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) getWorkOrder(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	out, err := h.store.GetWorkOrder(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
