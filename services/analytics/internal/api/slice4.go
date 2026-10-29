package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/store"
)

// Duties for the slice-4 screens (§9.46.17).
const (
	DutyExport      = "AnalyticsExport"
	DutyCatalogView = "CatalogView"
)

// registerSlice4 mounts dashboards, explorer exports, schedule deliveries and
// catalog lineage.
func (a *API) registerSlice4(r chi.Router) {
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireDuty(DutyView))
		g.Get("/v1/analytics/dashboards", a.listDashboards)
		g.Get("/v1/analytics/dashboards/{id}", a.getDashboard)
	})
	r.With(authz.RequireDuty(DutyAuthor)).Put("/v1/analytics/dashboards/{id}", a.putDashboard)
	r.With(authz.RequireDuty(DutyAuthor)).Get("/v1/analytics/schedules/{id}/deliveries", a.listDeliveries)
	r.Group(func(g chi.Router) {
		g.Use(authz.RequireDuty(DutyExport))
		g.Get("/v1/analytics/exports", a.listExports)
		g.Post("/v1/analytics/exports", a.createExport)
	})
	r.With(authz.RequireDuty(DutyCatalogView)).Get("/v1/catalog/lineage/{asset_fqn}", a.getLineage)
	r.With(authz.RequireDuty(DutyCatalogSteer)).Post("/v1/catalog/lineage/edges", a.addLineageEdges)
}

// --- dashboards (FR-46.8) ----------------------------------------------------

// DashboardsResponse lists dashboards.
type DashboardsResponse struct {
	Items []domain.Dashboard `json:"items"`
}

// DashboardRequest is the body of PUT /analytics/dashboards/{id}.
// refreshSeconds defaults to 60 when omitted.
type DashboardRequest struct {
	Title          string        `json:"title"`
	Tiles          []domain.Tile `json:"tiles"`
	RefreshSeconds int           `json:"refreshSeconds,omitempty"`
}

func (a *API) listDashboards(w http.ResponseWriter, r *http.Request) {
	items, err := a.store.ListDashboards(r.Context())
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, DashboardsResponse{Items: items})
}

func (a *API) getDashboard(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !domain.IsDashboardID(id) {
		fail(w, r, domain.NotFound("dashboard "+id))
		return
	}
	d, err := a.store.GetDashboard(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(d.Version))
	httpx.JSON(w, http.StatusOK, d)
}

// putDashboard creates (201, no If-Match) or replaces (200, If-Match with the
// current version) a dashboard. Tiles are checked against the published
// metric catalog before anything is stored.
func (a *API) putDashboard(w http.ResponseWriter, r *http.Request) {
	var req DashboardRequest
	if err := httpx.Decode(r, &req); err != nil {
		refuse(w, r, "request body is not valid JSON", nil)
		return
	}
	version, ok := ifMatch(r)
	if !ok {
		refuse(w, r, `If-Match must be the dashboard version as an entity tag, for example "3"`, map[string]string{"If-Match": "entity tag such as \"3\""})
		return
	}
	in := domain.Dashboard{ID: chi.URLParam(r, "id"), Title: req.Title, Tiles: req.Tiles, RefreshSeconds: req.RefreshSeconds}
	if _, verr := domain.PrepareDashboard(in, a.catalog); verr != nil {
		fail(w, r, verr)
		return
	}
	saved, created, err := a.store.PutDashboard(r.Context(), in, a.catalog, version)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(saved.Version))
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	httpx.JSON(w, code, saved)
}

// --- schedule deliveries (FR-46.10) ------------------------------------------

// DeliveriesResponse lists delivery attempts, newest first.
type DeliveriesResponse struct {
	Items []domain.Delivery `json:"items"`
}

func (a *API) listDeliveries(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status != "" && !domain.IsDeliveryStatus(status) {
		refuse(w, r, "request failed validation", map[string]string{"status": "DELIVERED, RETRY or FAILED"})
		return
	}
	items, err := a.store.ListDeliveries(r.Context(), chi.URLParam(r, "id"), status)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, DeliveriesResponse{Items: items})
}

// --- explorer exports (FR-46.9) ----------------------------------------------

// ExportRequest is the body of POST /analytics/exports. rowCount is the
// explorer's count of the rows the query returns; it picks inline or Temporal
// mode and is checked against the 1M-row cap (ANA-0101). linkTtlHours
// defaults to 24.
type ExportRequest struct {
	Measures     []string `json:"measures"`
	Dimensions   []string `json:"dimensions"`
	From         string   `json:"from"`
	To           string   `json:"to"`
	Format       string   `json:"format"`
	RowCount     int64    `json:"rowCount"`
	LinkTTLHours int      `json:"linkTtlHours,omitempty"`
}

// ExportsResponse is the export log, newest first.
type ExportsResponse struct {
	Items []domain.Export `json:"items"`
}

