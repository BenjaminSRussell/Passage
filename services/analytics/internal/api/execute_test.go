package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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

// fakeCube answers every query with rows and records what it was asked.
type fakeCube struct {
	mu      sync.Mutex
	rows    []map[string]any
	err     error
	calls   int
	tenant  string
	scope   domain.Scope
	queries []domain.CubeQuery
}

func (f *fakeCube) Load(_ context.Context, tenantID string, scope domain.Scope, q domain.CubeQuery) ([]map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.tenant, f.scope = tenantID, scope
	f.queries = append(f.queries, q)
	return f.rows, f.err
}

func executeServer(e Executor) http.Handler {
	r := httpx.NewRouter()
	a := New(nil)
	if e != nil {
		a.WithExecutor(e)
	}
	a.Register(r)
	return tenant.Middleware(r)
}

const executeBody = `{"measures":["revenue"],"dimensions":["fiscal_period","currency"],"from":"2026-10-01","to":"2026-10-31"}`

func TestExecute_RunsThePlanWithTheCallersScope(t *testing.T) {
	f := &fakeCube{rows: []map[string]any{{"fin_journal_line.revenue": "1250.50", "fin_journal_line.fiscal_period": "2026-10", "fin_journal_line.currency": "USD"}}}
	rec := do(t, executeServer(f), http.MethodPost, "/v1/analytics/query:execute", executeBody, withLE(usmf))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var got ExecuteResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.RowCount != 1 || got.Rows[0]["revenue"] != "1250.50" || got.Rows[0]["fiscal_period"] != "2026-10" {
		t.Fatalf("response: %s", rec.Body)
	}
	if f.tenant != "00000000-0000-7000-8000-000000000001" || len(f.scope.LegalEntityIDs) != 1 || f.scope.LegalEntityIDs[0] != usmf {
		t.Fatalf("executor got tenant %q scope %+v", f.tenant, f.scope)
	}
	if q := f.queries[0]; len(q.Filters) != 2 || q.Filters[0].Values[0] != usmf || q.Filters[1].Member != "fin_journal_line.tenant_id" || q.Measures[0] != "fin_journal_line.revenue" {
		t.Fatalf("query %+v", q)
	}
}

func TestExecute_NoScopeIsEmptyAndNeverCallsCube(t *testing.T) {
	f := &fakeCube{}
	rec := do(t, executeServer(f), http.MethodPost, "/v1/analytics/query:execute", executeBody, viewer)
	if rec.Code != http.StatusOK || f.calls != 0 || !strings.Contains(rec.Body.String(), `"rows":[]`) || !strings.Contains(rec.Body.String(), "NO_SCOPE") {
		t.Fatalf("status %d calls %d: %s", rec.Code, f.calls, rec.Body)
	}
}

