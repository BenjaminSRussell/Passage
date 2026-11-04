package store_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

const bottleneckType = "dev.passage.scheduling.bottleneck.detected.v1"

// assertMaterialized checks that scheduling_capacity_load holds exactly what
// the on-read weekly load computes: a row for every week with a booking, with
// the same minutes and flag, and none for a week without.
func assertMaterialized(t *testing.T, ctx context.Context, s *store.Postgres, admin *pgxpool.Pool, tenantN int) {
	t.Helper()
	live, err := s.CapacityLoad(ctx, store.LoadFilter{From: monday.AddDate(0, 0, -14), Weeks: 10, ThresholdPct: 90, Consecutive: 2})
	if err != nil {
		t.Fatal(err)
	}
	type key struct {
		res  string
		week string
	}
	want := map[key]domain.WeekLoad{}
	for _, l := range live {
		if l.BookedMinutes > 0 {
			want[key{l.ResourceID, l.WeekStart.Format(time.DateOnly)}] = l
		}
	}
	rows, err := admin.Query(context.Background(), `select resource_id::text, week_start::text, open_minutes, booked_minutes, bottleneck
		from scheduling_capacity_load where tenant_id = $1`, testkit.TenantID(tenantN))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := 0
	for rows.Next() {
		var res, week string
		var open, booked int
		var flagged bool
		if err := rows.Scan(&res, &week, &open, &booked, &flagged); err != nil {
			t.Fatal(err)
		}
		got++
		l, ok := want[key{res, week}]
		if !ok || l.OpenMinutes != open || l.BookedMinutes != booked || l.Bottleneck != flagged {
			t.Errorf("materialized %s %s = open %d booked %d flagged %v; on read %+v (present %v)", res, week, open, booked, flagged, l, ok)
		}
	}
	if got != len(want) {
		t.Errorf("materialized %d weeks, on read %d weeks have a booking", got, len(want))
	}
}

