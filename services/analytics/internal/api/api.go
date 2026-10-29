// Package api exposes the analytics HTTP surface: the governed metric catalog,
// the Cube query planner (with the OPA scope mirror applied), the daily
// reconciliation results, data-quality rules and scores, archive partitions
// and report schedules. Handlers decode, validate, read the tenant principal,
// call the domain or store, and answer with httpx.
package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/store"
)

// Duties from §9.46.17.
const (
	DutyView    = "AnalyticsView"
	DutyFinance = "FinanceView"
)

// CodeBadRequest is raised by the HTTP layer for malformed input. Domain codes
// (ANA-0103 and the rest) pass through unchanged.
const CodeBadRequest = domain.CodeValidation

// API serves analytics routes. A nil store is valid for routes that do not
// touch the database (catalog and query planning).
type API struct {
	store   *store.Postgres
	catalog []domain.Metric
	now     func() time.Time
}

// New returns the API over the store with the platform metric catalog.
func New(st *store.Postgres) *API {
	return &API{store: st, catalog: domain.GovernedMetrics(), now: time.Now}
}

// Register mounts the analytics routes on r.
func (a *API) Register(r *chi.Mux) {
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireDuty(DutyView))
		g.Get("/v1/analytics/metrics", a.listMetrics)
		g.Post("/v1/analytics/query", a.query)
	})
	r.With(authz.RequireDuty(DutyFinance)).Get("/v1/analytics/reconciliation", a.reconciliation)
	a.registerSlice2(r)
	a.registerSlice4(r)
}

// MetricsResponse lists published metrics.
type MetricsResponse struct {
	Items []domain.Metric `json:"items"`
}

func (a *API) listMetrics(w http.ResponseWriter, r *http.Request) {
	items := []domain.Metric{}
	for _, m := range a.catalog {
		if m.Status == domain.StatusPublished {
			items = append(items, m)
		}
	}
	httpx.JSON(w, http.StatusOK, MetricsResponse{Items: items})
}

// QueryRequest is the body of POST /v1/analytics/query. Dates are YYYY-MM-DD.
type QueryRequest struct {
	Measures   []string `json:"measures"`
	Dimensions []string `json:"dimensions"`
	From       string   `json:"from"`
	To         string   `json:"to"`
}

// QueryResponse carries the compiled Cube plan. Reason NO_SCOPE in the plan
// means the caller has no legal-entity scope and the result is empty (BR-46.9).
type QueryResponse struct {
	Plan domain.Plan `json:"plan"`
}

func (a *API) query(w http.ResponseWriter, r *http.Request) {
	var req QueryRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeBadRequest, "request body is not valid JSON"))
		return
	}
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
		return
	}
	from, to, ok := parseRange(req.From, req.To)
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeBadRequest, "from and to must be YYYY-MM-DD"))
		return
	}
	plan, qerr := domain.BuildPlan(domain.Query{
		Measures: req.Measures, Dimensions: req.Dimensions, From: from, To: to,
	}, a.catalog, domain.ScopeFromPrincipal(p.LegalEntityID))
	if qerr != nil {
		httpx.Fail(w, r, problemOf(qerr))
		return
	}
	httpx.JSON(w, http.StatusOK, QueryResponse{Plan: plan})
}

// ReconciliationResponse is one day of reconciliation results. FailedCount
// drives the dashboard banner (FR-46.13).
type ReconciliationResponse struct {
	Date        string                  `json:"date"`
	FailedCount int                     `json:"failedCount"`
	Items       []domain.Reconciliation `json:"items"`
}

func (a *API) reconciliation(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeBadRequest, "date must be YYYY-MM-DD"))
		return
	}
	metric := r.URL.Query().Get("metric")
	if metric != "" && !domain.IsMetricID(metric) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, CodeBadRequest, "metric must be a lower snake case metric id"))
		return
	}
	items, err := a.store.ListReconciliations(r.Context(), date, metric)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	failed := 0
	for _, it := range items {
		if it.Status != domain.ReconOK {
			failed++
		}
	}
	httpx.JSON(w, http.StatusOK, ReconciliationResponse{Date: date, FailedCount: failed, Items: items})
}

// problemOf maps a domain error to a problem, keeping its field errors.
func problemOf(e *domain.Error) *httpx.Problem {
	p := httpx.NewProblem(e.Status, e.Code, e.Detail)
	if len(e.Fields) > 0 {
		p.Errors = e.Fields
	}
	return p
}

func parseRange(from, to string) (time.Time, time.Time, bool) {
	f, err := time.Parse(time.DateOnly, from)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	t, err := time.Parse(time.DateOnly, to)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	return f, t, true
}
