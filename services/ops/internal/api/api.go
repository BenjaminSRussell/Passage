// Package api is the ops control plane's HTTP surface (PSG-50, part 1): the region topology, the guarded failover,
// the capacity estimate and the region health check DNS reads.
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
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
	maxLagS      = 1 << 30
)

// Config is the process's settings.
type Config struct {
	// Region is the region this replica runs in (REGION_NAME); /healthz/region answers for it.
	Region string
	// MaxLagSeconds is FAILOVER_MAX_LAG_S.
	MaxLagSeconds int
}

// API is the ops HTTP surface.
type API struct {
	store     store.Store
	cfg       Config
	failovers atomic.Int64
	rejected  atomic.Int64
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
		g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/regions", a.regions)
		g.With(authz.RequireDuty(DutyView)).Get("/v1/ops/capacity/estimate", a.capacity)
		g.With(authz.RequireDuty(DutyReport)).Put("/v1/ops/regions/{name}/lag", a.reportLag)
		g.With(authz.RequireDuty(DutyFailover)).Post("/v1/ops/failover", a.failover)
		g.With(authz.RequireDuty(DutyFailover)).Post("/v1/ops/regions/{name}:rejoin", a.rejoin)
	})
}

// Metrics renders Prometheus text.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		tenant.WriteReplayMetrics(w)
		_, _ = fmt.Fprintf(w, "# HELP ops_failovers_total Failovers applied.\n# TYPE ops_failovers_total counter\nops_failovers_total %d\n", a.failovers.Load())
		_, _ = fmt.Fprintf(w, "# HELP ops_failovers_rejected_total Failover requests refused by a rule.\n# TYPE ops_failovers_rejected_total counter\nops_failovers_rejected_total %d\n", a.rejected.Load())
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

// regionHealth is what DNS health checks read: 200 only while this region is PRIMARY, 503 with Retry-After otherwise
// (FENCED and PASSIVE regions serve no writes, BR-50.2). A store error is 503 too, so DNS never routes to a region
// that cannot say what it is.
func (a *API) regionHealth(w http.ResponseWriter, r *http.Request) {
	regions, err := a.store.Regions(r.Context())
	role := domain.Role("UNKNOWN")
	if err == nil {
		for _, reg := range regions {
			if reg.Name == a.cfg.Region {
				role = reg.Role
			}
		}
	}
	status := http.StatusOK
	if role != domain.Primary {
		status = http.StatusServiceUnavailable
		w.Header().Set("Retry-After", "30")
	}
	httpx.JSON(w, status, map[string]string{"region": a.cfg.Region, "role": string(role)})
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

type failoverRequest struct {
	To             string `json:"to"`
	AcceptDataLoss bool   `json:"acceptDataLoss"`
	ApprovedBy     string `json:"approvedBy"`
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
	rec, replay, err := a.store.Failover(r.Context(), eventID, domain.FailoverRequest{
		To: req.To, AcceptDataLoss: req.AcceptDataLoss, Caller: p.UserID, Approver: req.ApprovedBy, MaxLagSeconds: a.cfg.MaxLagSeconds,
	})
	if err != nil {
		a.rejected.Add(1)
		httpx.Fail(w, r, err)
		return
	}
	status := http.StatusOK
	if !replay {
		a.failovers.Add(1)
		status = http.StatusCreated
		slog.Warn("failover applied", "event", eventID, "from", rec.Plan.From, "to", rec.Plan.To, "acceptDataLoss", rec.Plan.AcceptDataLoss,
			"requestedBy", rec.RequestedBy, "approvedBy", rec.ApprovedBy, "correlationId", middleware.GetReqID(r.Context()))
	}
	httpx.JSON(w, status, rec)
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
