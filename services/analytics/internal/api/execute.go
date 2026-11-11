package api

import (
	"context"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/cube"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
)

// Executor runs a Cube query for a tenant within a legal-entity scope. internal/cube
// implements it; tests use a fake.
type Executor interface {
	Load(ctx context.Context, tenantID string, scope domain.Scope, q domain.CubeQuery) ([]map[string]any, error)
}

var _ Executor = (*cube.Client)(nil)

// ExecuteResponse is the plan that ran and its rows. Each row maps a measure or dimension
// name (without its model) to a value: decimal strings for measures, null for none.
type ExecuteResponse struct {
	Plan     domain.Plan      `json:"plan"`
	Rows     []map[string]any `json:"rows"`
	RowCount int              `json:"rowCount"`
}

func semanticLayerDown(w http.ResponseWriter, r *http.Request, detail string) {
	httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, domain.CodeSemanticLayer, detail))
}

// execute serves POST /v1/analytics/query:execute (46-3). It plans the query exactly like
// POST /v1/analytics/query (published metrics only, the caller's scope as filters) and runs
// the plan on Cube. A caller without scope gets an empty result and Cube is not called.
func (a *API) execute(w http.ResponseWriter, r *http.Request) {
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
	scope := domain.ScopeFromPrincipal(p)
	plan, qerr := domain.BuildPlan(domain.Query{Measures: req.Measures, Dimensions: req.Dimensions, From: from, To: to}, a.catalog, scope)
	if qerr != nil {
		httpx.Fail(w, r, problemOf(qerr))
		return
	}
	if plan.Reason == domain.ReasonNoScope {
		httpx.JSON(w, http.StatusOK, ExecuteResponse{Plan: plan, Rows: []map[string]any{}})
		return
	}
	cq, qerr := plan.ToCubeQuery()
	if qerr != nil {
		httpx.Fail(w, r, problemOf(qerr))
		return
	}
	if a.exec == nil {
		semanticLayerDown(w, r, "the semantic layer is not configured")
		return
	}
	data, err := a.exec.Load(r.Context(), p.TenantID, scope, cq)
	if err != nil {
		semanticLayerDown(w, r, "the semantic layer did not answer")
		return
	}
	rows := make([]map[string]any, 0, len(data))
	for _, d := range data {
		row := map[string]any{}
		for _, m := range cq.Measures {
			row[domain.ShortMember(m)] = cube.Text(d[m])
		}
		for _, m := range cq.Dimensions {
			row[domain.ShortMember(m)] = d[m]
		}
		rows = append(rows, row)
	}
	httpx.JSON(w, http.StatusOK, ExecuteResponse{Plan: plan, Rows: rows, RowCount: len(rows)})
}

// DashboardValuesResponse is a dashboard with the current value of every tile. One tile
// that cannot be computed (no model, no scope, Cube down) does not fail the others.
type DashboardValuesResponse struct {
	DashboardID    string             `json:"dashboardId"`
	Title          string             `json:"title"`
	RefreshSeconds int                `json:"refreshSeconds"`
	ComputedAt     time.Time          `json:"computedAt"`
	Tiles          []domain.TileValue `json:"tiles"`
}

func (a *API) dashboardValues(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !domain.IsDashboardID(id) {
		fail(w, r, domain.NotFound("dashboard "+id))
		return
	}
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
		return
	}
	d, err := a.store.GetDashboard(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	now := a.now()
	httpx.JSON(w, http.StatusOK, DashboardValuesResponse{
		DashboardID: d.ID, Title: d.Title, RefreshSeconds: d.RefreshSeconds, ComputedAt: now.UTC(),
		Tiles: a.tileValues(r.Context(), p, d, now),
	})
}

// tileValues computes each tile of d for the caller: its metric over its period, in the
// caller's scope.
func (a *API) tileValues(ctx context.Context, p tenant.Principal, d domain.Dashboard, now time.Time) []domain.TileValue {
	scope := domain.ScopeFromPrincipal(p)
	out := make([]domain.TileValue, 0, len(d.Tiles))
	for _, t := range d.Tiles {
		tv := domain.TileValue{Title: t.Title, Metric: t.Metric, Period: t.Period, Status: domain.TileUnavail}
		m, ok := domain.CatalogMetric(a.catalog, t.Metric)
		if !ok {
			out = append(out, tv)
			continue
		}
		tv.MetricName = m.Name
		tv.Unit = "REPORTING_CURRENCY"
		if t.Metric == "gross_margin_pct" {
			tv.Unit = "RATIO"
		}
		from, to, err := domain.PeriodRange(t.Period, now)
		if err != nil {
			out = append(out, tv)
			continue
		}
		tv.From, tv.To = from, to
		if !domain.CubeServes(t.Metric) {
			tv.Status = domain.TileNotModeled
			out = append(out, tv)
			continue
		}
		fromT, _ := time.Parse(time.DateOnly, from)
		toT, _ := time.Parse(time.DateOnly, to)
		plan, qerr := domain.BuildPlan(domain.Query{Measures: []string{t.Metric}, From: fromT, To: toT}, a.catalog, scope)
		switch {
		case qerr != nil:
		case plan.Reason == domain.ReasonNoScope:
			tv.Status = domain.TileNoScope
		case a.exec == nil:
		default:
			cq, qerr := plan.ToCubeQuery()
			if qerr != nil {
				break
			}
			rows, err := a.exec.Load(ctx, p.TenantID, scope, cq)
			if err != nil || len(rows) > 1 { // a tile is one number
				break
			}
			tv.Status = domain.TileOK
			if len(rows) == 1 {
				tv.Value = cube.Text(rows[0][cq.Measures[0]])
			}
		}
		out = append(out, tv)
	}
	return out
}
