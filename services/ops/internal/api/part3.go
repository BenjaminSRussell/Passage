package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
)

// Part 3 duties (operator tenant only, like every ops duty).
const (
	// DutyProvision provisions a tenant and reports its provisioning steps (the provisioning runner).
	DutyProvision = "ops.tenant.provision"
	// DutyCostRecord records a tenant's metered month (the monthly metering job).
	DutyCostRecord = "ops.cost.record"
	// DutyLicenseReport reports a tenant's active user counts (the hourly user-count reporter).
	DutyLicenseReport = "ops.license.report"
)

// SLOSource reads the per-service request, failure and p95 latency figures of a window.
type SLOSource interface {
	Samples(ctx context.Context, window time.Duration) ([]domain.SLOSample, error)
}

func (a *API) registerPart3(g chi.Router) {
	g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/slo", a.sloReport)
	g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/tenants", a.listProvisions)
	g.With(authz.RequireDuty(DutyProvision)).Post("/v1/ops/tenants", a.provision)
	g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/tenants/{id}", a.getProvision)
	g.With(authz.RequireDuty(DutyProvision)).Put("/v1/ops/tenants/{id}/provision-steps/{action}", a.provisionStep)
	g.With(authz.RequireDuty(DutyCostRecord)).Put("/v1/ops/tenants/{id}/costs/{month}", a.recordCost)
	g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/tenants/{id}/costs/{month}", a.getCost)
	g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/costs", a.listCosts)
	g.With(authz.RequireDuty(DutyLicenseReport)).Put("/v1/ops/tenants/{id}/license/usage", a.reportUsage)
	g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/licenses", a.listLicenses)
}

// ---- SLO ----

func (a *API) sloTargets() domain.SLOTargets {
	t := a.cfg.SLOTargets
	if t.Availability <= 0 {
		t.Availability = domain.DefaultAvailabilityTarget
	}
	if t.LatencyP95 <= 0 {
		t.LatencyP95 = domain.DefaultLatencyP95
	}
	return t
}

// buildSLO reads the window from the source and evaluates it.
func (a *API) buildSLO(ctx context.Context, window string) (domain.SLOReport, error) {
	d, ok := domain.SLOWindows[window]
	if !ok {
		return domain.SLOReport{}, &domain.Error{Code: domain.CodeSLOInvalid, Detail: "window must be one of 1h, 6h, 24h, 7d, 30d"}
	}
	if a.cfg.SLOSource == nil {
		return domain.SLOReport{}, &domain.Error{Code: domain.CodeSLOUnavailable, Detail: "the SLO report reads Prometheus: set PROMETHEUS_URL"}
	}
	samples, err := a.cfg.SLOSource.Samples(ctx, d)
	if err != nil {
		slog.Error("slo source", "err", err)
		return domain.SLOReport{}, &domain.Error{Code: domain.CodeSLOUnavailable, Detail: "Prometheus did not answer the SLO queries; try again"}
	}
	return domain.EvaluateSLO(window, samples, a.sloTargets(), a.now()), nil
}

// sloReport is GET /v1/ops/slo: availability and p95 latency per service over the window (default 24h), against the targets,
// with the time each breached service turned breached (from the hourly check). It reads; it records nothing.
func (a *API) sloReport(w http.ResponseWriter, r *http.Request) {
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "24h"
	}
	rep, err := a.buildSLO(r.Context(), window)
	if err != nil {
		httpx.Fail(w, r, store.Problem(err))
		return
	}
	since, err := a.store.SLOStates(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	for i := range rep.Services {
		if at, ok := since[rep.Services[i].Service]; ok && rep.Services[i].Breached {
			v := at.UTC().Format(time.RFC3339)
			rep.Services[i].BreachedSince = &v
		}
	}
	httpx.JSON(w, http.StatusOK, rep)
}

// ---- Tenant provisioning ----

type provisionBody struct {
	TenantID  string `json:"tenantId"`
	Name      string `json:"name"`
	Residency string `json:"residency"`
	Region    string `json:"region"`
	Tier      string `json:"tier"`
}

