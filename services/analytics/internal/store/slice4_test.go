package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/analytics/internal/domain"
	analyticsevents "github.com/BenjaminSRussell/passage/services/analytics/internal/events"
	"github.com/BenjaminSRussell/passage/services/analytics/migrations"
)

func controllerDashboard() domain.Dashboard {
	return domain.Dashboard{ID: "controller_kpis", Title: "Controller KPIs",
		Tiles: []domain.Tile{{Title: "Revenue", Metric: "revenue", Period: "MTD"}}}
}

func TestPutDashboard_CreateReplaceAndVersioning(t *testing.T) {
	st := New(migrated(t))
	ctx := ctxFor(1, "")
	catalog := domain.GovernedMetrics()

	created, isNew, err := st.PutDashboard(ctx, controllerDashboard(), catalog, 0)
	if err != nil || !isNew || created.Version != 1 || created.RefreshSeconds != domain.DefaultRefreshSecs || created.UpdatedAt == nil {
		t.Fatalf("create = %+v new=%v, %v", created, isNew, err)
	}
	if _, _, err := st.PutDashboard(ctx, controllerDashboard(), catalog, 0); codeOf(err) != domain.CodePreconditionRequired {
		t.Fatalf("replace without If-Match = %v, want %s", err, domain.CodePreconditionRequired)
	}
	other := controllerDashboard()
	other.ID = "ops"
	if _, _, err := st.PutDashboard(ctx, other, catalog, 1); codeOf(err) != domain.CodeVersionConflict {
		t.Fatalf("If-Match on a missing dashboard = %v, want %s", err, domain.CodeVersionConflict)
	}
	next := controllerDashboard()
	next.Tiles = append(next.Tiles, domain.Tile{Title: "Margin", Metric: "gross_margin", Period: "YTD"})
	next.RefreshSeconds = 120
	updated, isNew, err := st.PutDashboard(ctx, next, catalog, 1)
	if err != nil || isNew || updated.Version != 2 || len(updated.Tiles) != 2 || updated.Tiles[1].Metric != "gross_margin" || updated.RefreshSeconds != 120 {
		t.Fatalf("replace = %+v new=%v, %v", updated, isNew, err)
	}
	if _, _, err := st.PutDashboard(ctx, controllerDashboard(), catalog, 1); codeOf(err) != domain.CodeVersionConflict {
		t.Fatalf("stale If-Match = %v, want %s", err, domain.CodeVersionConflict)
	}
	bad := controllerDashboard()
	bad.Tiles[0].Metric = "scratch_metric"
	if _, _, err := st.PutDashboard(ctx, bad, catalog, 2); codeOf(err) != domain.CodeValidation {
		t.Fatalf("an ungoverned metric must be refused by the store too: %v", err)
	}

	got, err := st.GetDashboard(ctx, "controller_kpis")
	if err != nil || got.Version != 2 || got.Tiles[1].Title != "Margin" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := st.GetDashboard(ctxFor(2, ""), "controller_kpis"); codeOf(err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 reads tenant 1's dashboard: %v", err)
	}
	// The same id in another tenant is another dashboard.
	if d, isNew, err := st.PutDashboard(ctxFor(2, ""), controllerDashboard(), catalog, 0); err != nil || !isNew || d.Version != 1 {
		t.Fatalf("tenant 2 create = %+v %v %v", d, isNew, err)
	}
	if list, err := st.ListDashboards(ctx); err != nil || len(list) != 1 || list[0].Version != 2 {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func revenueExport(user string) domain.Export {
	plan := domain.Plan{Model: "fin_journal_line", Grain: "journal_line", Measures: []string{"revenue"},
		Dimensions: []string{}, From: "2026-01-01", To: "2026-01-31", Filters: []domain.Filter{}}
	return domain.Export{Plan: plan, Format: "CSV", Mode: domain.ExportInline, RowCount: 10,
		LinkTTLHours: domain.DefaultLinkTTLHours, QueryHash: domain.QueryHash(domain.ExportCanonical(user, "CSV", plan))}
}

func decodeExport(t *testing.T, c Created) domain.Export {
	t.Helper()
	var e domain.Export
	if err := json.Unmarshal(c.Body, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// BR-46.7: an identical export within one hour returns the existing job; files
// are deleted 24 hours after creation.
func TestCreateExport_ReuseWindowIdempotencyAndLog(t *testing.T) {
	st := New(migrated(t))
	ctx := ctxFor(1, "")
	now := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

	first, err := st.CreateExport(ctx, "k1", "h1", revenueExport("user-test"), now)
	if err != nil || first.Status != 202 || first.Replayed {
		t.Fatalf("create = %+v, %v", first, err)
	}
	job := decodeExport(t, first)
	if job.Status != domain.ExportQueued || job.Reused || job.RequestedBy != "user-test" ||
		!job.DeleteAt.Equal(now.Add(24*time.Hour)) || !job.LinkExpiresAt.Equal(now.Add(24*time.Hour)) || job.Plan.Model != "fin_journal_line" {
		t.Fatalf("job = %+v", job)
	}
	replay, err := st.CreateExport(ctx, "k1", "h1", revenueExport("user-test"), now.Add(time.Minute))
	if err != nil || !replay.Replayed || decodeExport(t, replay).ID != job.ID {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := st.CreateExport(ctx, "k1", "h2", revenueExport("user-test"), now); codeOf(err) != domain.CodeIdempotencyReuse {
		t.Fatalf("key reuse = %v", err)
	}
	reused, err := st.CreateExport(ctx, "k2", "h2", revenueExport("user-test"), now.Add(59*time.Minute))
	if err != nil || reused.Status != 200 || !decodeExport(t, reused).Reused || decodeExport(t, reused).ID != job.ID {
		t.Fatalf("identical request inside the hour = %+v, %v", reused, err)
	}
	fresh, err := st.CreateExport(ctx, "k3", "h3", revenueExport("user-test"), now.Add(time.Hour))
	if err != nil || fresh.Status != 202 || decodeExport(t, fresh).ID == job.ID {
		t.Fatalf("after the hour a new job must start: %+v, %v", fresh, err)
	}
	// Another tenant with the same hash starts its own job.
	if other, err := st.CreateExport(ctxFor(2, ""), "k2", "h2", revenueExport("user-test"), now.Add(time.Minute)); err != nil || other.Status != 202 {
		t.Fatalf("tenant 2 = %+v, %v", other, err)
	}

	all, err := st.ListExports(ctx, ExportFilter{})
	if err != nil || len(all) != 2 || !all[0].CreatedAt.After(all[1].CreatedAt) {
		t.Fatalf("log = %+v, %v; want two jobs newest first", all, err)
	}
	for _, tc := range []struct {
		f    ExportFilter
		want int
	}{
		{ExportFilter{RequestedBy: "user-test"}, 2},
		{ExportFilter{RequestedBy: "someone-else"}, 0},
		{ExportFilter{Format: "XLSX"}, 0},
		{ExportFilter{From: "2026-03-02", To: "2026-03-02"}, 2},
		{ExportFilter{From: "2026-03-03"}, 0},
		{ExportFilter{To: "2026-03-01"}, 0},
	} {
		if got, err := st.ListExports(ctx, tc.f); err != nil || len(got) != tc.want {
			t.Errorf("filter %+v = %d jobs, %v; want %d", tc.f, len(got), err, tc.want)
		}
	}
}

// seedSchedule creates a schedule and moves its next run to due.
func seedSchedule(t *testing.T, st *Postgres, ctx context.Context, key string, in domain.Schedule, due time.Time) domain.Schedule {
	t.Helper()
	out, err := st.CreateSchedule(ctx, key, key, in, due.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var sc domain.Schedule
	if err := json.Unmarshal(out.Body, &sc); err != nil {
		t.Fatal(err)
	}
	if _, err := st.pool.Exec(context.Background(), `update report_schedule set next_run_at = $2 where schedule_id = $1`, sc.ID, due); err != nil {
		t.Fatal(err)
	}
	return sc
}

func countRows(t *testing.T, st *Postgres, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := st.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// AC-46.10: at Monday 06:00 Europe/Berlin the report is delivered,
// report.delivered is emitted and the next run is the following Monday.
func TestDispatchDue_DeliversAndEmits(t *testing.T) {
	st := New(migrated(t))
	ctx := ctxFor(1, "")
	monday := time.Date(2026, 1, 5, 5, 0, 0, 0, time.UTC) // 06:00 CET
	sc := seedSchedule(t, st, ctx, "s1", scheduleIn(), monday)
	notDue := seedSchedule(t, st, ctx, "s2", scheduleIn(), monday.Add(time.Minute))
	other := seedSchedule(t, st, ctxFor(2, ""), "s1", scheduleIn(), monday)

	var delivered []string
	n, err := st.DispatchDue(ctx, monday, 10, func(_ context.Context, s domain.Schedule) (Delivered, error) {
		delivered = append(delivered, s.ID)
		return Delivered{RowCount: 42, Bytes: 2048}, nil
	})
	if err != nil || n != 1 || len(delivered) != 1 || delivered[0] != sc.ID {
		t.Fatalf("dispatch = %d %v, %v; want only tenant 1's due schedule %s", n, delivered, err, sc.ID)
	}
	got, err := st.GetSchedule(ctx, sc.ID)
	if err != nil || got.FailureCount != 0 || got.LastRunAt == nil || !got.LastRunAt.Equal(monday) ||
		!got.NextRunAt.Equal(time.Date(2026, 1, 12, 5, 0, 0, 0, time.UTC)) || got.Version != 1 {
		t.Fatalf("after delivery = %+v, %v", got, err)
	}
	if s, _ := st.GetSchedule(ctx, notDue.ID); s.LastRunAt != nil {
		t.Fatalf("a schedule due later ran: %+v", s)
	}
	if s, _ := st.GetSchedule(ctxFor(2, ""), other.ID); s.LastRunAt != nil {
		t.Fatalf("tenant 2's schedule ran in tenant 1's dispatch: %+v", s)
	}
	list, err := st.ListDeliveries(ctx, sc.ID, "")
	if err != nil || len(list) != 1 || list[0].Status != domain.DeliveryDelivered || list[0].Attempt != 1 ||
		list[0].RowCount != 42 || list[0].Bytes != 2048 || list[0].DestinationType != domain.DestinationEmail || list[0].RetryAt != nil {
		t.Fatalf("deliveries = %+v, %v", list, err)
	}
	var data analyticsevents.ReportDeliveredData
	if c := lastEvent(t, ctx, st, analyticsevents.ReportDelivered, &data); c != 1 || data.ScheduleID != sc.ID ||
		data.RunID != list[0].RunID || data.RowCount != 42 || data.Bytes != 2048 || data.Format != "PDF" || data.Destination != "EMAIL" {
		t.Fatalf("report.delivered = %d %+v", c, data)
	}
	if strings.Contains(string(mustJSON(t, data)), "cfo@example.com") {
		t.Fatal("the event must not carry recipient addresses")
	}
	// Nothing is due any more: a second pass does nothing.
	if n, err := st.DispatchDue(ctx, monday, 10, func(context.Context, domain.Schedule) (Delivered, error) {
		t.Fatal("nothing is due")
		return Delivered{}, nil
	}); err != nil || n != 0 {
		t.Fatalf("second pass = %d, %v", n, err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// FR-46.10: a failing delivery is retried after 1, 4 and 16 minutes within one
// run, then FAILED, and the schedule moves on to its next cron slot.
func TestDispatchDue_RetriesThenFails(t *testing.T) {
	st := New(migrated(t))
	ctx := ctxFor(1, "")
	at := time.Date(2026, 1, 5, 5, 0, 0, 0, time.UTC)
	sc := seedSchedule(t, st, ctx, "s1", scheduleIn(), at)
	boom := func(context.Context, domain.Schedule) (Delivered, error) {
		return Delivered{RowCount: 9}, errors.New("smtp: 550 mailbox unavailable " + strings.Repeat("x", 600))
	}
	for i, wait := range []time.Duration{time.Minute, 4 * time.Minute, 16 * time.Minute} {
		if n, err := st.DispatchDue(ctx, at, 10, boom); err != nil || n != 1 {
			t.Fatalf("attempt %d = %d, %v", i+1, n, err)
		}
		got, _ := st.GetSchedule(ctx, sc.ID)
		if got.FailureCount != i+1 || !got.NextRunAt.Equal(at.Add(wait)) {
			t.Fatalf("after failure %d: %+v; want retry at %v", i+1, got, at.Add(wait))
		}
		at = at.Add(wait)
	}
	if n, err := st.DispatchDue(ctx, at, 10, boom); err != nil || n != 1 {
		t.Fatalf("last attempt = %d, %v", n, err)
	}
	got, _ := st.GetSchedule(ctx, sc.ID)
	if got.FailureCount != 0 || !got.NextRunAt.Equal(time.Date(2026, 1, 12, 5, 0, 0, 0, time.UTC)) {
		t.Fatalf("after FAILED = %+v; want the next Monday and a reset counter", got)
	}
	list, err := st.ListDeliveries(ctx, sc.ID, "")
	if err != nil || len(list) != 4 {
		t.Fatalf("deliveries = %+v, %v", list, err)
	}
	for i, d := range list { // newest first
		wantStatus := domain.DeliveryRetry
		if i == 0 {
			wantStatus = domain.DeliveryFailed
		}
		if d.Status != wantStatus || d.Attempt != 4-i || d.RunID != list[0].RunID || d.RowCount != 0 ||
			len(d.Error) != maxErrorText || (d.RetryAt == nil) != (wantStatus != domain.DeliveryRetry) {
			t.Fatalf("delivery %d = %+v", i, d)
		}
	}
	failedRun := list[0].RunID
	if failed, _ := st.ListDeliveries(ctx, sc.ID, domain.DeliveryFailed); len(failed) != 1 {
		t.Fatalf("FAILED filter = %+v", failed)
	}
	if c := lastEvent(t, ctx, st, analyticsevents.ReportDelivered, &analyticsevents.ReportDeliveredData{}); c != 0 {
		t.Fatalf("a failed run emitted %d report.delivered events", c)
	}
	// The next run opens a new run id and succeeds on its first attempt.
	if _, err := st.pool.Exec(context.Background(), `update report_schedule set next_run_at = $2 where schedule_id = $1`, sc.ID, at); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DispatchDue(ctx, at, 10, func(context.Context, domain.Schedule) (Delivered, error) { return Delivered{}, nil }); err != nil {
		t.Fatal(err)
	}
	list, _ = st.ListDeliveries(ctx, sc.ID, domain.DeliveryDelivered)
	if len(list) != 1 || list[0].Attempt != 1 || list[0].RunID == failedRun {
		t.Fatalf("next run = %+v", list)
	}
	if _, err := st.ListDeliveries(ctx, "0193d6a0-0000-7000-8000-00000000ffff", ""); codeOf(err) != domain.CodeNotFound {
		t.Fatalf("unknown schedule = %v", err)
	}
	if _, err := st.ListDeliveries(ctxFor(2, ""), sc.ID, ""); codeOf(err) != domain.CodeNotFound {
		t.Fatalf("tenant 2 reads tenant 1's deliveries: %v", err)
	}
}

// Two dispatchers running at once never deliver the same schedule twice.
func TestDispatchDue_ConcurrentDispatchersSkipLockedRows(t *testing.T) {
	st := New(migrated(t))
	ctx := ctxFor(1, "")
	at := time.Date(2026, 1, 5, 5, 0, 0, 0, time.UTC)
	seedSchedule(t, st, ctx, "s1", scheduleIn(), at)
	seedSchedule(t, st, ctx, "s2", scheduleIn(), at)

	inFirst := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := st.DispatchDue(ctx, at, 1, func(context.Context, domain.Schedule) (Delivered, error) {
			close(inFirst)
			<-release
			return Delivered{}, nil
		})
		done <- err
	}()
	<-inFirst
	// The first dispatcher holds one schedule; the second takes only the other.
	n, err := st.DispatchDue(ctx, at, 10, func(context.Context, domain.Schedule) (Delivered, error) { return Delivered{}, nil })
	close(release)
	if err != nil || n != 1 {
		t.Fatalf("second dispatcher = %d, %v; want exactly the unlocked schedule", n, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if c := countRows(t, st, `select count(*) from report_delivery`); c != 2 {
		t.Fatalf("%d deliveries, want one per schedule", c)
	}
}

func TestLineage_AddWalkDepthAndCycles(t *testing.T) {
	st := New(migrated(t))
	ctx := ctxFor(1, "")
	edges := []domain.Edge{
		{Source: "raw.ledger.journal_line", Target: "core.journal_line"},
		{Source: "core.journal_line", Target: "mart.fin_journal_line"},
		{Source: "core.main_account", Target: "mart.fin_journal_line"},
		{Source: "mart.fin_journal_line", Target: "cube.revenue"},
		{Source: "cube.revenue", Target: "dashboard.controller_kpis"},
	}
	if n, err := st.AddLineageEdges(ctx, edges); err != nil || n != 5 {
		t.Fatalf("add = %d, %v", n, err)
	}
	if n, err := st.AddLineageEdges(ctx, edges[:2]); err != nil || n != 0 {
		t.Fatalf("re-sending edges added %d, %v; want 0", n, err)
	}
	if _, err := st.AddLineageEdges(ctx, []domain.Edge{{Source: "mart.x", Target: "mart.x"}}); codeOf(err) != domain.CodeValidation {
		t.Fatalf("self loop = %v", err)
	}

	got, err := st.GetLineage(ctx, "mart.fin_journal_line", domain.MaxLineageDepth)
	if err != nil {
		t.Fatal(err)
	}
	want := []LineageAsset{{"core.journal_line", 1}, {"core.main_account", 1}, {"raw.ledger.journal_line", 2}}
	if !equalAssets(got.Upstream, want) {
		t.Fatalf("upstream = %+v, want %+v", got.Upstream, want)
	}
	if !equalAssets(got.Downstream, []LineageAsset{{"cube.revenue", 1}, {"dashboard.controller_kpis", 2}}) {
		t.Fatalf("downstream = %+v", got.Downstream)
	}
	if one, _ := st.GetLineage(ctx, "mart.fin_journal_line", 1); len(one.Upstream) != 2 || len(one.Downstream) != 1 {
		t.Fatalf("depth 1 = %+v", one)
	}
	// A cycle ends at the depth and the start asset is never listed.
	if _, err := st.AddLineageEdges(ctx, []domain.Edge{{Source: "dashboard.controller_kpis", Target: "raw.ledger.journal_line"}}); err != nil {
		t.Fatal(err)
	}
	cyc, err := st.GetLineage(ctx, "cube.revenue", domain.MaxLineageDepth)
	if err != nil || len(cyc.Downstream) != 4 || containsAsset(cyc.Downstream, "cube.revenue") || cyc.Downstream[0].FQN != "dashboard.controller_kpis" {
		t.Fatalf("cycle walk = %+v, %v", cyc, err)
	}
	if none, err := st.GetLineage(ctx, "mart.unknown", 0); err != nil || len(none.Upstream) != 0 || len(none.Downstream) != 0 {
		t.Fatalf("unknown asset = %+v, %v", none, err)
	}
	if other, _ := st.GetLineage(ctxFor(2, ""), "mart.fin_journal_line", 5); len(other.Upstream) != 0 {
		t.Fatalf("tenant 2 sees tenant 1's lineage: %+v", other)
	}
}

func equalAssets(a, b []LineageAsset) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsAsset(list []LineageAsset, fqn string) bool {
	for _, a := range list {
		if a.FQN == fqn {
			return true
		}
	}
	return false
}

// The slice-4 tables isolate tenants through row-level security alone, and
// every slice-4 write path works with the grants in 0004 as passage_app.
func TestSlice4Tables_RowLevelSecurityAndGrants(t *testing.T) {
	if os.Getenv(testkit.EnvAppURL) == "" {
		t.Skipf("%s not set; skipping RLS test", testkit.EnvAppURL)
	}
	admin, app := testkit.DBWithApp(t)
	bg := context.Background()
	if err := dbx.Migrate(bg, admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	st := New(app)
	ctx := ctxFor(1, "")
	catalog := domain.GovernedMetrics()
	now := time.Date(2026, 1, 5, 5, 0, 0, 0, time.UTC)

	if _, _, err := st.PutDashboard(ctx, controllerDashboard(), catalog, 0); err != nil {
		t.Fatalf("create dashboard as app role: %v", err)
	}
	if _, _, err := st.PutDashboard(ctx, controllerDashboard(), catalog, 1); err != nil {
		t.Fatalf("replace dashboard as app role: %v", err)
	}
	for range 2 {
		if _, err := st.CreateExport(ctx, "exp", "h", revenueExport("user-test"), now); err != nil {
			t.Fatalf("create or replay export as app role: %v", err)
		}
	}
	if _, err := st.AddLineageEdges(ctx, []domain.Edge{{Source: "core.journal_line", Target: "mart.fin_journal_line"}}); err != nil {
		t.Fatalf("add lineage as app role: %v", err)
	}
	out, err := st.CreateSchedule(ctx, "sched", "h", scheduleIn(), now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var sc domain.Schedule
	if err := json.Unmarshal(out.Body, &sc); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(bg, `update report_schedule set next_run_at = $1`, now); err != nil {
		t.Fatal(err)
	}
	fail := true
	for range 2 { // a retry and a delivery: FOR UPDATE, both update paths and the event
		n, err := st.DispatchDue(ctx, now, 1, func(context.Context, domain.Schedule) (Delivered, error) {
			if fail {
				fail = false
				return Delivered{}, errors.New("sftp timeout")
			}
			return Delivered{RowCount: 1}, nil
		})
		if err != nil || n != 1 {
			t.Fatalf("dispatch as app role = %d, %v", n, err)
		}
		now = now.Add(time.Minute)
	}
	if list, err := st.ListDeliveries(ctx, sc.ID, ""); err != nil || len(list) != 2 {
		t.Fatalf("deliveries as app role = %+v, %v", list, err)
	}
	for _, stmt := range []string{
		`delete from dashboard`, `update export_job set status = 'READY'`, `delete from export_job`,
		`update report_delivery set status = 'DELIVERED'`, `delete from report_delivery`,
		`update lineage_edge set created_at = now()`, `delete from lineage_edge`,
		`update report_schedule set name = name`, `update report_schedule set cron = cron`,
	} {
		err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, stmt)
			return err
		})
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Errorf("%q as app role = %v; want insufficient_privilege 42501", stmt, err)
		}
	}
	for _, table := range []string{"dashboard", "export_job", "report_delivery", "lineage_edge"} {
		var mine, theirs int
		count := func(n int, dst *int) {
			if err := dbx.WithTenantTx(ctxFor(n, ""), app, func(tx pgx.Tx) error {
				return tx.QueryRow(bg, `select count(*) from `+table).Scan(dst)
			}); err != nil {
				t.Fatal(err)
			}
		}
		count(1, &mine)
		count(2, &theirs)
		if mine == 0 || theirs != 0 {
			t.Errorf("%s: tenant 1 sees %d rows, tenant 2 sees %d; want >0 and 0", table, mine, theirs)
		}
	}
	err = dbx.WithTenantTx(ctxFor(2, ""), app, func(tx pgx.Tx) error {
		_, err := tx.Exec(bg, `insert into lineage_edge (tenant_id, source_fqn, target_fqn) values ($1, 'core.a', 'mart.b')`, testkit.TenantID(1))
		return err
	})
	if err == nil {
		t.Fatal("RLS must refuse a lineage edge for another tenant")
	}
}
