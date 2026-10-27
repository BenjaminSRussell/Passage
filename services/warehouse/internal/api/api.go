// Package api exposes the warehouse HTTP surface: location-directive
// simulation (stateless), and with a store the location and directive setup
// and work execution routes (execution.go). Handlers decode, validate, call
// the domain or store and answer with httpx.JSON or httpx.Problem.
package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/warehouse/internal/domain"
)

// dutySetupView is the PRD duty WMS_SETUP_VIEW (§6.31.17), named the way every
// other service names its duties.
const (
	dutySetupView = "warehouse.setup.view"
	maxDirectives = 1000
	maxLocations  = 50000
)

// simulatePath is the PRD route /wms/v1/location-directives:simulate under the
// /v1/<service>/ prefix every Passage service uses; the web BFF maps
// /api/v1/warehouse/... onto it.
const simulatePath = "/v1/warehouse/location-directives:simulate"

// API holds the handlers and their counters. st is nil in stateless mode (no
// DATABASE_URL): only the simulation route is mounted then.
type API struct {
	simulations atomic.Int64
	blocked     atomic.Int64
	workCreated atomic.Int64
	workBlocked atomic.Int64
	assignments atomic.Int64
	st          Store
	lockTimeout time.Duration
}

// New returns a stateless API with zeroed counters.
func New() *API { return &API{} }

// NewWithStore returns an API that persists locations, directives and work in
// st. lockTimeout is the idle time after which ASSIGNED work returns to OPEN
// (WMS_LOCK_TIMEOUT_MIN, BR-31.3).
func NewWithStore(st Store, lockTimeout time.Duration) *API {
	return &API{st: st, lockTimeout: lockTimeout}
}

// Register mounts the routes on r.
func (a *API) Register(r chi.Router) {
	r.With(authz.RequireDuty(dutySetupView)).Post(simulatePath, a.simulate)
	if a.st != nil {
		a.registerPersisted(r)
	}
}

// Metrics serves the admin-port counters in Prometheus text format.
func (a *API) Metrics() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		fmt.Fprintf(w, "# HELP warehouse_directive_simulations_total Directive simulations evaluated.\n# TYPE warehouse_directive_simulations_total counter\nwarehouse_directive_simulations_total %d\n", a.simulations.Load())
		fmt.Fprintf(w, "# HELP warehouse_directive_blocked_total Simulated lines that resolved to no location (WMS-0101).\n# TYPE warehouse_directive_blocked_total counter\nwarehouse_directive_blocked_total %d\n", a.blocked.Load())
		fmt.Fprintf(w, "# HELP warehouse_work_created_total Works created.\n# TYPE warehouse_work_created_total counter\nwarehouse_work_created_total %d\n", a.workCreated.Load())
		fmt.Fprintf(w, "# HELP warehouse_work_blocked_total Works created BLOCKED_NO_LOCATION (WMS-0101).\n# TYPE warehouse_work_blocked_total counter\nwarehouse_work_blocked_total %d\n", a.workBlocked.Load())
		fmt.Fprintf(w, "# HELP warehouse_work_assignments_total Works assigned to a user.\n# TYPE warehouse_work_assignments_total counter\nwarehouse_work_assignments_total %d\n", a.assignments.Load())
	})
}

// simulateRequest is a hypothetical line with the directives and candidate
// locations to evaluate it against. It never reads or writes the database, so
// a directive can be tried before it is saved.
type simulateRequest struct {
	Line       domain.Line        `json:"line"`
	Directives []domain.Directive `json:"directives"`
	Locations  []domain.Location  `json:"locations"`
	Compliance *complianceRequest `json:"compliance,omitempty"`
}

// complianceRequest is the optional hazmat reference data (FR-31.19): profiles
// by item, the segregation matrix (missing pairs are allowed) and section limits.
type complianceRequest struct {
	HazmatProfiles  []domain.HazmatProfile     `json:"hazmatProfiles"`
	Segregation     []domain.SegregationPair   `json:"segregation"`
	SectionLimitsKg map[string]decimal.Decimal `json:"sectionLimitsKg"`
}

// simulateResponse is the outcome of Evaluate with the chosen location named.
type simulateResponse struct {
	Blocked    bool          `json:"blocked"`
	Code       string        `json:"code,omitempty"`
	LocationID string        `json:"locationId,omitempty"`
	Location   string        `json:"location,omitempty"`
	Directive  string        `json:"directive,omitempty"`
	Trace      []domain.Step `json:"trace"`
}

func (a *API) simulate(w http.ResponseWriter, r *http.Request) {
	var req simulateRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if fields := validateSimulate(req); len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	comp, err := buildCompliance(req.Compliance)
	if err != nil {
		validationFailed(w, r, map[string]string{"compliance": err.Error()})
		return
	}
	res := domain.EvaluateWithCompliance(req.Directives, req.Line, req.Locations, comp)
	a.simulations.Add(1)
	out := simulateResponse{Blocked: res.Blocked, Code: res.Code, Directive: res.Directive, Trace: res.Trace}
	if out.Trace == nil {
		out.Trace = []domain.Step{}
	}
	if res.Blocked {
		a.blocked.Add(1)
	} else {
		out.LocationID = res.Location.ID
		out.Location = res.Location.Name
	}
	slog.Info("directive simulated", "blocked", res.Blocked, "correlationId", middleware.GetReqID(r.Context()))
	httpx.JSON(w, http.StatusOK, out)
}

// buildCompliance turns the optional request block into domain reference data.
// An absent block yields the zero Compliance, which disables hazmat checks.
func buildCompliance(c *complianceRequest) (domain.Compliance, error) {
	if c == nil {
		return domain.Compliance{}, nil
	}
	return domain.NewCompliance(c.HazmatProfiles, c.Segregation, c.SectionLimitsKg)
}

func validationFailed(w http.ResponseWriter, r *http.Request, fields map[string]string) {
	p := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
	p.Errors = fields
	httpx.Fail(w, r, p)
}