func TestExecute_Refusals(t *testing.T) {
	if rec := do(t, executeServer(&fakeCube{}), http.MethodPost, "/v1/analytics/query:execute", executeBody, nil); rec.Code != http.StatusForbidden {
		t.Errorf("without duty: %d", rec.Code)
	}
	if rec := do(t, executeServer(nil), http.MethodPost, "/v1/analytics/query:execute", executeBody, withLE(usmf)); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "ANA-0120") {
		t.Errorf("not configured: %d %s", rec.Code, rec.Body)
	}
	down := &fakeCube{err: errors.New("boom")}
	if rec := do(t, executeServer(down), http.MethodPost, "/v1/analytics/query:execute", executeBody, withLE(usmf)); rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("cube down: %d %s", rec.Code, rec.Body)
	}
	for name, body := range map[string]string{
		"no currency": `{"measures":["revenue"],"dimensions":["fiscal_period"],"from":"2026-10-01","to":"2026-10-31"}`,
		"unpublished": `{"measures":["scratch"],"from":"2026-10-01","to":"2026-10-31"}`,
		"unmodelled":  `{"measures":["dso_days"],"from":"2026-10-01","to":"2026-10-31"}`,
		"bad dates":   `{"measures":["revenue"],"from":"x","to":"y"}`,
	} {
		if rec := do(t, executeServer(&fakeCube{}), http.MethodPost, "/v1/analytics/query:execute", body, withLE(usmf)); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestTileValues_EachTileHasItsOwnStatus(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	f := &fakeCube{rows: []map[string]any{{"fin_journal_line.revenue": "98000.00", "fin_journal_line.currency": "USD"}}}
	a := New(nil).WithExecutor(f)
	d := domain.Dashboard{ID: "controller", Title: "Controller", Tiles: []domain.Tile{
		{Title: "Revenue", Metric: "revenue", Period: "MTD"},
		{Title: "DSO", Metric: "dso_days", Period: "QTD"},
	}}
	p := tenant.Principal{TenantID: testTenant, LegalEntityID: usmf}
	tiles := a.tileValues(context.Background(), p, d, now)
	if len(tiles) != 2 {
		t.Fatalf("tiles = %+v", tiles)
	}
	if tiles[0].Status != domain.TileOK || tiles[0].Currency != "USD" || tiles[0].Value == nil || *tiles[0].Value != "98000.00" || tiles[0].From != "2026-11-01" || tiles[0].To != "2026-11-11" || tiles[0].MetricName != "Revenue" {
		t.Errorf("revenue tile = %+v", tiles[0])
	}
	if tiles[1].Status != domain.TileNotModeled || tiles[1].Value != nil || f.calls != 1 {
		t.Errorf("dso tile = %+v (calls %d)", tiles[1], f.calls)
	}
	// No scope: NO_SCOPE, no call. Cube down: UNAVAILABLE. An empty result: OK with no value.
	f.calls = 0
	if got := a.tileValues(context.Background(), tenant.Principal{TenantID: testTenant}, d, now); got[0].Status != domain.TileNoScope || f.calls != 0 {
		t.Errorf("no scope tile = %+v", got[0])
	}
	f.err = errors.New("down")
	if got := a.tileValues(context.Background(), p, d, now); got[0].Status != domain.TileUnavail {
		t.Errorf("cube down tile = %+v", got[0])
	}
	f.err, f.rows = nil, nil
	if got := a.tileValues(context.Background(), p, d, now); got[0].Status != domain.TileOK || got[0].Value != nil {
		t.Errorf("empty tile = %+v", got[0])
	}
	if got := New(nil).tileValues(context.Background(), p, d, now); got[0].Status != domain.TileUnavail {
		t.Errorf("no executor tile = %+v", got[0])
	}
}

func TestDashboardValues_ServesTheStoredDashboard(t *testing.T) {
	pool := testkit.DB(t)
	if err := dbx.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	f := &fakeCube{rows: []map[string]any{{"fin_journal_line.revenue": "42.00", "fin_journal_line.currency": "USD"}}}
	a := New(store.New(pool)).WithExecutor(f)
	a.now = func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }
	r := httpx.NewRouter()
	a.Register(r)
	h := tenant.Middleware(r)
	if rec := do(t, h, http.MethodPut, "/v1/analytics/dashboards/ops", dashboardBody, author); rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	rec := do(t, h, http.MethodGet, "/v1/analytics/dashboards/ops/values", "", withLE(usmf))
	var got DashboardValuesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || len(got.Tiles) != 2 ||
		got.Tiles[0].Status != domain.TileOK || *got.Tiles[0].Value != "42.00" || got.Tiles[1].Status != domain.TileNotModeled || got.Title != "Controller KPIs" {
		t.Fatalf("values: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, h, http.MethodGet, "/v1/analytics/dashboards/ops/values", "", nil); rec.Code != http.StatusForbidden {
		t.Errorf("without duty: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodGet, "/v1/analytics/dashboards/nope/values", "", withLE(usmf)); rec.Code != http.StatusNotFound {
		t.Errorf("unknown dashboard: %d", rec.Code)
	}
}

func TestTileValues_SeveralCurrenciesAreNeverAdded(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	f := &fakeCube{rows: []map[string]any{
		{"fin_journal_line.revenue": "100.00", "fin_journal_line.gross_margin_pct": "0.3", "fin_journal_line.currency": "USD"},
		{"fin_journal_line.revenue": "90.00", "fin_journal_line.gross_margin_pct": "0.4", "fin_journal_line.currency": "EUR"},
		{"fin_journal_line.revenue": nil, "fin_journal_line.gross_margin_pct": nil, "fin_journal_line.currency": "GBP"},
	}}
	a := New(nil).WithExecutor(f)
	d := domain.Dashboard{ID: "x", Tiles: []domain.Tile{{Title: "Revenue", Metric: "revenue", Period: "MTD"}, {Title: "Margin", Metric: "gross_margin_pct", Period: "MTD"}}}
	tiles := a.tileValues(context.Background(), tenant.Principal{TenantID: testTenant, LegalEntityID: usmf}, d, now)
	for _, tv := range tiles {
		if tv.Status != domain.TileMixedCurrency || tv.Value != nil || tv.Currency != "" {
			t.Errorf("%s = %+v", tv.Title, tv)
		}
	}
	if q := f.queries[0]; len(q.Dimensions) != 1 || q.Dimensions[0] != "fin_journal_line.currency" {
		t.Errorf("tile query does not group by currency: %+v", q)
	}
	// A currency with no amounts does not make the figure mixed.
	f.rows = append(f.rows[:1:1], map[string]any{"fin_journal_line.revenue": nil, "fin_journal_line.gross_margin_pct": nil, "fin_journal_line.currency": "EUR"})
	if got := a.tileValues(context.Background(), tenant.Principal{TenantID: testTenant, LegalEntityID: usmf}, d, now)[0]; got.Status != domain.TileOK || got.Currency != "USD" || *got.Value != "100.00" {
		t.Errorf("one real currency = %+v", got)
	}
	// A ratio keeps no currency.
	if got := a.tileValues(context.Background(), tenant.Principal{TenantID: testTenant, LegalEntityID: usmf}, d, now)[1]; got.Status != domain.TileOK || got.Currency != "" {
		t.Errorf("ratio = %+v", got)
	}
}

func TestTileValues_RunConcurrentlyInOrderAndStopAtTheDeadline(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	slow := &slowCube{release: make(chan struct{})}
	a := New(nil).WithExecutor(slow)
	var tiles []domain.Tile
	for i := 0; i < 8; i++ {
		tiles = append(tiles, domain.Tile{Title: string(rune('A' + i)), Metric: "revenue", Period: "MTD"})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	got := a.tileValues(ctx, tenant.Principal{TenantID: testTenant, LegalEntityID: usmf}, domain.Dashboard{ID: "x", Tiles: tiles}, now)
	for i, tv := range got {
		if tv.Title != string(rune('A'+i)) || tv.Status != domain.TileUnavail {
			t.Errorf("tile %d = %+v", i, tv)
		}
	}
	if slow.peak.Load() < 2 || slow.peak.Load() > tileConcurrency {
		t.Errorf("peak concurrency %d, want 2..%d", slow.peak.Load(), tileConcurrency)
	}
}

// slowCube blocks until the context ends, counting how many queries run at once.
type slowCube struct {
	release chan struct{}
	now     atomic.Int32
	peak    atomic.Int32
}

func (s *slowCube) Load(ctx context.Context, _ string, _ domain.Scope, _ domain.CubeQuery) ([]map[string]any, error) {
	n := s.now.Add(1)
	defer s.now.Add(-1)
	for {
		p := s.peak.Load()
		if n <= p || s.peak.CompareAndSwap(p, n) {
			break
		}
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		return nil, nil
	}
}
