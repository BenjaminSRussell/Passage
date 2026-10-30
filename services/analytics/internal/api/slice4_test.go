package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/store"
	"github.com/BenjaminSRussell/passage/services/analytics/migrations"
)

var (
	exporter    = map[string]string{tenant.HeaderDuties: DutyExport, tenant.HeaderLegalEntity: usmf}
	catalogView = map[string]string{tenant.HeaderDuties: DutyCatalogView}
)

const dashboardBody = `{"title":"Controller KPIs","tiles":[{"title":"Revenue","metric":"revenue","period":"MTD"},{"title":"DSO","metric":"dso_days","period":"QTD"}]}`

const exportBody = `{"measures":["revenue"],"dimensions":["account_code"],"from":"2026-01-01","to":"2026-01-31","format":"XLSX","rowCount":250000}`

// Each slice-4 route requires its duty (§9.46.17).
func TestSlice4_DutiesEnforced(t *testing.T) {
	h := newTestServer()
	cases := []struct {
		method, path string
		headers      map[string]string
	}{
		{http.MethodGet, "/v1/analytics/dashboards", nil},
		{http.MethodGet, "/v1/analytics/dashboards/controller_kpis", nil},
		{http.MethodPut, "/v1/analytics/dashboards/controller_kpis", viewer},
		{http.MethodGet, "/v1/analytics/schedules/0193d6a0-0000-7000-8000-000000000001/deliveries", viewer},
		{http.MethodGet, "/v1/analytics/exports", viewer},
		{http.MethodPost, "/v1/analytics/exports", author},
		{http.MethodGet, "/v1/catalog/lineage/mart.fin_journal_line", steward},
		{http.MethodPost, "/v1/catalog/lineage/edges", catalogView},
	}
	for _, c := range cases {
		if rec := do(t, h, c.method, c.path, "{}", c.headers); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s with %v: status %d, want 403", c.method, c.path, c.headers, rec.Code)
		}
	}
}

// Validation runs before the store: these requests never reach the (nil) database.
func TestSlice4_ValidationBeforeStore(t *testing.T) {
	h := newTestServer()
	cases := []struct {
		name         string
		method, path string
		body         string
		headers      map[string]string
		status       int
		code, field  string
	}{
		{"dashboard unpublished metric", http.MethodPut, "/v1/analytics/dashboards/ops",
			strings.Replace(dashboardBody, `"dso_days"`, `"scratch"`, 1), author, 400, domain.CodeValidation, "tiles[1].metric"},
		{"dashboard bad id", http.MethodPut, "/v1/analytics/dashboards/Ops", dashboardBody, author, 400, domain.CodeValidation, "dashboardId"},
		{"dashboard unknown field", http.MethodPut, "/v1/analytics/dashboards/ops", `{"title":"x","owner":"me"}`, author, 400, domain.CodeValidation, ""},
		{"dashboard refresh too fast", http.MethodPut, "/v1/analytics/dashboards/ops",
			strings.Replace(dashboardBody, `"tiles"`, `"refreshSeconds":5,"tiles"`, 1), author, 400, domain.CodeValidation, "refreshSeconds"},
		{"dashboard weak etag", http.MethodPut, "/v1/analytics/dashboards/ops", dashboardBody, with(author, "If-Match", `W/"1"`), 400, domain.CodeValidation, "If-Match"},
		{"dashboard malformed id on get", http.MethodGet, "/v1/analytics/dashboards/Not%20An%20Id", "", viewer, 404, domain.CodeNotFound, ""},
		{"deliveries bad status", http.MethodGet, "/v1/analytics/schedules/0193d6a0-0000-7000-8000-000000000001/deliveries?status=LOST", "", author, 400, domain.CodeValidation, "status"},
		{"export without key", http.MethodPost, "/v1/analytics/exports", exportBody, exporter, 400, domain.CodeValidation, "Idempotency-Key"},
		{"export bad format and ttl", http.MethodPost, "/v1/analytics/exports",
			strings.Replace(exportBody, `"XLSX"`, `"PDF","linkTtlHours":200`, 1), with(exporter, headerIdempotency, "e1"), 400, domain.CodeValidation, "linkTtlHours"},
		{"export without an entity has no scope", http.MethodPost, "/v1/analytics/exports", exportBody,
			with(exporter, tenant.HeaderLegalEntity, "", headerIdempotency, "e0"), 403, domain.CodeNoScope, ""},
		{"export over the cap", http.MethodPost, "/v1/analytics/exports",
			strings.Replace(exportBody, "250000", "1000001", 1), with(exporter, headerIdempotency, "e1"), 422, domain.CodeExportTooLarge, ""},
		{"export cap cannot be raised", http.MethodPost, "/v1/analytics/exports",
			strings.Replace(exportBody, `"rowCount":250000`, `"rowCount":2000000,"maxRows":5000000`, 1), with(exporter, headerIdempotency, "e1"), 400, domain.CodeValidation, ""},
		{"export disallowed dimension", http.MethodPost, "/v1/analytics/exports",
			strings.Replace(exportBody, "account_code", "site_id", 1), with(exporter, headerIdempotency, "e1"), 400, domain.CodeValidation, ""},
		{"export log bad day", http.MethodGet, "/v1/analytics/exports?from=yesterday", "", exporter, 400, domain.CodeValidation, "from"},
		{"lineage bad asset", http.MethodGet, "/v1/catalog/lineage/Mart.Revenue", "", catalogView, 400, domain.CodeValidation, "asset_fqn"},
		{"lineage depth too large", http.MethodGet, "/v1/catalog/lineage/mart.fin_journal_line?depth=99", "", catalogView, 400, domain.CodeValidation, "depth"},
		{"lineage depth not a number", http.MethodGet, "/v1/catalog/lineage/mart.fin_journal_line?depth=x", "", catalogView, 400, domain.CodeValidation, "depth"},
		{"lineage self loop", http.MethodPost, "/v1/catalog/lineage/edges",
			`{"edges":[{"source":"mart.fin_journal_line","target":"mart.fin_journal_line"}]}`, steward, 400, domain.CodeValidation, "edges[0]"},
		{"lineage empty batch", http.MethodPost, "/v1/catalog/lineage/edges", `{"edges":[]}`, steward, 400, domain.CodeValidation, "edges"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, h, tc.method, tc.path, tc.body, tc.headers)
			if rec.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			p := problemCode(t, rec.Body.String())
			if p.Code != tc.code {
				t.Fatalf("code %s, want %s", p.Code, tc.code)
			}
			if tc.field != "" && p.Errors[tc.field] == "" {
				t.Fatalf("problem must name field %s: %s", tc.field, rec.Body.String())
			}
		})
	}
}

