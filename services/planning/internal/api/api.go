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

	idempotencyKey = "Idempotency-Key"
	dateLayout     = "2006-01-02"
	maxItems       = 100000
	maxClosedDates = 1000
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
	a.registerBuffers(r)
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
	// ForecastModel, when set, adds the model's reduced forecast at the site
	// as forecast demand of the snapshot's items (FR-22.8).
	ForecastModel string `json:"forecastModel,omitempty"`
	// Calendar is the site work calendar of the run (FR-22.5, BR-22.8); without
	// it lead times and margins count calendar days.
	Calendar *calendarDTO `json:"calendar,omitempty"`
	Items    []itemDTO    `json:"items"`
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
	ItemID      string          `json:"itemId"`
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
	Code         string          `json:"code"`
	PeriodDays   int             `json:"periodDays"`
	MarginDays   int             `json:"marginDays"`
	PositiveDays int             `json:"positiveDays"`
	NegativeDays int             `json:"negativeDays"`
	ReorderMin   decimal.Decimal `json:"reorderMin"`
	ReorderMax   decimal.Decimal `json:"reorderMax"`
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
	if len(req.Items) == 0 || len(req.Items) > maxItems {
		fields["items"] = fmt.Sprintf("must hold 1 to %d items", maxItems)
	}
	for i, it := range req.Items {
		prefix := fmt.Sprintf("items[%d]", i)
		if !ids.Valid(it.ItemID) {
			fields[prefix+".itemId"] = "must be a UUID"
		}
		if it.LeadTimeDays != nil && (*it.LeadTimeDays < 0 || *it.LeadTimeDays > 3650) {
			fields[prefix+".leadTimeDays"] = "must be 0 to 3650"
		}
		c := it.Coverage
		if c.PeriodDays < 0 || c.MarginDays < 0 || c.PositiveDays < 0 || c.NegativeDays < 0 {
			fields[prefix+".coverage"] = "periodDays, marginDays, positiveDays and negativeDays must be 0 or more"
		}
		for name, days := range map[string]int{"freezeFenceDays": c.FreezeFenceDays, "actionFenceDays": c.ActionFenceDays,
			"futuresFenceDays": c.FuturesFenceDays, "coverageFenceDays": c.CoverageFenceDays, "firmingFenceDays": c.FirmingFenceDays} {
			if days < 0 || days > 3650 {
				fields[prefix+".coverage."+name] = "must be 0 to 3650"
			}
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
			PositiveDays: it.Coverage.PositiveDays, NegativeDays: it.Coverage.NegativeDays,
			ReorderMin: it.Coverage.ReorderMin, ReorderMax: it.Coverage.ReorderMax,
			FreezeDays: it.Coverage.FreezeFenceDays, ActionDays: it.Coverage.ActionFenceDays, FuturesDays: it.Coverage.FuturesFenceDays,
			CoverageDays: it.Coverage.CoverageFenceDays, FirmingDays: it.Coverage.FirmingFenceDays, AutoFirm: it.Coverage.AutoFirm,
			OnHand: it.OnHand, SafetyStock: it.SafetyStock, OrderType: it.OrderType, LeadTimeDays: deref(it.LeadTimeDays),
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
	stored, err := a.store.RunCoverages(r.Context(), req.SiteID, itemIDsOf(req.Items))
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := withStoredCoverage(req.Items, stored); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	asOf, _ := time.Parse(dateLayout, req.AsOf)
	items, err := req.toDomain(asOf)
	if err != nil {
		validationFailed(w, r, map[string]string{"items": err.Error()})
		return
	}
	if req.ForecastModel != "" {
		lines, err := a.store.ForecastDemand(r.Context(), req.ForecastModel, req.SiteID, asOf)
		if err != nil {
			var p *httpx.Problem
			if errors.As(err, &p) && p.Code == store.CodeForecastNotFound {
				err = httpx.NewProblem(http.StatusUnprocessableEntity, store.CodeForecastNotFound, "forecast model "+req.ForecastModel+" does not exist")
			}
			httpx.Fail(w, r, err)
			return
		}
		addForecastDemand(items, req.ForecastModel, asOf, lines)
	}
	started := a.now()
	result, err := domain.RunWithCalendar(0, req.Calendar.toDomain(asOf), items)
	if err != nil {
		a.runsRefused.Add(1)
		httpx.Fail(w, r, runProblem(err))
		return
	}
	elapsed := a.now().Sub(started).Milliseconds()
	v, replayed, err := a.store.RunPlan(r.Context(), store.RunRequest{
		PlanCode: code, SiteID: req.SiteID, AsOf: asOf, ItemsPlanned: len(items), DurationMs: elapsed,
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

// addForecastDemand appends each reduced forecast cell to the demand of its
// item as a forecast requirement at the period start (FR-22.8, BR-22.3). Cells
// of items outside the snapshot are not planned. A period that started before
// asOf is due at asOf, like any past demand.
func addForecastDemand(items []domain.Item, model string, asOf time.Time, lines []store.ForecastDemandLine) {
	index := make(map[string]*domain.Item, len(items))
	for i := range items {
		index[items[i].ID] = &items[i]
	}
	for _, l := range lines {
		it := index[l.ItemID]
		if it == nil {
			continue
		}
		start := l.PeriodStart.Format(dateLayout)
		day := int(l.PeriodStart.Sub(asOf) / (24 * time.Hour))
		it.Demand = append(it.Demand, domain.Requirement{Ref: "FC:" + model + ":" + start, RefType: domain.RefForecast,
			Date: day, Qty: l.ReducedQty, Priority: domain.PriorityForecast})
	}
}

func runProblem(err error) error {
	switch {
	case errors.Is(err, domain.ErrBOMCycle):
		return httpx.NewProblem(http.StatusUnprocessableEntity, "PLAN-0004", "BOM cycle detected; low-level codes not updated")
	case errors.Is(err, domain.ErrTooManySplits):
		return httpx.NewProblem(http.StatusUnprocessableEntity, "PLAN-0005", "a requirement splits into more orders than the limit of 1000")
	case errors.Is(err, domain.ErrInvalidItem), errors.Is(err, domain.ErrInvalidCalendar):
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
	f := store.OrderFilter{Status: q.Get("status"), ItemID: q.Get("itemId")}
	fields := map[string]string{}
	if !planCodePattern.MatchString(code) {
		fields["planCode"] = "is required"
	}
	if f.Status != "" && f.Status != "unprocessed" && f.Status != "approved" && f.Status != "firmed" {
		fields["status"] = "must be unprocessed, approved or firmed"
	}
	if f.ItemID != "" && !ids.Valid(f.ItemID) {
		fields["itemId"] = "must be a UUID"
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
	case "", domain.ActionDelay, domain.ActionPostpone, domain.ActionAdvance, domain.ActionShortage, domain.ActionCancel, domain.ActionDecrease, domain.ActionIncrease:
	default:
		validationFailed(w, r, map[string]string{"action": "must be delay, postpone, advance, shortage, cancel, decrease or increase"})
		return
	}
	limit, ok := limitParam(w, r)
	if !ok {
		return
	}
	msgs, err := a.store.ListMessages(r.Context(), code, action, limit)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": msgs})
}
