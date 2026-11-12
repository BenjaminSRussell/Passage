// Package api is the ops control plane's HTTP surface (PSG-50, part 1): the region topology, the guarded failover,
// the capacity estimate and the region health check DNS reads.
package api

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"net/http"
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
	"github.com/BenjaminSRussell/passage/services/ops/internal/domain"
	"github.com/BenjaminSRussell/passage/services/ops/internal/store"
)

const (
	readyTimeout   = 500 * time.Millisecond
	idempotencyKey = "Idempotency-Key"
	// DutyView reads the topology and the capacity model (PlatformView). DutyFailover starts a failover or rejoins a
	// fenced region (PlatformAdmin). DutyReport records a region's replication lag (the monitoring agent).
	DutyView     = "ops.region.view"
	DutyFailover = "ops.failover.execute"
	DutyReport   = "ops.region.report"
	// DutyBreakGlassRequest opens a break-glass window for the caller; DutyBreakGlassReview reviews one after it ends.
	DutyBreakGlassRequest = "ops.breakglass.request"
	// DutyLicenseInstall installs a signed tenant licence.
	DutyLicenseInstall   = "ops.license.install"
	DutyBreakGlassReview = "ops.breakglass.review"
	maxLagS              = 1 << 30
)

// Config is the process's settings.
type Config struct {
	// Region is the region this replica runs in (REGION_NAME); /healthz/region answers for it.
	Region string
	// MaxLagSeconds is FAILOVER_MAX_LAG_S.
	MaxLagSeconds int
	// OperatorTenantID is the platform operator's tenant (OPS_OPERATOR_TENANT_ID). The ops routes act on platform-wide
	// state, so they serve only principals of this tenant, whatever duties another tenant's role holds. Empty serves no one.
	OperatorTenantID string
	// LicensePublicKey verifies licence signatures (OPS_LICENSE_PUBLIC_KEY, base64 Ed25519); empty refuses every install (LIC-1804).
	LicensePublicKey ed25519.PublicKey
	// RTOTargetMinutes and RPOTargetSeconds are the game-day targets (RTO_TARGET_MIN 60, RPO_TARGET_S 300).
	RTOTargetMinutes int
	RPOTargetSeconds int
	// Now is the clock for licence evaluation; nil means time.Now.
	Now func() time.Time
	// SLOSource reads the SLO figures (Prometheus, PROMETHEUS_URL); nil answers GET /v1/ops/slo with 503 OPS-1725.
	SLOSource SLOSource
	// SLOTargets are the objectives (SLO_AVAILABILITY_TARGET, SLO_LATENCY_P95_MS, SLO_LATENCY_P95_BY).
	SLOTargets domain.SLOTargets
	// CostRates are the unit rates of the cost statements (OPS_COST_RATES); nil refuses to record a statement (COST-1904).
	CostRates *domain.CostRates
}

// API is the ops HTTP surface.
type API struct {
	store     store.Store
	cfg       Config
	failovers atomic.Int64
	rejected  atomic.Int64

	breakGlassOpened atomic.Int64
	breakGlassUses   atomic.Int64
	sloBreaches      atomic.Int64
}

// New builds the API.
func New(s store.Store, cfg Config) *API {
	if cfg.MaxLagSeconds <= 0 {
		cfg.MaxLagSeconds = 300
	}
	return &API{store: s, cfg: cfg}
}

// Register mounts routes on r, which must come from httpx.NewRouterWithoutTenant: the region check is public and
// the rest sits behind the tenant middleware.
func (a *API) Register(r chi.Router) {
	r.Get("/readyz", a.ready)
	r.Get("/healthz/region", a.regionHealth)
	r.Group(func(g chi.Router) {
		g.Use(tenant.Middleware)
		g.Use(a.operatorOnly)
		g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/regions", a.regions)
		g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/capacity/estimate", a.capacity)
		g.With(authz.RequireDuty(DutyReport)).Put("/v1/ops/regions/{name}/lag", a.reportLag)
		g.With(a.requireFailover).Post("/v1/ops/failover", a.failover)
		g.With(a.requireFailover).Post("/v1/ops/regions/{name}:rejoin", a.rejoin)
		g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/failover-requests/{id}", a.request)
		g.With(a.requireFailover).Post("/v1/ops/failover-requests/{id}:approve", a.approve)
		g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/failovers/{eventId}", a.failoverDetail)
		g.With(a.requireFailover).Put("/v1/ops/failovers/{eventId}/steps/{action}", a.recordStep)
		g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/failovers/{eventId}/scorecard", a.scorecard)
		g.With(authz.RequireDuty(DutyLicenseInstall)).Put("/v1/ops/tenants/{id}/license", a.installLicense)
		g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/tenants/{id}/license", a.getLicense)
		g.With(authz.RequireDuty(DutyBreakGlassRequest)).Post("/v1/ops/break-glass", a.openBreakGlass)
		g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/break-glass", a.listBreakGlass)
		g.With(authz.RequireDuty(DutyBreakGlassRequest)).Post("/v1/ops/break-glass/{id}:revoke", a.revokeBreakGlass)
		g.With(authz.RequireDuty(DutyBreakGlassReview)).Post("/v1/ops/break-glass/{id}:review", a.reviewBreakGlass)
		a.registerPart3(g)
	})
}