func TestDashboards_PutWithETagGetAndList(t *testing.T) {
	h := dbServer(t, time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC))
	path := "/v1/analytics/dashboards/controller_kpis"

	rec := do(t, h, http.MethodPut, path, dashboardBody, author)
	if rec.Code != http.StatusCreated || rec.Header().Get("ETag") != `"1"` {
		t.Fatalf("create: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	var d domain.Dashboard
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || d.RefreshSeconds != 60 || len(d.Tiles) != 2 {
		t.Fatalf("created: %+v %v", d, err)
	}
	if rec := do(t, h, http.MethodPut, path, dashboardBody, author); rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("replace without If-Match: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPut, path, dashboardBody, with(author, "If-Match", `"7"`)); rec.Code != http.StatusPreconditionFailed ||
		problemCode(t, rec.Body.String()).Code != domain.CodeVersionConflict {
		t.Fatalf("stale If-Match: %d %s", rec.Code, rec.Body.String())
	}
	renamed := strings.Replace(dashboardBody, "Controller KPIs", "Close KPIs", 1)
	if rec := do(t, h, http.MethodPut, path, renamed, with(author, "If-Match", `"1"`)); rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` {
		t.Fatalf("replace: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, path, "", viewer)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"2"` || !strings.Contains(rec.Body.String(), `"title":"Close KPIs"`) {
		t.Fatalf("get: %d %q %s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/v1/analytics/dashboards/ops", "", viewer); rec.Code != http.StatusNotFound {
		t.Fatalf("missing dashboard: %d", rec.Code)
	}
	var list DashboardsResponse
	rec = do(t, h, http.MethodGet, "/v1/analytics/dashboards", "", viewer)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].Version != 2 {
		t.Fatalf("list: %s", rec.Body.String())
	}
}

func TestExports_CreateReuseReplayAndLog(t *testing.T) {
	h := dbServer(t, time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC))
	rec := do(t, h, http.MethodPost, "/v1/analytics/exports", exportBody, with(exporter, headerIdempotency, "x1"))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	var job domain.Export
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil || job.Mode != domain.ExportJob || job.Status != domain.ExportQueued || job.Reused {
		t.Fatalf("job: %+v %v", job, err)
	}
	// The stored plan carries the caller's legal-entity filter.
	if len(job.Plan.Filters) != 1 || job.Plan.Filters[0].Values[0] != usmf {
		t.Fatalf("export plan must be scoped to the caller: %+v", job.Plan)
	}
	rec = do(t, h, http.MethodPost, "/v1/analytics/exports", exportBody, with(exporter, headerIdempotency, "x1"))
	if rec.Code != http.StatusAccepted || rec.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("replay: %d %q", rec.Code, rec.Header().Get("Idempotency-Replayed"))
	}
	// A new key for the identical request within the hour reuses the job (BR-46.7).
	rec = do(t, h, http.MethodPost, "/v1/analytics/exports", exportBody, with(exporter, headerIdempotency, "x2"))
	var reused domain.Export
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &reused) != nil || !reused.Reused || reused.ID != job.ID {
		t.Fatalf("reuse: %d %s", rec.Code, rec.Body.String())
	}
	// Another legal entity's identical query is another export.
	demf := with(exporter, tenant.HeaderLegalEntity, "00000000-0000-7000-8000-0000000000e2", headerIdempotency, "x3")
	if rec := do(t, h, http.MethodPost, "/v1/analytics/exports", exportBody, demf); rec.Code != http.StatusAccepted {
		t.Fatalf("other scope: %d %s", rec.Code, rec.Body.String())
	}
	var log ExportsResponse
	rec = do(t, h, http.MethodGet, "/v1/analytics/exports?format=XLSX&from=2026-03-02&to=2026-03-02", "", exporter)
	if err := json.Unmarshal(rec.Body.Bytes(), &log); err != nil || len(log.Items) != 2 {
		t.Fatalf("log: %s", rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/v1/analytics/exports?format=CSV", "", exporter)
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("CSV log must be empty: %s", rec.Body.String())
	}
}