// provision records a tenant provision after the residency check (OPS-1705). The provisioning runner then executes the steps
// and reports each with PUT .../provision-steps/{action}.
func (a *API) provision(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	key := r.Header.Get(idempotencyKey)
	if key == "" || len(key) > 200 {
		httpx.Fail(w, r, validation(idempotencyKey, "an Idempotency-Key header is required"))
		return
	}
	var body provisionBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	id, ok := ids.Canonical(body.TenantID)
	if !ok || id == ids.Nil {
		httpx.Fail(w, r, validation("tenantId", "must be the new tenant's id (a UUID)"))
		return
	}
	regions, err := a.store.Regions(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	req, err := domain.ValidateProvision(domain.ProvisionRequest{TenantID: id, Name: body.Name, Residency: body.Residency, Region: body.Region, Tier: body.Tier}, regions)
	if err != nil {
		httpx.Fail(w, r, store.Problem(err))
		return
	}
	rec, replay, err := a.store.CreateProvision(r.Context(), key, req, p.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	slog.Info("tenant provision recorded", "tenant", rec.TenantID, "region", rec.Region, "residency", rec.Residency, "requestedBy", p.UserID, "replay", replay)
	httpx.JSON(w, status, rec)
}

func (a *API) listProvisions(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.Provisions(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": list})
}

func tenantParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	id, ok := ids.Canonical(chi.URLParam(r, "id"))
	if !ok {
		httpx.Fail(w, r, validation("id", "must be a tenant id"))
	}
	return id, ok
}

func (a *API) getProvision(w http.ResponseWriter, r *http.Request) {
	id, ok := tenantParam(w, r)
	if !ok {
		return
	}
	rec, err := a.store.Provision(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rec)
}

type provisionStepBody struct {
	Status string `json:"status"`
	Detail string `json:"detail"`
}

func (a *API) provisionStep(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	id, ok := tenantParam(w, r)
	if !ok {
		return
	}
	action := chi.URLParam(r, "action")
	if !domain.ValidStep(action) {
		httpx.Fail(w, r, validation("action", "must be one of "+strings.Join(domain.ProvisionSteps, ", ")))
		return
	}
	var body provisionStepBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if body.Status != "DONE" && body.Status != "FAILED" {
		httpx.Fail(w, r, validation("status", "must be DONE or FAILED"))
		return
	}
	if len(body.Detail) > 2000 {
		httpx.Fail(w, r, validation("detail", "at most 2000 characters"))
		return
	}
	rec, err := a.store.RecordProvisionStep(r.Context(), id, action, body.Status, body.Detail, p.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rec)
}

// ---- Cost statements ----

type costBody struct {
	Quantities   map[string]string `json:"quantities"`
	USDPerEUR    string            `json:"usdPerEur"`
	ModelVersion string            `json:"modelVersion"`
}

// recordCost computes and stores a tenant's month from its metered quantities (the monthly metering job sends them); a second
// call restates the month.
func (a *API) recordCost(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	id, ok := tenantParam(w, r)
	if !ok {
		return
	}
	if a.cfg.CostRates == nil {
		httpx.Fail(w, r, store.Problem(&domain.Error{Code: domain.CodeCostUnavailable, Detail: "no unit rates are configured: set OPS_COST_RATES"}))
		return
	}
	var body costBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	qty, err := domain.ParseQuantities(body.Quantities)
	if err != nil {
		httpx.Fail(w, r, store.Problem(err))
		return
	}
	st, err := domain.ComputeCost(id, chi.URLParam(r, "month"), qty, *a.cfg.CostRates, body.USDPerEUR, body.ModelVersion)
	if err != nil {
		httpx.Fail(w, r, store.Problem(err))
		return
	}
	rec, err := a.store.RecordCost(r.Context(), st, p.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, rec)
}

// getCost reads a statement as JSON, or as CSV with ?format=csv (or Accept: text/csv).
func (a *API) getCost(w http.ResponseWriter, r *http.Request) {
	id, ok := tenantParam(w, r)
	if !ok {
		return
	}
	month := chi.URLParam(r, "month")
	if !domain.ValidMonth(month) {
		httpx.Fail(w, r, validation("month", "must be YYYY-MM"))
		return
	}
	rec, err := a.store.Cost(r.Context(), id, month)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if r.URL.Query().Get("format") == "csv" || strings.Contains(r.Header.Get("Accept"), "text/csv") {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="cost-%s-%s.csv"`, id, month))
		_, _ = w.Write([]byte(rec.CSV()))
		return
	}
	httpx.JSON(w, http.StatusOK, rec)
}

func (a *API) listCosts(w http.ResponseWriter, r *http.Request) {
	month := r.URL.Query().Get("month")
	if !domain.ValidMonth(month) {
		httpx.Fail(w, r, validation("month", "must be YYYY-MM"))
		return
	}
	list, total, err := a.store.Costs(r.Context(), month)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"month": month, "items": list, "clusterCostUsd": total})
}

// ---- Licence user counts ----

type usageBody struct {
	ActiveFull        *int `json:"activeFull"`
	ActiveOperational *int `json:"activeOperational"`
	ActiveSelfService *int `json:"activeSelfservice"`
}

func (a *API) reportUsage(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	id, ok := tenantParam(w, r)
	if !ok {
		return
	}
	var body usageBody
	if err := httpx.Decode(r, &body); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	for field, v := range map[string]*int{"activeFull": body.ActiveFull, "activeOperational": body.ActiveOperational, "activeSelfservice": body.ActiveSelfService} {
		if v == nil || *v < 0 || *v > 1<<24 {
			httpx.Fail(w, r, validation(field, "must be a non-negative number of users"))
			return
		}
	}
	u, err := a.store.ReportLicenseUsage(r.Context(), id, store.LicenseUsage{ActiveFull: *body.ActiveFull, ActiveOperational: *body.ActiveOperational, ActiveSelfService: *body.ActiveSelfService}, p.UserID)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, u)
}

type licenseListItem struct {
	licenseView
	Usage store.LicenseUsage `json:"usage"`
}

// listLicenses lists every installed licence with its state today, evaluated with the tenant's last reported full users.
func (a *API) listLicenses(w http.ResponseWriter, r *http.Request) {
	list, err := a.store.Licenses(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	items := make([]licenseListItem, 0, len(list))
	for _, rec := range list {
		u, err := a.store.LicenseUsage(r.Context(), rec.License.TenantID)
		if err != nil {
			httpx.Fail(w, r, err)
			return
		}
		items = append(items, licenseListItem{licenseView: a.licenseView(rec, u.ActiveFull), Usage: u})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// ---- The hourly checks ----

// RunChecks runs the licence check and, when an SLO source is configured, the SLO check once now and then every interval until
// ctx ends. Each replica runs it; both checks are idempotent (a state change is written once, under a row lock).
func (a *API) RunChecks(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		a.CheckOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// CheckOnce is one round of the hourly checks.
func (a *API) CheckOnce(ctx context.Context) {
	if n, err := a.store.CheckLicenses(ctx, a.now()); err != nil {
		slog.Error("licence check", "err", err)
	} else if n > 0 {
		slog.Warn("licence states changed", "count", n)
	}
	if a.cfg.SLOSource == nil {
		return
	}
	rep, err := a.buildSLO(ctx, "1h")
	if err != nil {
		slog.Error("slo check", "err", err)
		return
	}
	n, err := a.store.RecordSLO(ctx, &rep)
	if err != nil {
		slog.Error("slo check", "err", err)
		return
	}
	a.sloBreaches.Add(int64(n))
	if n > 0 {
		slog.Warn("services breached their SLO", "count", n, "window", rep.Window)
	}
}

// ---- Prometheus ----

// PromSource reads the shared HTTP series from the Prometheus HTTP API (/api/v1/query), one figure per scrape job.
type PromSource struct {
	URL    string
	Client *http.Client
}

func promRange(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		return strconv.Itoa(int(d/(24*time.Hour))) + "d"
	}
	return strconv.Itoa(int(d/time.Hour)) + "h"
}

// Samples runs the three queries of the window.
func (p PromSource) Samples(ctx context.Context, window time.Duration) ([]domain.SLOSample, error) {
	w := promRange(window)
	requests, err := p.query(ctx, `sum by (job) (increase(http_requests_total[`+w+`]))`)
	if err != nil {
		return nil, err
	}
	failed, err := p.query(ctx, `sum by (job) (increase(http_requests_total{status=~"5.."}[`+w+`]))`)
	if err != nil {
		return nil, err
	}
	p95, err := p.query(ctx, `histogram_quantile(0.95, sum by (job, le) (rate(http_request_duration_seconds_bucket[`+w+`])))`)
	if err != nil {
		return nil, err
	}
	out := make([]domain.SLOSample, 0, len(requests))
	for job, n := range requests {
		s := domain.SLOSample{Service: job, Requests: n, Failed: failed[job]}
		if v, ok := p95[job]; ok {
			v := v
			s.P95 = &v
		}
		out = append(out, s)
	}
	return out, nil
}

type promResponse struct {
	Status string `json:"status"`
	Data   struct {
		Result []struct {
			Metric map[string]string `json:"metric"`
			Value  [2]any            `json:"value"`
		} `json:"result"`
	} `json:"data"`
	Error string `json:"error"`
}

func (p PromSource) query(ctx context.Context, q string) (map[string]float64, error) {
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.URL, "/")+"/api/v1/query?query="+url.QueryEscape(q), nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	var body promResponse
	if err := json.NewDecoder(http.MaxBytesReader(nil, resp.Body, 8<<20)).Decode(&body); err != nil {
		return nil, fmt.Errorf("prometheus answered %d: %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK || body.Status != "success" {
		return nil, fmt.Errorf("prometheus answered %d: %s", resp.StatusCode, body.Error)
	}
	out := map[string]float64{}
	for _, r := range body.Data.Result {
		job := r.Metric["job"]
		raw, _ := r.Value[1].(string)
		v, err := strconv.ParseFloat(raw, 64)
		if job == "" || err != nil || v != v { // NaN: no observation in the window
			continue
		}
		out[job] = v
	}
	return out, nil
}
