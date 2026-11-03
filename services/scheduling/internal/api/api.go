// Package api is the HTTP layer of the scheduling service: decode, validate,
// check the tenant, call the store, and write JSON or a problem.
package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

const (
	readyTimeout     = 500 * time.Millisecond
	idempotencyKey   = "Idempotency-Key"
	dutyRead         = "scheduling.read"
	dutyRun          = "scheduling.run"
	dutySchedule     = "scheduling.schedule.write"
	dutyCapacity     = "scheduling.capacity.write"
	validationDetail = "request failed validation"
	maxBody          = 4 << 20
	maxSelection     = 5000
	maxRunTimeZone   = 64
)

var resourceCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,31}$`)

// Config is the service configuration the handlers need.
type Config struct {
	HorizonDays    int // SCHED_HORIZON_DAYS
	BottleneckPct  int // SCHED_BOTTLENECK_PCT
	BottleneckWeek int // consecutive weeks above the threshold
}

// DefaultConfig matches the PRD defaults.
var DefaultConfig = Config{HorizonDays: 365, BottleneckPct: 90, BottleneckWeek: 3}

// API holds the store, configuration and counters.
type API struct {
	store    store.Store
	cfg      Config
	seq      CampaignSequencer
	created  atomic.Int64
	runs     atomic.Int64
	moves    atomic.Int64
	conflict atomic.Int64
	loads    atomic.Int64
}

// New builds the API.
func New(s store.Store, cfg Config) *API {
	if cfg.HorizonDays == 0 {
		cfg = DefaultConfig
	}
	return &API{store: s, cfg: cfg}
}

type resourceRequest struct {
	Code     string         `json:"code"`
	Name     string         `json:"name"`
	Finite   *bool          `json:"finite"`
	TimeZone string         `json:"timeZone"`
	Shifts   []domain.Shift `json:"shifts"`
	// EfficiencyPct is whole percent from 1 to 500, default 100 (BR-23.1).
	EfficiencyPct *int `json:"efficiencyPct"`
	// EngineeringResourceID is the engineering (wave 09) resource this one
	// stands for; optional.
	EngineeringResourceID *string `json:"engineeringResourceId"`
}

type operationRequest struct {
	OrderID     string     `json:"orderId"`
	OperNumber  int        `json:"operNumber"`
	Minutes     int        `json:"minutes"`
	DueAt       *time.Time `json:"dueAt"`
	ReadyAt     *time.Time `json:"readyAt"`
	Priority    *int       `json:"priority"`
	ResourceIDs []string   `json:"resourceIds"`
	// SecondaryResourceIds are booked for the same segments as the primary
	// (BR-23.4); none of them may be a candidate.
	SecondaryResourceIDs []string          `json:"secondaryResourceIds"`
	SetupGroup           *string           `json:"setupGroup"`
	SetupMinutes         *int              `json:"setupMinutes"`
	Attributes           map[string]string `json:"attributes"`
	NetworkID            *string           `json:"networkId"`
}

// Operation attribute limits: block conditions match on these (FR-23.17).
const (
	maxAttributes     = 20
	maxAttributeName  = 64
	maxAttributeValue = 120
)

type runRequest struct {
	Scope        string     `json:"scope"`
	Mode         string     `json:"mode"`
	Finite       *bool      `json:"finite"`
	OperationIDs []string   `json:"operationIds"`
	Start        *time.Time `json:"start"`
	HorizonDays  *int       `json:"horizonDays"`
	Direction    string     `json:"direction"`
}

type moveRequest struct {
	ResourceID string    `json:"resourceId"`
	Start      time.Time `json:"start"`
	DryRun     bool      `json:"dryRun"`
	// Propagate also re-plans the orders pegged to the moved order (FR-23.8).
	Propagate bool `json:"propagate"`
}

type violation struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
	JobID  string `json:"jobId,omitempty"`
}

type moveResponse struct {
	DryRun     bool           `json:"dryRun"`
	Feasible   bool           `json:"feasible"`
	Violations []violation    `json:"violations"`
	Changes    []store.Change `json:"changes"`
	OrderID    string         `json:"orderId,omitempty"`
	OrderStart time.Time      `json:"orderStart,omitempty"`
	OrderEnd   time.Time      `json:"orderEnd,omitempty"`
	// Dependents are the pegged orders that move too, and Truncated reports
	// that the walk stopped at a BR-23.10 limit (SCHED-0007).
	Dependents []store.DependentChange `json:"dependents"`
	Truncated  bool                    `json:"truncated"`
	Run        *store.Run              `json:"run,omitempty"`
}

type loadItem struct {
	ResourceID    string  `json:"resourceId"`
	Period        string  `json:"period"`
	PeriodStart   string  `json:"periodStart"`
	WeekStart     string  `json:"weekStart,omitempty"`
	Start         string  `json:"start"`
	End           string  `json:"end"`
	OpenMinutes   int     `json:"openMinutes"`
	BookedMinutes int     `json:"bookedMinutes"`
	LoadPct       float64 `json:"loadPct"`
	Bottleneck    bool    `json:"bottleneck"`
}

// Register mounts the routes on r. Health endpoints come from httpx.NewRouter.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyCapacity)).Post("/v1/scheduling/resources", a.createResource)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/resources", a.listResources)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/resources/{id}", a.getResource)
	r.With(authz.RequireDuty(dutyCapacity)).Patch("/v1/scheduling/resources/{id}", a.updateResource)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/resources/{id}/capacity", a.resourceCapacity)
	r.With(authz.RequireDuty(dutySchedule)).Post("/v1/scheduling/operations", a.createOperation)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/operations", a.listOperations)
	r.With(authz.RequireDuty(dutyRun)).Post("/v1/scheduling/runs", a.createRun)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/runs", a.listRuns)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/runs/{id}", a.getRun)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/gantt", a.gantt)
	r.With(authz.RequireDuty(dutySchedule)).Post("/v1/scheduling/jobs/{id}:move", a.moveJob)
	r.With(authz.RequireDuty(dutySchedule)).Post("/v1/scheduling/jobs/{id}:firm", a.firmJob)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/capacity-load", a.capacityLoad)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/capacity-load/jobs", a.capacityLoadJobs)
	// Slice 17: run measures and the late orders inquiry (PRD 4.23.14).
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/runs/{id}/kpis", a.runKPIs)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/late-orders", a.lateOrders)

	// Slice 18: production orders received from wave 21 (FR-23.10).
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/order-imports", a.listOrderImports)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/order-imports/{orderId}", a.getOrderImport)
	r.With(authz.RequireDuty(dutySchedule)).Post("/v1/scheduling/order-imports/{orderId}:reimport", a.reimportOrder)

	// Slice 9: downtimes and extra shifts over a resource's calendar (FR-23.1).
	r.With(authz.RequireDuty(dutyCapacity)).Post("/v1/scheduling/resources/{id}/capacity-overrides", a.createCapacityOverride)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/resources/{id}/capacity-overrides", a.listCapacityOverrides)
	r.With(authz.RequireDuty(dutyCapacity)).Delete("/v1/scheduling/resources/{id}/capacity-overrides/{overrideId}", a.deleteCapacityOverride)

	// Slice 4: order pegs that moves propagate along (FR-23.8).
	r.With(authz.RequireDuty(dutySchedule)).Post("/v1/scheduling/order-pegs", a.createPeg)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/order-pegs", a.listPegs)
	r.With(authz.RequireDuty(dutySchedule)).Delete("/v1/scheduling/order-pegs/{id}", a.deletePeg)

	// Slice 2: resource networks, setup matrices, cleanout rules, capacity
	// blocks and campaigns (FR-23.13 to FR-23.17).
	r.With(authz.RequireDuty(dutySetup)).Post("/v1/scheduling/networks", a.createNetwork)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/networks", a.listNetworks)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/networks/{id}", a.getNetwork)
	r.With(authz.RequireDuty(dutySetup)).Patch("/v1/scheduling/networks/{id}", a.updateNetwork)
	r.With(authz.RequireDuty(dutySetup)).Post("/v1/scheduling/setup-matrices", a.createSetupMatrix)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/setup-matrices", a.listSetupMatrices)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/setup-matrices/{id}", a.getSetupMatrix)
	r.With(authz.RequireDuty(dutySetup)).Post("/v1/scheduling/cleanout-rules", a.createCleanoutRule)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/cleanout-rules", a.listCleanoutRules)
	r.With(authz.RequireDuty(dutyCapacity)).Post("/v1/scheduling/capacity-blocks", a.createCapacityBlock)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/capacity-blocks", a.listCapacityBlocks)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/capacity-blocks/{id}", a.getCapacityBlock)
	r.With(authz.RequireDuty(dutyCapacity)).Patch("/v1/scheduling/capacity-blocks/{id}", a.updateCapacityBlock)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/resources/{id}/block-utilization", a.blockUtilization)
	r.With(authz.RequireDuty(dutyCampaign)).Post("/v1/scheduling/campaigns", a.createCampaign)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/campaigns", a.listCampaigns)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/campaigns/{id}", a.getCampaign)
	r.With(authz.RequireDuty(dutyCampaign)).Post("/v1/scheduling/campaigns/{id}/orders", a.addCampaignOrder)
	r.With(authz.RequireDuty(dutyCampaign)).Delete("/v1/scheduling/campaigns/{id}/orders/{orderId}", a.removeCampaignOrder)
	r.With(authz.RequireDuty(dutySchedule)).Post("/v1/scheduling/campaigns/{id}:sequence", a.sequenceCampaign)
	r.With(authz.RequireDuty(dutyCampaign)).Post("/v1/scheduling/campaigns/{id}:start", a.startCampaign)
	r.With(authz.RequireDuty(dutyCampaign)).Post("/v1/scheduling/campaigns/{id}:complete", a.completeCampaign)
	r.With(authz.RequireDuty(dutyCampaign)).Post("/v1/scheduling/campaigns/{id}:cancel", a.cancelCampaign)
	r.With(authz.RequireDuty(dutyCampaign)).Post("/v1/scheduling/campaigns/{id}:allocate-cost", a.allocateCampaignCost)

	// Slice 8: campaign proposals from scheduled orders sharing a key (FR-23.14).
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/scheduling/campaign-proposals", a.campaignProposals)
}

// Metrics serves Prometheus text on the admin port.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP scheduling_resources_created_total Resources created; replays are not counted.\n# TYPE scheduling_resources_created_total counter\nscheduling_resources_created_total %d\n", a.created.Load())
		fmt.Fprintf(w, "# HELP scheduling_runs_total Schedule runs requested.\n# TYPE scheduling_runs_total counter\nscheduling_runs_total %d\n", a.runs.Load())
		fmt.Fprintf(w, "# HELP scheduling_moves_total Job move requests answered.\n# TYPE scheduling_moves_total counter\nscheduling_moves_total %d\n", a.moves.Load())
		fmt.Fprintf(w, "# HELP scheduling_conflicts_total Moves refused for a constraint violation.\n# TYPE scheduling_conflicts_total counter\nscheduling_conflicts_total %d\n", a.conflict.Load())
		fmt.Fprintf(w, "# HELP scheduling_capacity_load_queries_total Capacity load queries answered.\n# TYPE scheduling_capacity_load_queries_total counter\nscheduling_capacity_load_queries_total %d\n", a.loads.Load())
	})
}

func (a *API) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
	defer cancel()
	if err := a.store.Ping(ctx); err != nil {
		slog.Error("readiness check failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func validTenant(w http.ResponseWriter, r *http.Request) bool {
	p, ok := tenant.FromContext(r.Context())
	if !ok || !ids.Valid(p.TenantID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "missing or malformed tenant"))
		return false
	}
	return true
}

// readBody reads the body once, keeps it for decoding, and returns it so the
// idempotency fingerprint can be computed over the same bytes.
func readBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return nil, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request body could not be read")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

// requireIdem reads the Idempotency-Key header and fingerprints the body.
func requireIdem(w http.ResponseWriter, r *http.Request, body []byte) (store.Idem, bool) {
	key := r.Header.Get(idempotencyKey)
	if key == "" || len(key) > 200 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "Idempotency-Key header is required (1 to 200 characters)"))
		return store.Idem{}, false
	}
	sum := sha256.Sum256(body)
	return store.Idem{Key: key, Fingerprint: hex.EncodeToString(sum[:])}, true
}

func invalid(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", validationDetail)
	p.Errors = fields
	httpx.Fail(w, r, p)
}

// decodeBody reads and decodes a JSON body, returning the raw bytes.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) ([]byte, bool) {
	body, err := readBody(r)
	if err != nil {
		httpx.Fail(w, r, err)
		return nil, false
	}
	if err := httpx.Decode(r, v); err != nil {
		httpx.Fail(w, r, err)
		return nil, false
	}
	return body, true
}

func (a *API) createResource(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req resourceRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	fields := map[string]string{}
	if !resourceCode.MatchString(req.Code) {
		fields["code"] = "must be 1 to 32 characters of A-Z, 0-9, _ or -"
	}
	if n := len(strings.TrimSpace(req.Name)); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if _, err := time.LoadLocation(req.TimeZone); err != nil || req.TimeZone == "" || len(req.TimeZone) > maxRunTimeZone || req.TimeZone == "Local" {
		fields["timeZone"] = "must be an IANA time zone name"
	}
	if msg := validShifts(req.Shifts); msg != "" {
		fields["shifts"] = msg
	}
	if req.EfficiencyPct != nil && (*req.EfficiencyPct < 1 || *req.EfficiencyPct > 500) {
		fields["efficiencyPct"] = "must be a whole percent from 1 to 500"
	}
	if req.EngineeringResourceID != nil && *req.EngineeringResourceID != "" && !ids.Valid(strings.ToLower(*req.EngineeringResourceID)) {
		fields["engineeringResourceId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	finite := true
	if req.Finite != nil {
		finite = *req.Finite
	}
	var engineering *string
	if req.EngineeringResourceID != nil && *req.EngineeringResourceID != "" {
		eid := strings.ToLower(*req.EngineeringResourceID)
		engineering = &eid
	}
	efficiency := 100
	if req.EfficiencyPct != nil {
		efficiency = *req.EfficiencyPct
	}
	res, created, err := a.store.CreateResource(r.Context(), store.Resource{Code: req.Code, Name: strings.TrimSpace(req.Name),
		Finite: finite, TimeZone: req.TimeZone, Shifts: req.Shifts, EfficiencyPct: efficiency, EngineeringResourceID: engineering}, idem)
	if err != nil {
		slog.Error("create resource failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		a.created.Add(1)
		status = http.StatusCreated
	} else {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("ETag", etag(res.Version))
	httpx.JSON(w, status, res)
}

func validShifts(shifts []domain.Shift) string {
	if len(shifts) < 1 || len(shifts) > 14 {
		return "must hold 1 to 14 shifts"
	}
	for _, s := range shifts {
		if s.Weekday < time.Sunday || s.Weekday > time.Saturday {
			return "weekday must be 0 (Sunday) to 6 (Saturday)"
		}
		if s.StartMinute < 0 || s.EndMinute > 1440 || s.EndMinute <= s.StartMinute {
			return "startMinute and endMinute must satisfy 0 <= start < end <= 1440"
		}
	}
	return ""
}

func (a *API) createOperation(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req operationRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	req.OrderID = strings.ToLower(req.OrderID)
	for i, id := range req.ResourceIDs {
		req.ResourceIDs[i] = strings.ToLower(id)
	}
	for i, id := range req.SecondaryResourceIDs {
		req.SecondaryResourceIDs[i] = strings.ToLower(id)
	}
	fields := map[string]string{}
	if !ids.Valid(req.OrderID) {
		fields["orderId"] = "must be a UUID"
	}
	if msg := validSecondaries(req.SecondaryResourceIDs, req.ResourceIDs); msg != "" {
		fields["secondaryResourceIds"] = msg
	}
	if req.OperNumber < 1 {
		fields["operNumber"] = "must be at least 1"
	}
	if req.Minutes < 1 || req.Minutes > 100000 {
		fields["minutes"] = "must be 1 to 100000"
	}
	if len(req.ResourceIDs) < 1 || len(req.ResourceIDs) > 50 {
		fields["resourceIds"] = "must list 1 to 50 resources in priority order"
	}
	for _, id := range req.ResourceIDs {
		if !ids.Valid(id) {
			fields["resourceIds"] = "every entry must be a UUID"
		}
	}
	priority := 100
	if req.Priority != nil {
		priority = *req.Priority
		if priority < 0 || priority > 1000 {
			fields["priority"] = "must be 0 to 1000"
		}
	}
	var group *string
	if req.SetupGroup != nil {
		g := strings.TrimSpace(*req.SetupGroup)
		if g == "" || len(g) > domain.MaxSetupGroup {
			fields["setupGroup"] = fmt.Sprintf("must be 1 to %d characters, or omitted", domain.MaxSetupGroup)
		}
		group = &g
	}
	setupMinutes := 0
	if req.SetupMinutes != nil {
		setupMinutes = *req.SetupMinutes
		if setupMinutes < 0 || setupMinutes > maxWeekMinutes {
			fields["setupMinutes"] = fmt.Sprintf("must be 0 to %d", maxWeekMinutes)
		}
	}
	if msg := validAttributes(req.Attributes); msg != "" {
		fields["attributes"] = msg
	}
	if req.NetworkID != nil {
		id := strings.ToLower(*req.NetworkID)
		if !ids.Valid(id) {
			fields["networkId"] = "must be a UUID"
		}
		req.NetworkID = &id
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	op, created, err := a.store.CreateOperation(r.Context(), store.Operation{OrderID: req.OrderID, OperNumber: req.OperNumber,
		Minutes: req.Minutes, DueAt: req.DueAt, ReadyAt: req.ReadyAt, Priority: priority, ResourceIDs: req.ResourceIDs,
		SecondaryResourceIDs: req.SecondaryResourceIDs, SetupGroup: group, SetupMinutes: setupMinutes, Attributes: req.Attributes, NetworkID: req.NetworkID}, idem)
	if err != nil {
		slog.Error("create operation failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	} else {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, status, op)
}

// validSecondaries checks the secondary resources of an operation: at most
// domain.MaxSecondaries distinct UUIDs, none of them a candidate (BR-23.4).
func validSecondaries(secondary, candidates []string) string {
	if len(secondary) > domain.MaxSecondaries {
		return fmt.Sprintf("must list at most %d resources", domain.MaxSecondaries)
	}
	seen := map[string]bool{}
	for _, id := range secondary {
		switch {
		case !ids.Valid(id):
			return "every entry must be a UUID"
		case seen[id]:
			return "must not repeat a resource"
		case slices.Contains(candidates, id):
			return "a candidate resource cannot also be a secondary resource"
		}
		seen[id] = true
	}
	return ""
}

// validAttributes checks the attributes block conditions match on: at most 20
// named values, names and values of bounded length. The campaignKey attribute
// becomes a proposed campaign's key, so it keeps to the campaign key limit.
func validAttributes(attrs map[string]string) string {
	if len(attrs) > maxAttributes {
		return fmt.Sprintf("must hold at most %d attributes", maxAttributes)
	}
	for name, value := range attrs {
		if n := len(strings.TrimSpace(name)); n < 1 || n > maxAttributeName || n != len(name) {
			return fmt.Sprintf("names must be 1 to %d characters without surrounding spaces", maxAttributeName)
		}
		if len(value) > maxAttributeValue {
			return fmt.Sprintf("values must be at most %d characters", maxAttributeValue)
		}
		if name == domain.CampaignKeyAttribute && len(strings.TrimSpace(value)) > maxCampaignKey {
			return fmt.Sprintf("%s must be at most %d characters, the campaign key limit", domain.CampaignKeyAttribute, maxCampaignKey)
		}
	}
	return ""
}

func (a *API) createRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	var req runRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	fields := map[string]string{}
	if req.Scope != "selection" && req.Scope != "site" {
		fields["scope"] = "must be selection or site"
	}
	mode := req.Mode
	if mode == "" {
		mode = "jobs"
	}
	if mode != "jobs" && mode != "operations" {
		fields["mode"] = "must be jobs or operations"
	}
	if req.Direction != "" && req.Direction != "forward" && req.Direction != "backward" {
		fields["direction"] = "must be forward or backward"
	}
	if req.Scope == "selection" && (len(req.OperationIDs) == 0 || len(req.OperationIDs) > maxSelection) {
		fields["operationIds"] = fmt.Sprintf("must list 1 to %d operations for selection scope", maxSelection)
	}
	for i, id := range req.OperationIDs {
		req.OperationIDs[i] = strings.ToLower(id)
		if !ids.Valid(req.OperationIDs[i]) {
			fields["operationIds"] = "every entry must be a UUID"
		}
	}
	if req.Scope == "site" && len(req.OperationIDs) > 0 {
		fields["operationIds"] = "must be omitted for site scope"
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	if req.Start != nil && req.Start.Before(today) {
		fields["start"] = "must not be before today (FR-23.4: forward scheduling starts today or later)"
	}
	horizon := a.cfg.HorizonDays
	if req.HorizonDays != nil {
		horizon = *req.HorizonDays
		if horizon < 30 || horizon > 730 {
			fields["horizonDays"] = "must be 30 to 730"
		}
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	finite := true
	if req.Finite != nil {
		finite = *req.Finite
	}
	start := time.Now().UTC().Truncate(time.Minute)
	if req.Start != nil {
		start = req.Start.UTC()
	}
	a.runs.Add(1)
	run, created, err := a.store.RunSchedule(r.Context(), store.RunRequest{Scope: req.Scope, Mode: mode, Finite: finite,
		OperationIDs: req.OperationIDs, Start: start, HorizonDays: horizon, Backward: req.Direction == "backward"}, idem)
	if err != nil {
		slog.Error("schedule run failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, status, run)
}

func (a *API) getRun(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeRunNotFound, "schedule run not found"))
		return
	}
	run, err := a.store.GetRun(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, run)
}

type firmRequest struct {
	Firmed *bool `json:"firmed"`
}

// firmJob firms or unfirms a placement (FR-23.4). The outcome is stored under
// the Idempotency-Key, so a retried click returns the first answer.
func (a *API) firmJob(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	jobID := strings.ToLower(chi.URLParam(r, "id"))
	var req firmRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	idem, ok := requireIdem(w, r, body)
	if !ok {
		return
	}
	fields := map[string]string{}
	if !ids.Valid(jobID) {
		fields["id"] = "must be a UUID"
	}
	if req.Firmed == nil {
		fields["firmed"] = "is required (true to firm, false to unfirm)"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	idem.Fingerprint = fingerprint(jobID, body)
	out, replayed, err := a.store.FirmJob(r.Context(), jobID, *req.Firmed, idem)
	if err != nil {
		slog.Error("firm job failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (a *API) gantt(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	from, errFrom := time.Parse(time.RFC3339, q.Get("from"))
	to, errTo := time.Parse(time.RFC3339, q.Get("to"))
	if errFrom != nil || errTo != nil || !to.After(from) {
		invalid(w, r, map[string]string{"from": "from and to are RFC 3339 times with to after from"})
		return
	}
	var resourceIDs []string
	if raw := q.Get("resourceIds"); raw != "" {
		resourceIDs = strings.Split(raw, ",")
		for _, id := range resourceIDs {
			if !ids.Valid(id) {
				invalid(w, r, map[string]string{"resourceIds": "must be a comma list of UUIDs"})
				return
			}
		}
	}
	bars, err := a.store.Gantt(r.Context(), from, to, resourceIDs)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": bars})
}

// moveJob previews (dryRun) or commits a move. A commit requires an
// Idempotency-Key; a preview writes nothing and needs none.
func (a *API) moveJob(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	jobID := strings.ToLower(chi.URLParam(r, "id"))
	var req moveRequest
	body, ok := decodeBody(w, r, &req)
	if !ok {
		return
	}
	var idem store.Idem
	if !req.DryRun {
		if idem, ok = requireIdem(w, r, body); !ok {
			return
		}
		idem.Fingerprint = fingerprint(jobID, body)
	}
	fields := map[string]string{}
	if !ids.Valid(jobID) {
		fields["id"] = "must be a UUID"
	}
	if req.Start.IsZero() {
		fields["start"] = "is required"
	}
	if req.ResourceID != "" && !ids.Valid(req.ResourceID) {
		fields["resourceId"] = "must be a UUID"
	}
	if len(fields) > 0 {
		invalid(w, r, fields)
		return
	}
	a.moves.Add(1)
	out, replayed, err := a.store.MoveJob(r.Context(), store.MoveRequest{JobID: jobID, ResourceID: strings.ToLower(req.ResourceID),
		Start: req.Start.UTC(), HorizonDays: a.cfg.HorizonDays, DryRun: req.DryRun, Propagate: req.Propagate}, idem)
	if err != nil {
		slog.Error("move job failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if out.Conflict != nil {
		a.conflict.Add(1)
		if req.DryRun {
			httpx.JSON(w, http.StatusOK, moveResponse{DryRun: true, Feasible: false,
				Violations: []violation{{Code: out.Conflict.Code, Detail: out.Conflict.Detail, JobID: out.Conflict.JobID}}, Changes: []store.Change{},
				Dependents: []store.DependentChange{}, Truncated: out.Truncated})
			return
		}
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, out.Conflict.Code, out.Conflict.Detail))
		return
	}
	if replayed {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	deps := out.Dependents
	if deps == nil {
		deps = []store.DependentChange{}
	}
	httpx.JSON(w, http.StatusOK, moveResponse{DryRun: req.DryRun, Feasible: true, Violations: []violation{}, Changes: out.Changes,
		OrderID: out.OrderID, OrderStart: out.OrderStart, OrderEnd: out.OrderEnd, Dependents: deps, Truncated: out.Truncated, Run: out.Run})
}

func (a *API) capacityLoad(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	from := time.Now().UTC()
	if raw := q.Get("from"); raw != "" {
		parsed, err := time.Parse(time.DateOnly, raw)
		if err != nil {
			invalid(w, r, map[string]string{"from": "must be a date (YYYY-MM-DD)"})
			return
		}
		from = parsed
	}
	period := q.Get("period")
	if period != "" && period != "week" && period != "day" {
		invalid(w, r, map[string]string{"period": "must be week or day"})
		return
	}
	if period == "day" {
		a.dailyLoad(w, r, from)
		return
	}
	weeks := 4
	if raw := q.Get("weeks"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 53 {
			invalid(w, r, map[string]string{"weeks": "must be 1 to 53"})
			return
		}
		weeks = n
	}
	resourceIDs, ok := uuidList(w, r, "resourceIds")
	if !ok {
		return
	}
	loads, err := a.store.CapacityLoad(r.Context(), store.LoadFilter{ResourceIDs: resourceIDs, From: from, Weeks: weeks,
		ThresholdPct: a.cfg.BottleneckPct, Consecutive: a.cfg.BottleneckWeek})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.loads.Add(1)
	items := make([]loadItem, 0, len(loads))
	for _, l := range loads {
		items = append(items, loadItem{ResourceID: l.ResourceID, Period: "week", PeriodStart: l.WeekStart.Format(time.DateOnly),
			WeekStart: l.WeekStart.Format(time.DateOnly), Start: l.WeekStart.Format(time.RFC3339), End: l.WeekStart.AddDate(0, 0, 7).Format(time.RFC3339),
			OpenMinutes:   l.OpenMinutes,
			BookedMinutes: l.BookedMinutes, LoadPct: math.Round(l.LoadPct*10) / 10, Bottleneck: l.Bottleneck})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// dailyLoad answers capacity-load?period=day: `days` local days of each
// resource's calendar time zone from the date `from` (default 14, at most 93).
// Days carry no bottleneck flag (FR-23.9).
func (a *API) dailyLoad(w http.ResponseWriter, r *http.Request, from time.Time) {
	days := 14
	if raw := r.URL.Query().Get("days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxLoadDays {
			invalid(w, r, map[string]string{"days": fmt.Sprintf("must be 1 to %d", store.MaxLoadDays)})
			return
		}
		days = n
	}
	resourceIDs, ok := uuidList(w, r, "resourceIds")
	if !ok {
		return
	}
	loads, err := a.store.DailyCapacityLoad(r.Context(), store.DailyLoadFilter{ResourceIDs: resourceIDs, From: from, Days: days})
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	a.loads.Add(1)
	items := make([]loadItem, 0, len(loads))
	for _, l := range loads {
		items = append(items, loadItem{ResourceID: l.ResourceID, Period: "day", PeriodStart: l.Day.Format(time.DateOnly),
			Start: l.Start.UTC().Format(time.RFC3339), End: l.End.UTC().Format(time.RFC3339), OpenMinutes: l.OpenMinutes,
			BookedMinutes: l.BookedMinutes, LoadPct: math.Round(l.LoadPct*10) / 10})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// capacityLoadJobs lists the jobs booked on a resource in [from, to): the
// drill-down behind a capacity load cell (PRD 4.23.14 Capacity load detail).
func (a *API) capacityLoadJobs(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	id := strings.ToLower(q.Get("resourceId"))
	if !ids.Valid(id) {
		invalid(w, r, map[string]string{"resourceId": "must be a resource UUID"})
		return
	}
	from, errFrom := time.Parse(time.RFC3339, q.Get("from"))
	to, errTo := time.Parse(time.RFC3339, q.Get("to"))
	if errFrom != nil || errTo != nil || !to.After(from) || to.Sub(from) > store.MaxLoadDays*24*time.Hour {
		invalid(w, r, map[string]string{"from": fmt.Sprintf("from and to are RFC 3339 times, to after from, at most %d days apart", store.MaxLoadDays)})
		return
	}
	items, truncated, err := a.store.LoadJobs(r.Context(), id, from.UTC(), to.UTC())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items, "truncated": truncated})
}

// uuidList parses an optional comma list of UUIDs from the query string.
func uuidList(w http.ResponseWriter, r *http.Request, name string) ([]string, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return nil, true
	}
	out := strings.Split(strings.ToLower(raw), ",")
	if len(out) > 500 {
		invalid(w, r, map[string]string{name: "must list at most 500 ids"})
		return nil, false
	}
	for _, id := range out {
		if !ids.Valid(id) {
			invalid(w, r, map[string]string{name: "must be a comma list of UUIDs"})
			return nil, false
		}
	}
	return out, true
}

// limitParam reads ?limit= with a default and an upper bound.
func limitParam(w http.ResponseWriter, r *http.Request, def, max int) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return def, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > max {
		invalid(w, r, map[string]string{"limit": fmt.Sprintf("must be 1 to %d", max)})
		return 0, false
	}
	return n, true
}

// fingerprint binds the replay record to the target and the body, so the same
// key reused on another job is refused rather than answered with a stale result.
func fingerprint(target string, body []byte) string {
	sum := sha256.Sum256(append([]byte(target+"\x00"), body...))
	return hex.EncodeToString(sum[:])
}

func (a *API) listResources(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	items, err := a.store.ListResources(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) getResource(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeResourceNotFound, "resource not found"))
		return
	}
	res, err := a.store.GetResource(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(res.Version))
	httpx.JSON(w, http.StatusOK, res)
}

type intervalItem struct {
	Start   time.Time `json:"start"`
	End     time.Time `json:"end"`
	Minutes int       `json:"minutes"`
}

// resourceCapacity lists the open intervals of a resource in [from, to).
func (a *API) resourceCapacity(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeResourceNotFound, "resource not found"))
		return
	}
	q := r.URL.Query()
	from, errFrom := time.Parse(time.RFC3339, q.Get("from"))
	to, errTo := time.Parse(time.RFC3339, q.Get("to"))
	if errFrom != nil || errTo != nil || !to.After(from) || to.Sub(from) > store.MaxCapacityDays*24*time.Hour {
		invalid(w, r, map[string]string{"from": fmt.Sprintf("from and to are RFC 3339 times, to after from, at most %d days apart", store.MaxCapacityDays)})
		return
	}
	ivs, err := a.store.ResourceCapacity(r.Context(), id, from.UTC(), to.UTC())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]intervalItem, 0, len(ivs))
	for _, iv := range ivs {
		items = append(items, intervalItem{Start: iv.Start, End: iv.End, Minutes: iv.Minutes()})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) listOperations(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	f := store.OperationFilter{OrderID: strings.ToLower(q.Get("orderId")), Status: q.Get("status")}
	if f.OrderID != "" && !ids.Valid(f.OrderID) {
		invalid(w, r, map[string]string{"orderId": "must be a UUID"})
		return
	}
	switch f.Status {
	case "", "open", "scheduled", "unplaced":
	default:
		invalid(w, r, map[string]string{"status": "must be open, scheduled or unplaced"})
		return
	}
	limit, ok := limitParam(w, r, 200, 1000)
	if !ok {
		return
	}
	f.Limit = limit
	items, err := a.store.ListOperations(r.Context(), f)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *API) listRuns(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	limit, ok := limitParam(w, r, 50, 200)
	if !ok {
		return
	}
	items, err := a.store.ListRuns(r.Context(), limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