func openEpisodes(t *testing.T, s *store.Postgres, ctx context.Context) []store.BottleneckEpisode {
	t.Helper()
	out, err := s.ListBottlenecks(ctx, store.BottleneckFilter{Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// Two orders of 2300 minutes on a 2400-minute week load weeks 1 and 2 above
// 90% (100% and 91.7%): one bottleneck.detected for the episode however many
// runs follow, none when the load drops, and a second one for a new episode.
func TestBottleneckEpisode_OneEventPerEpisode(t *testing.T) {
	s, _, admin := setup(t)
	s.WithBottleneck(90, 2).WithClock(func() time.Time { return monday.AddDate(0, 0, -20) })
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	o1, o2 := ids.New(), ids.New()
	mustOperation(t, ctx, s, o1, 10, 2300, m.ID)
	mustOperation(t, ctx, s, o2, 10, 2300, m.ID)

	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	eps := openEpisodes(t, s, ctx)
	if len(eps) != 1 || eps[0].ResourceID != m.ID || eps[0].FirstWeek != monday.Format(time.DateOnly) || eps[0].FlaggedWeeks != 2 ||
		eps[0].CurrentFlaggedWeeks != 2 || eps[0].PeakLoadPct != 100 || eps[0].ThresholdPct != 90 || eps[0].ConsecutiveWeeks != 2 {
		t.Fatalf("open episodes = %+v, want one on M1 from %s with 2 flagged weeks peaking at 100%%", eps, monday.Format(time.DateOnly))
	}
	if n := outboxCount(t, admin, bottleneckType); n != 1 {
		t.Fatalf("bottleneck.detected events = %d, want 1", n)
	}
	assertMaterialized(t, ctx, s, admin, 1)

	for i := 0; i < 2; i++ {
		if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	if n := outboxCount(t, admin, bottleneckType); n != 1 || len(openEpisodes(t, s, ctx)) != 1 {
		t.Fatalf("after two more runs: %d events, want still 1 for the same episode", n)
	}
	detail, err := s.GetBottleneck(ctx, eps[0].ID)
	if err != nil || len(detail.Weeks) != 2 || !detail.Weeks[0].Bottleneck || detail.Weeks[0].BookedMinutes != 2400 {
		t.Fatalf("detail = %+v err=%v, want the two flagged weeks", detail, err)
	}

	// Taking one order off the schedule ends the episode quietly.
	if _, _, err := s.UnscheduleOrder(ctx, o2, idem("u1")); err != nil {
		t.Fatal(err)
	}
	if len(openEpisodes(t, s, ctx)) != 0 || outboxCount(t, admin, bottleneckType) != 1 {
		t.Fatal("unscheduling the second order must close the episode without a new event")
	}
	closed, err := s.ListBottlenecks(ctx, store.BottleneckFilter{Status: "closed"})
	if err != nil || len(closed) != 1 || closed[0].ClosedAt == nil || closed[0].CurrentFlaggedWeeks != 0 {
		t.Fatalf("closed = %+v err=%v", closed, err)
	}
	assertMaterialized(t, ctx, s, admin, 1)

	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if n := outboxCount(t, admin, bottleneckType); n != 2 || len(openEpisodes(t, s, ctx)) != 1 {
		t.Fatalf("a new overload: %d events, want 2 (one per episode)", n)
	}

	// Once the flagged weeks are behind us the episode is over; a rebuild
	// closes it and does not announce anything.
	s.WithClock(func() time.Time { return monday.AddDate(0, 0, 60) })
	out, err := s.RefreshCapacityLoad(ctx)
	if err != nil || out.Resources != 1 || out.LoadRows != 2 || out.OpenEpisodes != 0 {
		t.Fatalf("refresh = %+v err=%v, want 1 resource, 2 weeks, no open episode", out, err)
	}
	if n := outboxCount(t, admin, bottleneckType); n != 2 {
		t.Fatalf("events after refresh = %d, want 2", n)
	}
	if _, err := s.GetBottleneck(ctx, ids.New()); problemCode(err) != store.CodeBottleneckNotFound {
		t.Fatalf("unknown episode: %v", err)
	}
	if other := openEpisodes(t, s, tenantCtx(2)); len(other) != 0 {
		t.Fatalf("tenant 2 sees %+v", other)
	}
}

// Every writer that moves bookings or changes a calendar keeps the table equal
// to the load computed on read.
func TestCapacityLoad_StaysCurrentThroughEveryWriter(t *testing.T) {
	s, _, admin := setup(t)
	s.WithBottleneck(90, 2).WithClock(func() time.Time { return monday.AddDate(0, 0, -20) })
	ctx := tenantCtx(1)
	m1, m2, tool := mustResource(t, ctx, s, "M1", true), mustResource(t, ctx, s, "M2", true), mustResource(t, ctx, s, "TOOL", true)
	a := secOperation(t, ctx, s, 10, 600, m1.ID, tool.ID)
	b := mustOperation(t, ctx, s, ids.New(), 10, 900, m1.ID)
	c := mustOperation(t, ctx, s, ids.New(), 10, 300, m1.ID)
	assertMaterialized(t, ctx, s, admin, 1)

	run1, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	assertMaterialized(t, ctx, s, admin, 1)
	if rows := loadRows(t, admin); rows == 0 {
		t.Fatal("run left no materialized week")
	}

	if _, _, err := s.RunSchedule(ctx, siteRun(at(1, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	assertMaterialized(t, ctx, s, admin, 1)

	if _, _, err := s.RollbackRun(ctx, run1.ID, idem("rb")); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	assertMaterialized(t, ctx, s, admin, 1)

	if _, _, _, err := moveTo(ctx, s, b.ID, m2.ID, at(2, 8, 0)); err != nil {
		t.Fatal(err)
	}
	assertMaterialized(t, ctx, s, admin, 1)

	if _, _, err := s.CreateCapacityOverride(ctx, store.CapacityOverride{ResourceID: m1.ID, Kind: domain.OverrideDowntime, StartAt: at(3, 8, 0), EndAt: at(3, 16, 0)}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	assertMaterialized(t, ctx, s, admin, 1)

	half := weekdays(8*60, 12*60)
	if _, _, err := s.UpdateResource(ctx, m1.ID, m1.Version, store.ResourcePatch{Shifts: half}, monday, idem("shift")); err != nil {
		t.Fatal(err)
	}
	assertMaterialized(t, ctx, s, admin, 1)

	if _, _, err := s.DeleteOperation(ctx, c.ID, c.Version, idem("dc")); err != nil {
		t.Fatal(err)
	}
	assertMaterialized(t, ctx, s, admin, 1)
	if _, _, err := s.DeleteOperation(ctx, a.ID, a.Version, idem("da")); err != nil {
		t.Fatal(err)
	}
	assertMaterialized(t, ctx, s, admin, 1)
}

func loadRows(t *testing.T, admin *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from scheduling_capacity_load`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func moveTo(ctx context.Context, s *store.Postgres, jobID, resourceID string, start time.Time) (store.MoveOutcome, bool, bool, error) {
	out, replayed, err := s.MoveJob(ctx, store.MoveRequest{JobID: jobID, ResourceID: resourceID, Start: start, HorizonDays: 60}, idem("mv-"+jobID))
	return out, replayed, true, err
}

// A bottleneck whose weeks pass with no change to the resource is over: it no
// longer reads as open, and a later overload of the same resource is a new
// episode with its own bottleneck.detected instead of being absorbed by the
// stale one.
func TestBottleneckEpisode_PassedWeeksEndTheEpisode(t *testing.T) {
	s, _, admin := setup(t)
	s.WithBottleneck(90, 2).WithClock(func() time.Time { return monday.AddDate(0, 0, -20) })
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	for i := 0; i < 2; i++ {
		mustOperation(t, ctx, s, ids.New(), 10, 2300, m.ID)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if n := outboxCount(t, admin, bottleneckType); n != 1 || len(openEpisodes(t, s, ctx)) != 1 {
		t.Fatalf("first overload: %d events, want 1 and one open episode", n)
	}

	// Nine weeks on, nothing touched the resource: weeks 0 and 1 are history.
	s.WithClock(func() time.Time { return monday.AddDate(0, 0, 63) })
	if open := openEpisodes(t, s, ctx); len(open) != 0 {
		t.Fatalf("an episode whose weeks have all passed still reads as open: %+v", open)
	}
	closed, err := s.ListBottlenecks(ctx, store.BottleneckFilter{Status: "closed"})
	if err != nil || len(closed) != 1 || closed[0].ClosedAt == nil || !closed[0].ClosedAt.Equal(monday.AddDate(0, 0, 14)) {
		t.Fatalf("closed = %+v err=%v, want the episode closed at the end of its last flagged week", closed, err)
	}

	// The same resource overloaded again from the current week on.
	for i := 0; i < 2; i++ {
		mustOperation(t, ctx, s, ids.New(), 10, 2300, m.ID)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(63, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if n := outboxCount(t, admin, bottleneckType); n != 2 {
		t.Fatalf("a new overload after the old one passed: %d events, want 2", n)
	}
	open := openEpisodes(t, s, ctx)
	if len(open) != 1 || open[0].FirstWeek != monday.AddDate(0, 0, 63).Format(time.DateOnly) {
		t.Fatalf("open = %+v, want one new episode from week 9", open)
	}
	var payload string
	if err := admin.QueryRow(context.Background(), `select (payload->'data')::text from outbox where type = $1 order by created_at desc limit 1`, bottleneckType).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"resourceCode": "M1"`, `"periods"`, `"weekStart": "` + monday.AddDate(0, 0, 63).Format(time.DateOnly) + `"`} {
		if !strings.Contains(payload, want) {
			t.Errorf("event payload %s lacks %s", payload, want)
		}
	}
}

// The rewrite at the end of a writer's transaction must not wait on a lock
// another transaction holds on the resource row: UpdateResource holds it FOR
// UPDATE and then waits for the resource's load lock, so a writer that held
// the load lock and waited on the row would deadlock with it.
func TestCapacityLoad_RewriteDoesNotWaitOnTheResourceRow(t *testing.T) {
	s, _, admin := setup(t)
	s.WithClock(func() time.Time { return monday.AddDate(0, 0, -20) })
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	o1, o2 := ids.New(), ids.New()
	mustOperation(t, ctx, s, o1, 10, 600, m.ID)
	mustOperation(t, ctx, s, o2, 10, 600, m.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	tx, err := admin.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `select 1 from scheduling_resource where id = $1::uuid for update`, m.ID); err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, _, err := s.UnscheduleOrder(cctx, o2, idem("u-locked")); err != nil {
		t.Fatalf("unschedule while the resource row is locked: %v", err)
	}
}

// Runs over two resources whose operations book them as primary and secondary
// in opposite roles, calendar changes on each resource and overrides, all at
// once: none deadlocks, and the table ends equal to the load on read (a
// rewrite reads the calendar only after it holds the resource's lock).
func TestCapacityLoad_ConcurrentWritersNeitherDeadlockNorGoStale(t *testing.T) {
	s, _, admin := setup(t)
	s.WithBottleneck(90, 2).WithClock(func() time.Time { return monday.AddDate(0, 0, -20) })
	ctx := tenantCtx(1)
	m1, m2 := mustResource(t, ctx, s, "M1", true), mustResource(t, ctx, s, "M2", true)
	secOperation(t, ctx, s, 10, 600, m1.ID, m2.ID)
	secOperation(t, ctx, s, 10, 600, m2.ID, m1.ID)
	mustOperation(t, ctx, s, ids.New(), 10, 900, m1.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	const rounds = 6
	errs := make(chan error, 4*rounds)
	var wg sync.WaitGroup
	reshift := func(r store.Resource) {
		defer wg.Done()
		version := r.Version
		for i := 0; i < rounds; i++ {
			upd, _, err := s.UpdateResource(ctx, r.ID, version, store.ResourcePatch{Shifts: weekdays((7+i%2)*60, 16*60)}, monday, idem(fmt.Sprintf("sh-%s-%d", r.Code, i)))
			if err != nil {
				errs <- fmt.Errorf("update %s: %w", r.Code, err)
				return
			}
			version = upd.Version
		}
	}
	wg.Add(4)
	go reshift(m1)
	go reshift(m2)
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			if _, _, err := s.RunSchedule(ctx, siteRun(at(i%2, 6, 0)), store.Idem{}); err != nil {
				errs <- fmt.Errorf("run: %w", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < rounds; i++ {
			r := []string{m2.ID, m1.ID}[i%2]
			o, _, err := s.CreateCapacityOverride(ctx, store.CapacityOverride{ResourceID: r, Kind: domain.OverrideDowntime, StartAt: at(2, 8, 0), EndAt: at(2, 10, 0)}, store.Idem{})
			if err == nil {
				err = s.DeleteCapacityOverride(ctx, r, o.ID)
			}
			if err != nil {
				errs <- fmt.Errorf("override: %w", err)
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	assertMaterialized(t, ctx, s, admin, 1)
}
