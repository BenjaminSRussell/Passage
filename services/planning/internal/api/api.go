// Package api exposes the planning HTTP surface under /v1/planning.
package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/planning/internal/domain"
	"github.com/BenjaminSRussell/passage/services/planning/internal/store"
)

const (
	dutyRun              = "planning.run"
	dutyRead             = "planning.read"
	dutyMaintainOrders   = "planning.order.maintain"    // Maintain planned orders
	dutyMaintainForecast = "planning.forecast.maintain" // Maintain forecasts
	dutyFirmOrders       = "planning.order.firm"        // Firm planned orders
	dutyApproveForecast  = "planning.forecast.approve"  // Approve forecast consensus
	dutyMaintainSetup    = "planning.setup.maintain"    // Maintain planning setup (4.22.17)
	dutySimulate         = "planning.simulate"          // Run simulations: scenario create, run, compare (4.22.17)

	idempotencyKey = "Idempotency-Key"
	dateLayout     = "2006-01-02"
	maxItems       = 100000
	maxClosedDates = 1000
	maxMarginDays  = 365
	maxPegDepth    = 20
	defaultDepth   = 5
	readyTimeout   = 2 * time.Second

	codeValidation = "COM-0001"
	codeInput      = "PLAN-INPUT-INVALID"
)