// createExport plans the query exactly as an interactive query is planned, with
// the caller's scope filters, so an export never reads more than the caller
// may see. The job is stored under Idempotency-Key; 202 means queued, 200 an
// identical export from the last hour (BR-46.7).
func (a *API) createExport(w http.ResponseWriter, r *http.Request) {
	key, raw, ok := idempotentBody(w, r)
	if !ok {
		return
	}
	var req ExportRequest
	if err := httpx.Decode(r, &req); err != nil {
		refuse(w, r, "request body is not valid JSON", nil)
		return
	}
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnauthorized, "AUTH-0001", "no principal"))
		return
	}
	from, to, ok := parseRange(req.From, req.To)
	if !ok {
		refuse(w, r, "from and to must be YYYY-MM-DD", map[string]string{"from": "YYYY-MM-DD", "to": "YYYY-MM-DD"})
		return
	}
	plan, qerr := domain.BuildPlan(domain.Query{Measures: req.Measures, Dimensions: req.Dimensions, From: from, To: to},
		a.catalog, domain.ScopeFromPrincipal(p.LegalEntityID))
	if qerr != nil {
		fail(w, r, qerr)
		return
	}
	if plan.Reason == domain.ReasonNoScope {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, domain.CodeNoScope, "you have no data scope for this model (NO_SCOPE)"))
		return
	}
	fields := map[string]string{}
	if err := domain.ValidateExportFormat(req.Format); err != nil {
		fields["format"] = "XLSX or CSV"
	}
	if req.LinkTTLHours == 0 {
		req.LinkTTLHours = domain.DefaultLinkTTLHours
	}
	if err := domain.ValidateLinkTTL(req.LinkTTLHours); err != nil {
		fields["linkTtlHours"] = "1 to 168 hours"
	}
	if req.RowCount < 0 {
		fields["rowCount"] = "zero or more rows"
	}
	if len(fields) > 0 {
		refuse(w, r, "request failed validation", fields)
		return
	}
	// The cap is the tenant default (analytics.export.max_rows); a caller cannot raise it.
	mode, cerr := domain.ChooseExport(req.RowCount, 0)
	if cerr != nil {
		fail(w, r, cerr)
		return
	}
	in := domain.Export{
		Plan: plan, Format: req.Format, Mode: mode, RowCount: req.RowCount, LinkTTLHours: req.LinkTTLHours,
		QueryHash: domain.QueryHash(domain.ExportCanonical(p.UserID, req.Format, plan)),
	}
	out, err := a.store.CreateExport(r.Context(), key, requestHash(r, raw), in, a.now())
	if err != nil {
		fail(w, r, err)
		return
	}
	writeCreated(w, out)
}

// listExports is the export log inquiry, filtered by user, format and
// creation day (§9.46.14).
func (a *API) listExports(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ExportFilter{RequestedBy: q.Get("user"), Format: q.Get("format"), From: q.Get("from"), To: q.Get("to")}
	fields := map[string]string{}
	if f.Format != "" && !domain.IsExportFormat(f.Format) {
		fields["format"] = "XLSX or CSV"
	}
	for name, v := range map[string]string{"from": f.From, "to": f.To} {
		if _, err := time.Parse(time.DateOnly, v); v != "" && err != nil {
			fields[name] = "YYYY-MM-DD"
		}
	}
	if len(f.RequestedBy) > 255 {
		fields["user"] = "at most 255 characters"
	}
	if len(fields) > 0 {
		refuse(w, r, "request failed validation", fields)
		return
	}
	items, err := a.store.ListExports(r.Context(), f)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ExportsResponse{Items: items})
}

// --- catalog lineage (FR-46.12) ----------------------------------------------

// LineageEdgesRequest is a batch of lineage edges to record.
type LineageEdgesRequest struct {
	Edges []domain.Edge `json:"edges"`
}

// LineageEdgesResponse says how many edges in the batch were new.
type LineageEdgesResponse struct {
	Added int64 `json:"added"`
}

// getLineage walks the stored edges both ways. ?depth=n limits the hops in each
// direction; omitted or 0 means domain.MaxLineageDepth.
func (a *API) getLineage(w http.ResponseWriter, r *http.Request) {
	asset := chi.URLParam(r, "asset_fqn")
	fields := map[string]string{}
	if !domain.IsAssetFQN(asset) {
		fields["asset_fqn"] = "dotted lowercase asset name such as mart.fin_journal_line"
	}
	depth := domain.MaxLineageDepth
	if raw := r.URL.Query().Get("depth"); raw != "" {
		n, err := strconv.Atoi(raw)
		d, ok := domain.LineageDepth(n)
		if err != nil || !ok {
			fields["depth"] = "0 to 20 hops"
		}
		depth = d
	}
	if len(fields) > 0 {
		refuse(w, r, "request failed validation", fields)
		return
	}
	out, err := a.store.GetLineage(r.Context(), asset, depth)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}

// addLineageEdges records a batch of edges. Re-sending a batch adds nothing and
// answers added=0, so the call is idempotent without a key.
func (a *API) addLineageEdges(w http.ResponseWriter, r *http.Request) {
	var req LineageEdgesRequest
	if err := httpx.Decode(r, &req); err != nil {
		refuse(w, r, "request body is not valid JSON", nil)
		return
	}
	if verr := domain.ValidateEdges(req.Edges); verr != nil {
		fail(w, r, verr)
		return
	}
	added, err := a.store.AddLineageEdges(r.Context(), req.Edges)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, LineageEdgesResponse{Added: added})
}