// operatorOnly refuses every caller outside the operator tenant (OPS-1715): a tenant role granted an ops.* duty still
// cannot reach the platform's regions.
func (a *API) operatorOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := tenant.FromContext(r.Context())
		if !ok || a.cfg.OperatorTenantID == "" || !strings.EqualFold(p.TenantID, a.cfg.OperatorTenantID) {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, domain.CodeOperatorOnly, "the ops control plane serves the platform operator tenant only"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Metrics renders Prometheus text.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		tenant.WriteReplayMetrics(w)
		_, _ = fmt.Fprintf(w, "# HELP ops_failovers_total Failovers applied.\n# TYPE ops_failovers_total counter\nops_failovers_total %d\n", a.failovers.Load())
		a.writeGauges(w, r)
		_, _ = fmt.Fprintf(w, "# HELP ops_failovers_rejected_total Failover requests refused by a rule.\n# TYPE ops_failovers_rejected_total counter\nops_failovers_rejected_total %d\n", a.rejected.Load())
		_, _ = fmt.Fprintf(w, "# HELP ops_slo_breaches_total Services the hourly SLO check found newly breached.\n# TYPE ops_slo_breaches_total counter\nops_slo_breaches_total %d\n", a.sloBreaches.Load())
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

// regionHealth is what DNS health checks and every service's fence read: 200 with {"region","role":"PRIMARY"} while this region is
// PRIMARY, 503 with the role for PASSIVE and FENCED regions (they serve no writes, BR-50.2). When this service cannot say what
// the region is (its database is unreadable, or REGION_NAME names no registered region) it answers a 503 problem with no role,
// never a made-up one: DNS still sees a failing region, and the fence of the services reads it as "cannot tell" and keeps its
// last state instead of unfencing a region whose database was just fenced.
func (a *API) regionHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Retry-After", "30")
	regions, err := a.store.Regions(r.Context())
	if err != nil {
		slog.Error("region health: cannot read the regions", "err", err, "correlationId", middleware.GetReqID(r.Context()))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "this region's role cannot be read"))
		return
	}
	for _, reg := range regions {
		if reg.Name != a.cfg.Region {
			continue
		}
		status := http.StatusOK
		if reg.Role != domain.Primary {
			status = http.StatusServiceUnavailable
		} else {
			w.Header().Del("Retry-After")
		}
		httpx.JSON(w, status, map[string]string{"region": a.cfg.Region, "role": string(reg.Role)})
		return
	}
	httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, "COM-0503", "region "+a.cfg.Region+" is not registered"))
}

func (a *API) regions(w http.ResponseWriter, r *http.Request) {
	regions, err := a.store.Regions(r.Context())
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if regions == nil {
		regions = []domain.Region{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": regions, "maxLagSeconds": a.cfg.MaxLagSeconds})
}

type lagRequest struct {
	LagSeconds *int `json:"lagSeconds"`
}

func (a *API) reportLag(w http.ResponseWriter, r *http.Request) {
	var req lagRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.LagSeconds == nil || *req.LagSeconds < 0 || *req.LagSeconds > maxLagS {
		httpx.Fail(w, r, validation("lagSeconds", "must be a non-negative number of seconds"))
		return
	}
	region, err := a.store.ReportLag(r.Context(), chi.URLParam(r, "name"), *req.LagSeconds)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, region)
}

// failoverRequest has no approver: a failover that accepts data loss becomes a request, and a second operator approves it
// with their own verified call (POST /v1/ops/failover-requests/{id}:approve). A body naming approvedBy is refused.
type failoverRequest struct {
	To             string `json:"to"`
	AcceptDataLoss bool   `json:"acceptDataLoss"`
}

func (a *API) failover(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	eventID := r.Header.Get(idempotencyKey)
	if eventID == "" || len(eventID) > 128 {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "Idempotency-Key (the failover event id, up to 128 characters) is required"))
		return
	}
	var req failoverRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if req.To == "" {
		httpx.Fail(w, r, validation("to", "must name the region to promote"))
		return
	}
	if p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "failover needs a named user"))
		return
	}
	out, err := a.store.Failover(r.Context(), eventID, domain.FailoverRequest{
		To: req.To, AcceptDataLoss: req.AcceptDataLoss, Caller: p.UserID, MaxLagSeconds: a.cfg.MaxLagSeconds,
	})
	if err != nil {
		a.rejected.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	if out.Request != nil {
		if !out.Replay {
			slog.Warn("failover approval requested", "event", eventID, "request", out.Request.ID, "to", out.Request.To,
				"lagSeconds", out.Request.LagSeconds, "requestedBy", out.Request.RequestedBy, "correlationId", middleware.GetReqID(r.Context()))
		}
		httpx.JSON(w, http.StatusAccepted, map[string]any{"request": out.Request})
		return
	}
	a.applied(w, r, *out.Failover, out.Replay)
}