var (
	planCodePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,19}$`)
	knownRefTypes   = map[string]bool{"sales-line": true, "forecast": true, "safety-stock": true, "bom-line": true,
		"transfer-issue": true, "on-hand": true, "purchase-line": true, "production-order": true, "transfer-receipt": true, "planned-order": true}
	defaultPriority = map[string]int{"sales-line": domain.PrioritySales, "forecast": domain.PriorityForecast}
)

// API holds the handlers and their counters.
type API struct {
	store store.Store
	now   func() time.Time

	runsCompleted atomic.Int64
	runsReplayed  atomic.Int64
	runsRefused   atomic.Int64

	// horizonDays is the default planning horizon (0: none), see WithHorizon.
	horizonDays int
}

// New returns an API over st.
func New(st store.Store) *API { return &API{store: st, now: time.Now} }

// Register mounts the routes on r. /readyz replaces the router's static probe
// with one that checks the database, so the chart's readiness probe fails
// closed when Postgres is unreachable.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.With(authz.RequireDuty(dutyRun)).Post("/v1/planning/plans/{planCode}:run", a.runPlan)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/plans/{planCode}/versions", a.listVersions)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/plans/{planCode}/versions/{number}", a.getVersion)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/plans/{planCode}/diff", a.diffVersions)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/plans/{planCode}/projection", a.planProjection)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/planned-orders", a.listPlannedOrders)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/planned-orders/{id}", a.getPlannedOrder)
	r.With(authz.RequireDuty(dutyMaintainOrders)).Patch("/v1/planning/planned-orders/{id}", a.patchPlannedOrder)
	r.With(authz.RequireDuty(dutyMaintainOrders)).Delete("/v1/planning/planned-orders/{id}", a.deletePlannedOrder)
	r.With(authz.RequireDuty(dutyFirmOrders)).Post("/v1/planning/planned-orders:firm", a.firmPlannedOrders)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/planned-orders/{id}/pegging", a.pegging)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/messages", a.listMessages)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/net-requirements", a.listNetRequirements)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/forecasts/{modelCode}/lines", a.getForecastLines)
	r.With(authz.RequireDuty(dutyMaintainForecast)).Put("/v1/planning/forecasts/{modelCode}/lines", a.putForecastLines)
	r.With(authz.RequireDuty(dutyMaintainForecast)).Post("/v1/planning/forecasts/{modelCode}:submit", a.submitForecast)
	r.With(authz.RequireDuty(dutyApproveForecast)).Post("/v1/planning/forecasts/{modelCode}:publish", a.publishForecast)
	r.With(authz.RequireDuty(dutyApproveForecast)).Post("/v1/planning/forecasts/{modelCode}:reject", a.rejectForecast)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/coverage-groups", a.listCoverageGroups)
	r.With(authz.RequireDuty(dutyMaintainSetup)).Post("/v1/planning/coverage-groups", a.createCoverageGroup)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/coverage-groups/{code}", a.getCoverageGroup)
	r.With(authz.RequireDuty(dutyMaintainSetup)).Put("/v1/planning/coverage-groups/{code}", a.updateCoverageGroup)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/item-coverages", a.listItemCoverages)
	r.With(authz.RequireDuty(dutyMaintainSetup)).Post("/v1/planning/item-coverages", a.createItemCoverage)
	r.With(authz.RequireDuty(dutyRead)).Get("/v1/planning/item-coverages/{id}", a.getItemCoverage)
	r.With(authz.RequireDuty(dutyMaintainSetup)).Put("/v1/planning/item-coverages/{id}", a.updateItemCoverage)
	a.registerScenarios(r)
	a.registerBuffers(r)
	a.registerStrategies(r)
	a.registerQuotaArrangements(r)
	a.registerCalculators(r)
}

// Metrics renders the counters in Prometheus text format.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "# HELP planning_plan_runs_completed_total Plan versions that became current.\n# TYPE planning_plan_runs_completed_total counter\nplanning_plan_runs_completed_total %d\n", a.runsCompleted.Load())
		fmt.Fprintf(w, "# HELP planning_plan_runs_replayed_total Plan runs answered from an Idempotency-Key replay.\n# TYPE planning_plan_runs_replayed_total counter\nplanning_plan_runs_replayed_total %d\n", a.runsReplayed.Load())
		fmt.Fprintf(w, "# HELP planning_plan_runs_refused_total Plan runs refused (invalid input, cycle, lock).\n# TYPE planning_plan_runs_refused_total counter\nplanning_plan_runs_refused_total %d\n", a.runsRefused.Load())
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

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, codeValidation, "request failed validation")
	p.Errors = fields
	httpx.Fail(w, r, p)
}

type runRequest struct {
	AsOf   string `json:"asOf"`
	SiteID string `json:"siteId"`
	// LegalEntityID owns the plan when this run creates it; for an existing plan it is an assertion of its owner.
	LegalEntityID string `json:"legalEntityId,omitempty"`
	// ForecastModel, when set, adds the model's reduced forecast at the site
	// as forecast demand of the snapshot's items (FR-22.8).
	ForecastModel string `json:"forecastModel,omitempty"`
	// Calendar is the site work calendar of the run (FR-22.5, BR-22.8); without
	// it lead times and margins count calendar days.
	Calendar *calendarDTO `json:"calendar,omitempty"`
	// CarryForward adds the plan's firmed planned orders that are not yet
	// delivered, and the approved orders of the current version that the run
	// firms (FR-22.11), as scheduled receipts of the run (FR-22.1, FR-22.10),
	// so a firmed order is supply and is not planned a second time.
	CarryForward bool `json:"carryForward,omitempty"`
	// HorizonDays ignores demand dated beyond that many days from asOf (30 to
	// 1,095); 0 takes the service default (PLANNING_HORIZON_DAYS).
	HorizonDays int `json:"horizonDays,omitempty"`
	// Scope restricts the items the version reports (FR-22.13).
	Scope *scopeDTO `json:"scope,omitempty"`
	Items []itemDTO `json:"items"`
}

// calendarDTO names the closed days of the site: whole weekdays (0 Sunday to
// 6 Saturday) and single dates such as public holidays.
type calendarDTO struct {
	ClosedWeekdays []int    `json:"closedWeekdays"`
	ClosedDates    []string `json:"closedDates"`
}

// validate returns the field error of the calendar, or "" when it is usable.
func (c *calendarDTO) validate() string {
	closed := map[int]bool{}
	for _, wd := range c.ClosedWeekdays {
		if wd < 0 || wd > 6 {
			return "closedWeekdays must be 0 (Sunday) to 6 (Saturday)"
		}
		closed[wd] = true
	}
	if len(closed) == 7 {
		return "at least one weekday must be open"
	}
	if len(c.ClosedDates) > maxClosedDates {
		return fmt.Sprintf("closedDates holds at most %d dates", maxClosedDates)
	}
	for _, s := range c.ClosedDates {
		if _, err := time.Parse(dateLayout, s); err != nil {
			return "closedDates must be dates as YYYY-MM-DD"
		}
	}
	return ""
}

// toDomain converts a validated calendar into engine form; day 0 is asOf. A
// nil calendar stays nil (calendar days).
func (c *calendarDTO) toDomain(asOf time.Time) *domain.Calendar {
	if c == nil {
		return nil
	}
	cal := &domain.Calendar{Weekday0: int(asOf.Weekday()), ClosedDays: map[int]bool{}}
	for _, wd := range c.ClosedWeekdays {
		cal.ClosedWeekdays[wd] = true
	}
	for _, s := range c.ClosedDates {
		d, _ := time.Parse(dateLayout, s)
		cal.ClosedDays[int(d.Sub(asOf)/(24*time.Hour))] = true
	}
	return cal
}

type itemDTO struct {
	ItemID string `json:"itemId"`
	// ItemGroup and Planner are item master attributes the plan scope filters on (FR-22.13).
	ItemGroup string `json:"itemGroup,omitempty"`
	Planner   string `json:"planner,omitempty"`
	// VendorID is the default vendor of the item's planned purchase orders (FR-22.3);
	// a stored site-scope item coverage supplies it when left out.
	VendorID    string          `json:"vendorId,omitempty"`
	Coverage    coverageDTO     `json:"coverage"`
	OnHand      decimal.Decimal `json:"onHand"`
	SafetyStock decimal.Decimal `json:"safetyStock"`
	// OrderType, LeadTimeDays and a coverage without a code are taken from
	// the item's stored site-scope item coverage when left out (run_coverage.go).
	OrderType    string           `json:"orderType"`
	LeadTimeDays *int             `json:"leadTimeDays"`
	Lot          lotDTO           `json:"lot"`
	Demand       []requirementDTO `json:"demand"`
	Receipts     []requirementDTO `json:"receipts"`
	Components   []bomDTO         `json:"components"`
}

type coverageDTO struct {
	Code       string `json:"code"`
	PeriodDays int    `json:"periodDays"`
	MarginDays int    `json:"marginDays"`
	// IssueMarginDays moves demand earlier and ReorderMarginDays lengthens the
	// lead time of planned orders, in working days (FR-22.5).
	IssueMarginDays   int             `json:"issueMarginDays"`
	ReorderMarginDays int             `json:"reorderMarginDays"`
	PositiveDays      int             `json:"positiveDays"`
	NegativeDays      int             `json:"negativeDays"`
	ReorderMin        decimal.Decimal `json:"reorderMin"`
	ReorderMax        decimal.Decimal `json:"reorderMax"`
	// Time fences in days from asOf (BR-22.4): freezeFenceDays 0 freezes
	// nothing; actionFenceDays and futuresFenceDays 0 are unlimited.
	FreezeFenceDays  int `json:"freezeFenceDays"`
	ActionFenceDays  int `json:"actionFenceDays"`
	FuturesFenceDays int `json:"futuresFenceDays"`
	// CoverageFenceDays is the coverage time fence (FR-22.2): surplus
	// existing supply at or beyond it raises Cancel or Decrease; 0 raises neither.
	CoverageFenceDays int `json:"coverageFenceDays"`
	// FirmingFenceDays is the firming time fence (FR-22.2); with AutoFirm,
	// planned orders whose order date falls inside it are firmed by the run
	// (BR-22.4). AutoFirm needs a fence of at least 1 day.
	FirmingFenceDays int  `json:"firmingFenceDays"`
	AutoFirm         bool `json:"autoFirm"`
	// SafetyStockPolicy is today (default) or first-issue (BR-22.5): the day
	// the item's safety stock is a demand.
	SafetyStockPolicy string `json:"safetyStockPolicy"`
	// Strategy is the planning strategy code of the item (FR-22.19). Left out,
	// a run takes the strategy of the item's stored site-scope coverage, and
	// mts-net when the item has none; sent, it wins over the stored one.
	Strategy string `json:"strategy,omitempty"`
}

type lotDTO struct {
	MinOrderQty decimal.Decimal `json:"minOrderQty"`
	MultipleQty decimal.Decimal `json:"multipleQty"`
	MaxOrderQty decimal.Decimal `json:"maxOrderQty"`
	StandardQty decimal.Decimal `json:"standardQty"`
}

type requirementDTO struct {
	Ref      string          `json:"ref"`
	RefType  string          `json:"refType"`
	Date     string          `json:"date"`
	Qty      decimal.Decimal `json:"qty"`
	Priority *int            `json:"priority"`
}

type bomDTO struct {
	ItemID string          `json:"itemId"`
	Qty    decimal.Decimal `json:"qty"`
}

// validate checks the snapshot and returns field errors.
func (req runRequest) validate() map[string]string {
	fields := map[string]string{}
	if _, err := time.Parse(dateLayout, req.AsOf); err != nil {
		fields["asOf"] = "must be a date as YYYY-MM-DD"
	}
	if !ids.Valid(req.SiteID) {
		fields["siteId"] = "must be a UUID"
	}
	if req.ForecastModel != "" && !planCodePattern.MatchString(req.ForecastModel) {
		fields["forecastModel"] = codeMessage
	}
	if req.Calendar != nil {
		if msg := req.Calendar.validate(); msg != "" {
			fields["calendar"] = msg
		}
	}
	validateHorizon(fields, req.HorizonDays)
	req.Scope.validate(fields)
	if len(req.Items) == 0 || len(req.Items) > maxItems {
		fields["items"] = fmt.Sprintf("must hold 1 to %d items", maxItems)
	}
	for i, it := range req.Items {
		prefix := fmt.Sprintf("items[%d]", i)
		if !ids.Valid(it.ItemID) {
			fields[prefix+".itemId"] = "must be a UUID"
		}
		if it.VendorID != "" && !ids.Valid(it.VendorID) {
			fields[prefix+".vendorId"] = "must be a UUID"
		}
		if len(it.ItemGroup) > maxAttrLen || len(it.Planner) > maxAttrLen {
			fields[prefix+".itemGroup"] = "itemGroup and planner must be at most 64 characters"
		}
		if it.LeadTimeDays != nil && (*it.LeadTimeDays < 0 || *it.LeadTimeDays > 3650) {
			fields[prefix+".leadTimeDays"] = "must be 0 to 3650"
		}
		c := it.Coverage
		if c.PeriodDays < 0 || c.PositiveDays < 0 || c.NegativeDays < 0 {
			fields[prefix+".coverage"] = "periodDays, positiveDays and negativeDays must be 0 or more"
		}
		// Margins are stepped day by day over the working calendar, so they
		// are bounded like the item coverage's margins (FR-22.5).
		for name, days := range map[string]int{"marginDays": c.MarginDays, "issueMarginDays": c.IssueMarginDays, "reorderMarginDays": c.ReorderMarginDays} {
			if days < 0 || days > maxMarginDays {
				fields[prefix+".coverage."+name] = "must be 0 to 365"
			}
		}
		for name, days := range map[string]int{"freezeFenceDays": c.FreezeFenceDays, "actionFenceDays": c.ActionFenceDays,
			"futuresFenceDays": c.FuturesFenceDays, "coverageFenceDays": c.CoverageFenceDays, "firmingFenceDays": c.FirmingFenceDays} {
			if days < 0 || days > 3650 {
				fields[prefix+".coverage."+name] = "must be 0 to 3650"
			}
		}
		if c.SafetyStockPolicy != "" && c.SafetyStockPolicy != domain.SafetyToday && c.SafetyStockPolicy != domain.SafetyFirstIssue {
			fields[prefix+".coverage.safetyStockPolicy"] = "must be today or first-issue"
		}
		if c.Strategy != "" && !domain.ValidStrategy(c.Strategy) {
			fields[prefix+".coverage.strategy"] = "must be mts-net, mts-gross, mto, assemble-to-order-final, plan-without-final-assembly or subassembly"
		}
		if c.AutoFirm && c.FirmingFenceDays < 1 {
			fields[prefix+".coverage.autoFirm"] = "needs a firmingFenceDays of at least 1"
		}
		if c.ReorderMin.IsNegative() || c.ReorderMax.IsNegative() || it.OnHand.IsNegative() || it.SafetyStock.IsNegative() ||
			it.Lot.MinOrderQty.IsNegative() || it.Lot.MultipleQty.IsNegative() || it.Lot.MaxOrderQty.IsNegative() || it.Lot.StandardQty.IsNegative() {
			fields[prefix+".quantities"] = "on-hand, safety stock, min/max and lot quantities must be 0 or more"
		}
		validateRequirements(fields, prefix+".demand", it.Demand)
		validateRequirements(fields, prefix+".receipts", it.Receipts)
		for _, b := range it.Components {
			if !ids.Valid(b.ItemID) || !b.Qty.IsPositive() {
				fields[prefix+".components"] = "each component needs an itemId UUID and a positive qty"
			}
		}
	}
	return fields
}

func validateRequirements(fields map[string]string, prefix string, reqs []requirementDTO) {
	for _, r := range reqs {
		if !knownRefTypes[r.RefType] || r.Ref == "" || len(r.Ref) > 128 || !r.Qty.IsPositive() {
			fields[prefix] = "each line needs a ref (at most 128 characters), a known refType and a positive qty"
		}
		if _, err := time.Parse(dateLayout, r.Date); err != nil {
			fields[prefix+".date"] = "must be a date as YYYY-MM-DD"
		}
	}
}

// toDomain converts the request snapshot into engine items; day 0 is asOf.
func (req runRequest) toDomain(asOf time.Time) ([]domain.Item, error) {
	items := make([]domain.Item, 0, len(req.Items))
	for _, it := range req.Items {
		item := domain.Item{
			ID: it.ItemID, Coverage: it.Coverage.Code, PeriodDays: it.Coverage.PeriodDays, MarginDays: it.Coverage.MarginDays,
			IssueMarginDays: it.Coverage.IssueMarginDays, ReorderMarginDays: it.Coverage.ReorderMarginDays,
			PositiveDays: it.Coverage.PositiveDays, NegativeDays: it.Coverage.NegativeDays,
			ReorderMin: it.Coverage.ReorderMin, ReorderMax: it.Coverage.ReorderMax,
			FreezeDays: it.Coverage.FreezeFenceDays, ActionDays: it.Coverage.ActionFenceDays, FuturesDays: it.Coverage.FuturesFenceDays,
			CoverageDays: it.Coverage.CoverageFenceDays, FirmingDays: it.Coverage.FirmingFenceDays, AutoFirm: it.Coverage.AutoFirm,
			SafetyPolicy: it.Coverage.SafetyStockPolicy, OnHand: it.OnHand, SafetyStock: it.SafetyStock, OrderType: it.OrderType, LeadTimeDays: deref(it.LeadTimeDays),
			MinOrderQty: it.Lot.MinOrderQty, MultipleQty: it.Lot.MultipleQty, MaxOrderQty: it.Lot.MaxOrderQty, StandardQty: it.Lot.StandardQty,
		}
		var err error
		if item.Demand, err = toRequirements(it.Demand, asOf); err != nil {
			return nil, err
		}
		if item.Receipts, err = toRequirements(it.Receipts, asOf); err != nil {
			return nil, err
		}
		for _, c := range it.Components {
			item.Components = append(item.Components, domain.BOMLine{ItemID: c.ItemID, Qty: c.Qty})
		}
		items = append(items, item)
	}
	return items, nil
}

func toRequirements(in []requirementDTO, asOf time.Time) ([]domain.Requirement, error) {
	out := make([]domain.Requirement, 0, len(in))
	for _, r := range in {
		d, err := time.Parse(dateLayout, r.Date)
		if err != nil {
			return nil, err
		}
		prio, ok := defaultPriority[r.RefType]
		if !ok {
			prio = domain.PriorityDependent
		}
		if r.Priority != nil {
			prio = *r.Priority
		}
		day := int(d.Sub(asOf) / (24 * time.Hour))
		out = append(out, domain.Requirement{Ref: r.Ref, RefType: r.RefType, Date: day, Qty: r.Qty, Priority: prio})
	}
	return out, nil
}

// buildSnapshot turns a validated run request into the engine's items: stored
// site-scope item coverage fills what the request leaves out (FR-22.2,
// FR-22.5) and the forecast model's reduced cells are added as demand
// (FR-22.8). It also returns the stored DDMRP buffers of the items (FR-22.16),
// which the caller applies with domain.ApplyBuffers as the last change to the
// items (after a scenario's overrides) and with domain.AddBufferResults on the
// final result. On failure it has written the problem response and returns
// false. An operational run and a simulation (FR-22.15) take the same snapshot;
// for a simulation sc is the scenario, applied (domain.ApplyScenario) before
// the forecast is planned and with the model's cells scaled by the item's
// factor, so under assemble-to-order-final the scenario's added sales lines
// consume the scaled forecast like the scaled sales do (BR-22.13, BR-22.14).
func (a *API) buildSnapshot(w http.ResponseWriter, r *http.Request, code string, req runRequest, sc *domain.ScenarioSpec) (time.Time, []domain.Item, []domain.RunBuffer, bool) {
	planEntity, err := a.store.PlanLegalEntity(r.Context(), code, req.LegalEntityID)
	if err != nil {
		httpx.Fail(w, r, err)
		return time.Time{}, nil, nil, false
	}
	stored, err := a.store.RunCoverages(r.Context(), req.SiteID, planEntity, itemIDsOf(req.Items))
	if err != nil {
		httpx.Fail(w, r, err)
		return time.Time{}, nil, nil, false
	}
	if fields := withStoredCoverage(req.Items, stored); len(fields) > 0 {
		validationFailed(w, r, fields)
		return time.Time{}, nil, nil, false
	}
	asOf, _ := time.Parse(dateLayout, req.AsOf)
	items, err := req.toDomain(asOf)
	if err != nil {
		validationFailed(w, r, map[string]string{"items": err.Error()})
		return time.Time{}, nil, nil, false
	}
	var factor func(string) decimal.Decimal
	if sc != nil {
		if items, err = domain.ApplyScenario(items, asOf, *sc); err != nil {
			validationFailed(w, r, map[string]string{"scenario": strings.TrimPrefix(err.Error(), domain.ErrInvalidItem.Error()+": ")})
			return time.Time{}, nil, nil, false
		}
		factor = sc.Factors()
	}
	if req.ForecastModel != "" {
		lines, err := a.store.ForecastDemand(r.Context(), req.ForecastModel, req.SiteID, asOf)
		if err != nil {
			var p *httpx.Problem
			if errors.As(err, &p) && p.Code == store.CodeForecastNotFound {
				err = httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeForecastNotFound, "forecast model "+req.ForecastModel+" does not exist")
			}
			httpx.Fail(w, r, err)
			return time.Time{}, nil, nil, false
		}
		strategies, err := a.store.RunStrategies(r.Context(), req.SiteID, planEntity, itemIDsOf(req.Items))
		if err != nil {
			httpx.Fail(w, r, err)
			return time.Time{}, nil, nil, false
		}
		if strategies, err = a.inlineStrategies(r.Context(), req.Items, strategies); err != nil {
			httpx.Fail(w, r, err)
			return time.Time{}, nil, nil, false
		}
		if err := addForecastDemand(items, req.ForecastModel, asOf, lines, strategies, factor); err != nil {
			httpx.Fail(w, r, runProblem(err))
			return time.Time{}, nil, nil, false
		}
	}
	if req.CarryForward {
		firmed, err := a.store.FirmedSupply(r.Context(), code, req.SiteID, asOf)
		if err != nil {
			httpx.Fail(w, r, err)
			return time.Time{}, nil, nil, false
		}
		addFirmedSupply(items, asOf, firmed)
	}
	buffers, err := a.store.RunBuffers(r.Context(), req.SiteID, planEntity, itemIDsOf(req.Items))
	if err != nil {
		httpx.Fail(w, r, err)
		return time.Time{}, nil, nil, false
	}
	return asOf, items, buffers, true
}

func (a *API) runPlan(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code := chi.URLParam(r, "planCode")
	if !planCodePattern.MatchString(code) {
		validationFailed(w, r, map[string]string{"planCode": codeMessage})
		return
	}
	key := r.Header.Get(idempotencyKey)
	if key == "" {
		validationFailed(w, r, map[string]string{idempotencyKey: "header is required on plan runs"})
		return
	}
	var req runRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := req.validate(); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	// The idempotency fingerprint covers the plan code and the snapshot as
	// sent, so the same key on another plan or input is a reuse, not a replay,
	// and a retry still replays after the stored item coverage has changed.
	body, _ := json.Marshal(struct {
		PlanCode string     `json:"planCode"`
		Request  runRequest `json:"request"`
	}{code, req})
	asOf, items, buffers, ok := a.buildSnapshot(w, r, code, req, nil)
	if !ok {
		return
	}
	started := a.now()
	result, planned, err := a.plan(req, asOf, domain.ApplyBuffers(items, buffers))
	if err != nil {
		a.runsRefused.Add(1)
		httpx.Fail(w, r, runProblem(err))
		return
	}
	result, err = a.applyQuotaToRun(r.Context(), code, req, asOf, result)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	result = domain.ApplyDefaultVendors(result, defaultVendors(req.Items))
	result = domain.AddBufferResults(result, buffers)
	elapsed := a.now().Sub(started).Milliseconds()
	v, replayed, err := a.store.RunPlan(r.Context(), store.RunRequest{
		PlanCode: code, SiteID: req.SiteID, LegalEntityID: req.LegalEntityID, ForecastModel: req.ForecastModel, AsOf: asOf, ItemsPlanned: planned, DurationMs: elapsed,
		InputHash: fmt.Sprintf("%x", sha256.Sum256(body)), IdempotencyKey: key, Result: result,
	})
	if err != nil {
		var p *httpx.Problem
		if errors.As(err, &p) && p.Code == store.CodeAlreadyRunning {
			a.runsRefused.Add(1)
		}
		slog.Error("plan run failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, err)
		return
	}
	if replayed {
		a.runsReplayed.Add(1)
		w.Header().Set("Idempotent-Replayed", "true")
		httpx.JSON(w, http.StatusOK, v)
		return
	}
	a.runsCompleted.Add(1)
	httpx.JSON(w, http.StatusCreated, v)
}

// inlineStrategies lays the strategy sent on an item's coverage block over the
// strategies read from stored coverage, with the stored setup of that strategy
// (or its PRD defaults), so an item with no stored coverage can carry one.
func (a *API) inlineStrategies(ctx context.Context, items []itemDTO, strategies map[string]domain.StrategySpec) (map[string]domain.StrategySpec, error) {
	if strategies == nil {
		strategies = map[string]domain.StrategySpec{}
	}
	var setup map[string]domain.StrategySpec
	for _, it := range items {
		if it.Coverage.Strategy == "" {
			continue
		}
		if setup == nil {
			stored, err := a.store.ListStrategies(ctx)
			if err != nil {
				return nil, err
			}
			setup = make(map[string]domain.StrategySpec, len(stored))
			for _, st := range stored {
				setup[st.Spec.Code] = st.Spec
			}
		}
		spec, ok := setup[it.Coverage.Strategy]
		if !ok {
			spec = domain.DefaultStrategy(it.Coverage.Strategy)
		}
		strategies[it.ItemID] = spec
	}
	return strategies, nil
}

// addForecastDemand appends the forecast cells of the model to the demand of
// their items as forecast requirements at the period start (FR-22.8, BR-22.3).
// The planning strategy on the item's coverage (FR-22.19) decides the quantity:
// the reduced forecast (mts-net, the default), the forecast as entered
// (mts-gross), none (mto) or the forecast left after the item's sales lines
// consumed it (assemble-to-order-final, BR-22.13); see domain.PlanForecast.
// Cells of items outside the snapshot are not planned. A simulation passes the
// scenario's factor of each item (nil: 1), which scales a cell's forecast and
// reduced forecast before the strategy plans them.
func addForecastDemand(items []domain.Item, model string, asOf time.Time, lines []store.ForecastDemandLine, strategies map[string]domain.StrategySpec, factor func(string) decimal.Decimal) error {
	index := make(map[string]*domain.Item, len(items))
	for i := range items {
		index[items[i].ID] = &items[i]
	}
	var order []string
	cells := map[string][]domain.ForecastCellDemand{}
	for _, l := range lines {
		if index[l.ItemID] == nil {
			continue
		}
		if _, seen := cells[l.ItemID]; !seen {
			order = append(order, l.ItemID)
		}
		start := l.PeriodStart.Format(dateLayout)
		qty, reduced := l.Qty, l.ReducedQty
		if factor != nil {
			f := factor(l.ItemID)
			qty, reduced = qty.Mul(f), reduced.Mul(f)
		}
		cells[l.ItemID] = append(cells[l.ItemID], domain.ForecastCellDemand{
			Ref: "FC:" + model + ":" + start, StartDay: int(l.PeriodStart.Sub(asOf) / (24 * time.Hour)),
			EndDay: int(l.PeriodEnd.Sub(asOf) / (24 * time.Hour)), Qty: qty, ReducedQty: reduced,
		})
	}
	for _, id := range order {
		it := index[id]
		st, ok := strategies[id]
		if !ok {
			st = domain.DefaultStrategy(domain.StrategyMTSNet)
		}
		var sales []domain.Requirement
		for _, d := range it.Demand {
			if d.RefType == domain.RefSales {
				sales = append(sales, d)
			}
		}
		demand, onTop, err := domain.PlanForecastNotes(st, cells[id], sales)
		if err != nil {
			return err
		}
		it.Demand = append(it.Demand, demand...)
		it.SalesOnTop = append(it.SalesOnTop, onTop...)
	}
	return nil
}

// addFirmedSupply appends each firmed planned order that is still to be
// delivered to the receipts of its item as a planned-order receipt on its
// delivery day. Orders of items outside the snapshot are not planned.
func addFirmedSupply(items []domain.Item, asOf time.Time, firmed []store.FirmedSupply) {
	index := make(map[string]*domain.Item, len(items))
	for i := range items {
		index[items[i].ID] = &items[i]
	}
	for _, f := range firmed {
		it := index[f.ItemID]
		if it == nil {
			continue
		}
		day := int(f.DeliveryDate.Sub(asOf) / (24 * time.Hour))
		it.Receipts = append(it.Receipts, domain.Requirement{Ref: "FP:" + f.OrderID, RefType: domain.RefPlanned, Date: day, Qty: f.Qty})
	}
}

func runProblem(err error) error {
	switch {
	case errors.Is(err, domain.ErrBOMCycle):
		return httpx.NewProblem(http.StatusUnprocessableEntity, "PLAN-0004", "BOM cycle detected; low-level codes not updated")
	case errors.Is(err, domain.ErrTooManySplits):
		return httpx.NewProblem(http.StatusUnprocessableEntity, "PLAN-0005", "a requirement splits into more orders than the limit of 1000")
	case errors.Is(err, domain.ErrInvalidItem), errors.Is(err, domain.ErrInvalidCalendar), errors.Is(err, domain.ErrInvalidStrategy):
		return httpx.NewProblem(http.StatusUnprocessableEntity, codeInput, err.Error())
	}
	return err
}

func (a *API) listVersions(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code := chi.URLParam(r, "planCode")
	if !planCodePattern.MatchString(code) {
		validationFailed(w, r, map[string]string{"planCode": "must be 2 to 20 characters of A-Z, 0-9, _ or -"})
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	versions, err := a.store.ListVersions(r.Context(), code, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": versions})
}

func (a *API) getVersion(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	code := chi.URLParam(r, "planCode")
	number, err := strconv.Atoi(chi.URLParam(r, "number"))
	fields := map[string]string{}
	if err != nil || number < 1 {
		fields["number"] = "must be a positive integer"
	}
	if !planCodePattern.MatchString(code) {
		fields["planCode"] = "must be 2 to 20 characters of A-Z, 0-9, _ or -"
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	v, err := a.store.GetVersion(r.Context(), code, number)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, v)
}

func limitParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit := store.MaxPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > store.MaxPageSize {
			validationFailed(w, r, map[string]string{"limit": fmt.Sprintf("must be 1 to %d", store.MaxPageSize)})
			return 0, false
		}
		limit = n
	}
	return limit, true
}

func (a *API) listPlannedOrders(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	code := q.Get("planCode")
	f := store.OrderFilter{Status: q.Get("status"), ItemID: q.Get("itemId"), Sort: q.Get("sort")}
	fields := map[string]string{}
	if f.Sort != "" && f.Sort != store.SortPriority {
		fields["sort"] = "must be priority"
	}
	if !planCodePattern.MatchString(code) {
		fields["planCode"] = "is required"
	}
	if f.Status != "" && f.Status != "unprocessed" && f.Status != "approved" && f.Status != "firmed" {
		fields["status"] = "must be unprocessed, approved or firmed"
	}
	if f.ItemID != "" && !ids.Valid(f.ItemID) {
		fields["itemId"] = "must be a UUID"
	}
	if raw := q.Get("version"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			fields["version"] = "must be a positive version number"
		}
		f.Version = n
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	orders, err := a.store.ListPlannedOrders(r.Context(), code, f, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": orders})
}

func (a *API) getPlannedOrder(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeOrderNotFound, "planned order not found"))
		return
	}
	order, err := a.store.GetPlannedOrder(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etagOf(order.Version))
	httpx.JSON(w, http.StatusOK, order)
}

// pegNode is the JSON shape of one explain-tree node. Children is always an
// array (empty on a leaf) so clients can recurse without null checks.
type pegNode struct {
	Ref      string    `json:"ref"`
	RefType  string    `json:"refType"`
	Qty      string    `json:"qty"`
	Children []pegNode `json:"children"`
}

func toPegNode(n domain.TreeNode) pegNode {
	out := pegNode{Ref: n.Ref, RefType: n.RefType, Qty: n.Qty.String(), Children: []pegNode{}}
	for _, c := range n.Children {
		out.Children = append(out.Children, toPegNode(c))
	}
	return out
}

func (a *API) pegging(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeOrderNotFound, "planned order not found"))
		return
	}
	depth := defaultDepth
	if raw := r.URL.Query().Get("depth"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxPegDepth {
			validationFailed(w, r, map[string]string{"depth": fmt.Sprintf("must be 1 to %d", maxPegDepth)})
			return
		}
		depth = n
	}
	order, err := a.store.GetPlannedOrder(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	var lookupErr error
	pegsOf := func(ref string) []domain.PegLine {
		lines, err := a.store.PegsOf(r.Context(), order.VersionID, ref)
		if err != nil && lookupErr == nil {
			lookupErr = err
		}
		return lines
	}
	tree := domain.BuildTree(order.Number, depth, pegsOf)
	if lookupErr != nil {
		httpx.Fail(w, r, lookupErr)
		return
	}
	root := toPegNode(tree)
	root.Qty = order.Qty
	httpx.JSON(w, http.StatusOK, map[string]any{"orderId": order.ID, "number": order.Number, "qty": order.Qty, "tree": root})
}

func (a *API) listMessages(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	q := r.URL.Query()
	code := q.Get("planCode")
	action := q.Get("action")
	if !planCodePattern.MatchString(code) {
		validationFailed(w, r, map[string]string{"planCode": "is required"})
		return
	}
	switch action {
	case "", domain.ActionDelay, domain.ActionPostpone, domain.ActionAdvance, domain.ActionShortage, domain.ActionCancel, domain.ActionDecrease, domain.ActionIncrease, domain.ActionRedZone, domain.ActionSalesOnTop, domain.ActionNoVendor:
	default:
		validationFailed(w, r, map[string]string{"action": "must be delay, postpone, advance, shortage, cancel, decrease, increase, red-zone, sales-on-top or no-vendor"})
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	sort := q.Get("sort")
	if sort != "" && sort != store.SortPriority {
		validationFailed(w, r, map[string]string{"sort": "must be priority"})
		return
	}
	msgs, err := a.store.ListMessages(r.Context(), code, action, sort, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": msgs})
}