func dbServerStore(t *testing.T, now time.Time) (http.Handler, *store.Postgres) {
	t.Helper()
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := store.New(pool)
	a := New(st)
	a.now = func() time.Time { return now }
	r := httpx.NewRouter()
	a.Register(r)
	return tenant.Middleware(r), st
}

func TestScheduleDeliveries_ListAfterDispatch(t *testing.T) {
	now := time.Date(2026, 1, 4, 12, 0, 0, 0, time.UTC)
	h, st := dbServerStore(t, now)
	rec := do(t, h, http.MethodPost, "/v1/analytics/schedules", scheduleBody, with(author, headerIdempotency, "s1"))
	var sc domain.Schedule
	if err := json.Unmarshal(rec.Body.Bytes(), &sc); err != nil {
		t.Fatal(err)
	}
	path := "/v1/analytics/schedules/" + sc.ID + "/deliveries"
	if rec := do(t, h, http.MethodGet, path, "", author); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("no deliveries yet: %d %s", rec.Code, rec.Body.String())
	}
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testTenant})
	if n, err := st.DispatchDue(ctx, *sc.NextRunAt, 5, func(context.Context, domain.Schedule) (store.Delivered, error) {
		return store.Delivered{RowCount: 3, Bytes: 100}, nil
	}); err != nil || n != 1 {
		t.Fatalf("dispatch = %d, %v", n, err)
	}
	var list DeliveriesResponse
	rec = do(t, h, http.MethodGet, path+"?status=DELIVERED", "", author)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 1 || list.Items[0].RowCount != 3 {
		t.Fatalf("deliveries: %s", rec.Body.String())
	}
	if rec := do(t, h, http.MethodGet, "/v1/analytics/schedules/0193d6a0-0000-7000-8000-00000000ffff/deliveries", "", author); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown schedule: %d", rec.Code)
	}
}

func TestLineage_AddThenWalk(t *testing.T) {
	h := dbServer(t, time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC))
	edges := `{"edges":[{"source":"raw.ledger.journal_line","target":"mart.fin_journal_line"},{"source":"mart.fin_journal_line","target":"cube.revenue"}]}`
	for i, want := range []string{`{"added":2}`, `{"added":0}`} {
		rec := do(t, h, http.MethodPost, "/v1/catalog/lineage/edges", edges, steward)
		if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != want {
			t.Fatalf("add #%d: %d %s", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := do(t, h, http.MethodGet, "/v1/catalog/lineage/mart.fin_journal_line?depth=1", "", catalogView)
	var got store.Lineage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Depth != 1 || len(got.Upstream) != 1 ||
		got.Upstream[0].FQN != "raw.ledger.journal_line" || len(got.Downstream) != 1 || got.Downstream[0].FQN != "cube.revenue" {
		t.Fatalf("lineage: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(t, h, http.MethodGet, "/v1/catalog/lineage/core.vendor", "", catalogView)
	if !strings.Contains(rec.Body.String(), `"upstream":[]`) || !strings.Contains(rec.Body.String(), `"downstream":[]`) {
		t.Fatalf("an asset without edges has empty lists: %s", rec.Body.String())
	}
}