func (a *API) applied(w http.ResponseWriter, r *http.Request, rec store.Failover, replay bool) {
	status := http.StatusOK
	if !replay {
		a.failovers.Add(1)
		status = http.StatusCreated
		slog.Warn("failover applied", "event", rec.EventID, "from", rec.Plan.From, "to", rec.Plan.To, "acceptDataLoss", rec.Plan.AcceptDataLoss,
			"requestedBy", rec.RequestedBy, "approvedBy", rec.ApprovedBy, "correlationId", middleware.GetReqID(r.Context()))
	}
	httpx.JSON(w, status, rec)
}

func (a *API) request(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeRequestUnknown, "failover request not found"))
		return
	}
	req, err := a.store.Request(r.Context(), id)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, req)
}

// approve applies a pending failover request. The approver is the verified caller, never a value from the body, and the
// requester cannot approve their own request.
func (a *API) approve(w http.ResponseWriter, r *http.Request) {
	p, _ := tenant.FromContext(r.Context())
	if p.UserID == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "approving a failover needs a named user"))
		return
	}
	id := chi.URLParam(r, "id")
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, domain.CodeRequestUnknown, "failover request not found"))
		return
	}
	rec, replay, err := a.store.Approve(r.Context(), id, p.UserID, a.cfg.MaxLagSeconds)
	if err != nil {
		a.rejected.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	a.applied(w, r, rec, replay)
}

type rejoinRequest struct {
	ResyncVerified bool `json:"resyncVerified"`
}

func (a *API) rejoin(w http.ResponseWriter, r *http.Request) {
	var req rejoinRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	region, err := a.store.Rejoin(r.Context(), chi.URLParam(r, "name"), req.ResyncVerified)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, region)
}

// capacity serves the model of docs/ops/capacity-model.md. Query parameters override the reference inputs; the three
// rate parameters price the matching parts.
func (a *API) capacity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	in := domain.DefaultCapacityInputs()
	ints := map[string]*int{
		"users": &in.Users, "thinkTimeS": &in.ThinkTimeS, "journalLinesPerHour": &in.JournalLinesPerHour,
		"journalTargetLinesPerS": &in.JournalTargetLinesPerS, "orderLinesPerHour": &in.OrderLinesPerHour, "scanners": &in.Scanners,
		"scanIntervalS": &in.ScanIntervalS, "ledgerGbPerYear": &in.LedgerGBPerYear, "ordersGbPerYear": &in.OrdersGBPerYear,
		"scansGbPerYear": &in.ScansGBPerYear, "years": &in.Years, "peakKafkaMsgsPerS": &in.PeakKafkaMsgsPerS,
		"consumerParallelism": &in.ConsumerParallelism,
	}
	for name, dst := range ints {
		if v := q.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 || n > 1<<31-1 {
				httpx.Fail(w, r, validation(name, "must be a positive integer"))
				return
			}
			*dst = n
		}
	}
	est, err := domain.EstimateCapacity(in, domain.Rates{NodeUSDMonth: q.Get("nodeUsdMonth"), PgGiBUSDMonth: q.Get("pgGibUsdMonth"), BrokerUSDMonth: q.Get("brokerUsdMonth")})
	if err != nil {
		httpx.Fail(w, r, validation("rates", err.Error()))
		return
	}
	httpx.JSON(w, http.StatusOK, est)
}

func validation(field, msg string) *httpx.Problem {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
	p.Errors = map[string]string{field: msg}
	return p
}
