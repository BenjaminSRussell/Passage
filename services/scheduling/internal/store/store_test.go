package store_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/testdb"
)

// monday is 2027-01-04 00:00 UTC, a Monday in the future.
var monday = time.Date(2027, 1, 4, 0, 0, 0, 0, time.UTC)

func at(day, hh, mm int) time.Time {
	return monday.AddDate(0, 0, day).Add(time.Duration(hh)*time.Hour + time.Duration(mm)*time.Minute)
}

// setup migrates a throwaway schema as the owner and returns a store on the
// application role (NOSUPERUSER, NOBYPASSRLS), so every query runs under RLS,
// plus the owner pool for cross-tenant assertions.
func setup(t *testing.T) (*store.Postgres, *pgxpool.Pool, *pgxpool.Pool) {
	t.Helper()
	admin, app := testkit.DBWithApp(t)
	testdb.Migrate(t, admin)
	return store.New(app), app, admin
}

func tenantCtx(n int) context.Context {
	return tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(n), UserID: ids.New(), Duties: map[string]struct{}{}})
}

func weekdays(start, end int) []domain.Shift {
	var out []domain.Shift
	for d := time.Monday; d <= time.Friday; d++ {
		out = append(out, domain.Shift{Weekday: d, StartMinute: start, EndMinute: end})
	}
	return out
}

func mustResource(t *testing.T, ctx context.Context, s *store.Postgres, code string, finite bool) store.Resource {
	t.Helper()
	r, created, err := s.CreateResource(ctx, store.Resource{Code: code, Name: code, Finite: finite, TimeZone: "UTC", Shifts: weekdays(8*60, 16*60)}, store.Idem{})
	if err != nil || !created {
		t.Fatalf("create resource %s: created=%v err=%v", code, created, err)
	}
	return r
}

func mustOperation(t *testing.T, ctx context.Context, s *store.Postgres, orderID string, oper, minutes int, resources ...string) store.Operation {
	t.Helper()
	op, _, err := s.CreateOperation(ctx, store.Operation{OrderID: orderID, OperNumber: oper, Minutes: minutes, Priority: 100, ResourceIDs: resources}, store.Idem{})
	if err != nil {
		t.Fatalf("create operation %d: %v", oper, err)
	}
	return op
}

func siteRun(start time.Time) store.RunRequest {
	return store.RunRequest{Scope: "site", Mode: "jobs", Finite: true, Start: start, HorizonDays: 60}
}

func barsOf(t *testing.T, ctx context.Context, s *store.Postgres) map[string]store.Bar {
	t.Helper()
	bars, err := s.Gantt(ctx, monday.AddDate(0, 0, -1), monday.AddDate(0, 0, 30), nil)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.Bar{}
	for _, b := range bars {
		if !b.Secondary && !b.Cleanout {
			out[b.JobID] = b
		}
	}
	return out
}

func outboxCount(t *testing.T, admin *pgxpool.Pool, typ string) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, typ).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

func TestRunSchedule_PlacesJobsAndCommitsEventsTogether(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	order := ids.New()
	op1 := mustOperation(t, ctx, s, order, 10, 60, r.ID)
	op2 := mustOperation(t, ctx, s, order, 20, 90, r.ID)

	run, created, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{Key: "run-1", Fingerprint: "fp"})
	if err != nil || !created {
		t.Fatalf("run: created=%v err=%v", created, err)
	}
	if run.OperationsCount != 2 || run.MakespanMinutes != 150 || len(run.Messages) != 0 {
		t.Fatalf("run = %+v, want 2 operations, makespan 150", run)
	}
	bars := barsOf(t, ctx, s)
	if !bars[op1.ID].Start.Equal(at(0, 8, 0)) || !bars[op2.ID].Start.Equal(at(0, 9, 0)) || !bars[op2.ID].End.Equal(at(0, 10, 30)) {
		t.Fatalf("bars = %+v, want op1 08:00-09:00 then op2 09:00-10:30", bars)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.job.scheduled.v1"); got != 2 {
		t.Fatalf("job.scheduled events = %d, want 2", got)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.orderdates.changed.v1"); got != 1 {
		t.Fatalf("orderdates.changed events = %d, want 1", got)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.run.completed.v1"); got != 1 {
		t.Fatalf("run.completed events = %d, want 1", got)
	}
	ops, err := s.ListOperations(ctx, store.OperationFilter{Status: "scheduled", Limit: 10})
	if err != nil || len(ops) != 2 {
		t.Fatalf("scheduled operations = %+v err=%v", ops, err)
	}

	again, created, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{Key: "run-1", Fingerprint: "fp"})
	if err != nil || created || again.ID != run.ID {
		t.Fatalf("replay: run=%s created=%v err=%v, want %s replayed", again.ID, created, err, run.ID)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{Key: "run-1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key with another body: err=%v", err)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.run.completed.v1"); got != 1 {
		t.Fatalf("replays must not publish: run.completed events = %d", got)
	}
}

// Regression: the draft re-scheduled firmed operations too, so the second run
// inserted a duplicate (job_id, segment_no) row and failed with 409.
func TestRunSchedule_KeepsFirmedJobAndHoldsBackItsSuccessor(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	order := ids.New()
	op1 := mustOperation(t, ctx, s, order, 10, 60, r.ID)
	op2 := mustOperation(t, ctx, s, order, 20, 60, r.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: op1.ID, Start: at(1, 10, 0), HorizonDays: 60}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FirmJob(ctx, op1.ID, true, store.Idem{Key: "firm-1", Fingerprint: "fp"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatalf("second run with a firmed job: %v", err)
	}
	bars := barsOf(t, ctx, s)
	if !bars[op1.ID].Start.Equal(at(1, 10, 0)) || !bars[op1.ID].Firmed {
		t.Fatalf("firmed op1 = %+v, want kept at Tuesday 10:00", bars[op1.ID])
	}
	if !bars[op2.ID].Start.Equal(at(1, 11, 0)) {
		t.Fatalf("op2 = %+v, want Tuesday 11:00 after the firmed predecessor", bars[op2.ID])
	}
	if _, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: op1.ID, Start: at(2, 8, 0), HorizonDays: 60}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	out, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: op1.ID, Start: at(2, 8, 0), HorizonDays: 60, DryRun: true}, store.Idem{})
	if err != nil || out.Conflict == nil || out.Conflict.Code != domain.CodeFirmed {
		t.Fatalf("moving a firmed job: outcome=%+v err=%v, want %s", out, err, domain.CodeFirmed)
	}
}

func TestMoveJob_PreviewWritesNothingAndCommitPropagates(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	mix := mustResource(t, ctx, s, "MIX-1", true)
	fill := mustResource(t, ctx, s, "FILL-1", true)
	order := ids.New()
	op1 := mustOperation(t, ctx, s, order, 10, 60, mix.ID)
	op2 := mustOperation(t, ctx, s, order, 20, 60, fill.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	datesBefore := outboxCount(t, admin, "dev.passage.scheduling.orderdates.changed.v1")
	req := store.MoveRequest{JobID: op1.ID, Start: at(0, 14, 0), HorizonDays: 60, DryRun: true}
	preview, _, err := s.MoveJob(ctx, req, store.Idem{})
	if err != nil || preview.Conflict != nil || len(preview.Changes) != 2 || !preview.OrderEnd.Equal(at(0, 16, 0)) {
		t.Fatalf("preview = %+v err=%v, want both jobs moving and order ending 16:00", preview, err)
	}
	if bars := barsOf(t, ctx, s); !bars[op1.ID].Start.Equal(at(0, 8, 0)) {
		t.Fatalf("preview changed the schedule: %+v", bars[op1.ID])
	}

	req.DryRun = false
	idem := store.Idem{Key: "move-1", Fingerprint: "fp"}
	committed, replayed, err := s.MoveJob(ctx, req, idem)
	if err != nil || replayed || committed.Run == nil {
		t.Fatalf("commit = %+v replayed=%v err=%v", committed, replayed, err)
	}
	bars := barsOf(t, ctx, s)
	if !bars[op1.ID].Start.Equal(at(0, 14, 0)) || !bars[op2.ID].Start.Equal(at(0, 15, 0)) {
		t.Fatalf("after commit op1=%+v op2=%+v, want 14:00 and 15:00", bars[op1.ID], bars[op2.ID])
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.orderdates.changed.v1"); got != datesBefore+1 {
		t.Fatalf("orderdates.changed events = %d, want %d", got, datesBefore+1)
	}
	again, replayed, err := s.MoveJob(ctx, req, idem)
	if err != nil || !replayed || again.Run == nil || again.Run.ID != committed.Run.ID {
		t.Fatalf("replay = %+v replayed=%v err=%v", again, replayed, err)
	}
	if _, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: op1.ID, Start: at(0, 9, 0), HorizonDays: 60}, store.Idem{Key: "move-1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key: err=%v", err)
	}
}

func TestMoveJob_ConflictsAndUnknownJob(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	a := mustOperation(t, ctx, s, ids.New(), 10, 60, r.ID)
	b := mustOperation(t, ctx, s, ids.New(), 10, 60, r.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	out, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: b.ID, Start: bars[a.ID].Start, HorizonDays: 60}, store.Idem{Key: "k", Fingerprint: "f"})
	if err != nil || out.Conflict == nil || out.Conflict.Code != domain.CodeOverlap || out.Conflict.JobID != a.ID {
		t.Fatalf("move onto a: outcome=%+v err=%v", out, err)
	}
	out, _, err = s.MoveJob(ctx, store.MoveRequest{JobID: b.ID, Start: at(0, 20, 0), HorizonDays: 60}, store.Idem{})
	if err != nil || out.Conflict == nil || out.Conflict.Code != domain.CodeOutsideCapacity {
		t.Fatalf("move into closed time: outcome=%+v err=%v", out, err)
	}
	if _, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: ids.New(), Start: at(0, 9, 0), HorizonDays: 60}, store.Idem{}); problemCode(err) != store.CodeJobNotFound {
		t.Fatalf("unknown job: err=%v", err)
	}
}

func TestExclusionConstraint_RefusesDoubleBookingOnFiniteResource(t *testing.T) {
	s, app, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	order := ids.New()
	a := mustOperation(t, ctx, s, order, 10, 60, r.ID)
	b := mustOperation(t, ctx, s, order, 20, 60, r.ID)
	run, _, err := s.RunSchedule(ctx, store.RunRequest{Scope: "selection", Mode: "jobs", Finite: true, OperationIDs: []string{ids.New()}, Start: at(0, 6, 0), HorizonDays: 60}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	insert := func(tx pgx.Tx, job string, finite bool) error {
		_, err := tx.Exec(ctx, `insert into scheduled_job (tenant_id, id, job_id, order_id, oper_number, resource_id, segment_no, start_at, end_at, minutes, run_id, finite)
			values ($1, $2, $3, $4, 10, $5, 1, $6, $7, 60, $8, $9)`, testkit.TenantID(1), ids.New(), job, order, r.ID, at(0, 8, 0), at(0, 9, 0), run.ID, finite)
		return err
	}
	err = dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		if err := insert(tx, a.ID, true); err != nil {
			return err
		}
		return insert(tx, b.ID, true)
	})
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23P01" {
		t.Fatalf("overlapping finite rows: err=%v, want exclusion violation 23P01", err)
	}
	if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
		if err := insert(tx, a.ID, false); err != nil {
			return err
		}
		return insert(tx, b.ID, false)
	}); err != nil {
		t.Fatalf("overlapping infinite-run rows must be allowed: %v", err)
	}
}

func TestRunSchedule_InfiniteRunOverlapsButCountsLoad(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	a := mustOperation(t, ctx, s, ids.New(), 10, 480, r.ID)
	b := mustOperation(t, ctx, s, ids.New(), 10, 480, r.ID)
	req := siteRun(at(0, 6, 0))
	req.Finite = false
	if _, _, err := s.RunSchedule(ctx, req, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	if !bars[a.ID].Start.Equal(at(0, 8, 0)) || !bars[b.ID].Start.Equal(at(0, 8, 0)) {
		t.Fatalf("infinite run: a=%+v b=%+v, want both Monday 08:00", bars[a.ID], bars[b.ID])
	}
	loads, err := s.CapacityLoad(ctx, store.LoadFilter{ResourceIDs: []string{r.ID}, From: at(2, 0, 0), Weeks: 1, ThresholdPct: 90, Consecutive: 1})
	if err != nil || len(loads) != 1 {
		t.Fatalf("loads = %+v err=%v", loads, err)
	}
	// Reconciliation (PRD 4.23.9): booked minutes equal the scheduled job minutes.
	if l := loads[0]; l.OpenMinutes != 5*480 || l.BookedMinutes != 960 || !l.WeekStart.Equal(monday) {
		t.Fatalf("load = %+v, want open 2400, booked 960, week of %s", l, monday)
	}
}

func TestTenantIsolation(t *testing.T) {
	s, _, _ := setup(t)
	one, two := tenantCtx(1), tenantCtx(2)
	r := mustResource(t, one, s, "MIX-1", true)
	mustOperation(t, one, s, ids.New(), 10, 60, r.ID)
	if _, _, err := s.RunSchedule(one, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if rs, err := s.ListResources(two); err != nil || len(rs) != 0 {
		t.Fatalf("tenant 2 sees resources %+v err=%v", rs, err)
	}
	if bars := barsOf(t, two, s); len(bars) != 0 {
		t.Fatalf("tenant 2 sees bars %+v", bars)
	}
	if _, err := s.GetResource(two, r.ID); problemCode(err) != store.CodeResourceNotFound {
		t.Fatalf("tenant 2 reads tenant 1 resource: err=%v", err)
	}
	if _, _, err := s.CreateOperation(two, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 5, ResourceIDs: []string{r.ID}}, store.Idem{}); problemCode(err) != store.CodeUnknownResource {
		t.Fatalf("tenant 2 schedules on tenant 1 resource: err=%v", err)
	}
	// The same resource code is free in another tenant.
	mustResource(t, two, s, "MIX-1", true)
	if _, _, err := s.CreateResource(one, store.Resource{Code: "MIX-1", Name: "dup", Finite: true, TimeZone: "UTC", Shifts: weekdays(0, 60)}, store.Idem{}); problemCode(err) != store.CodeResourceDuplicate {
		t.Fatalf("duplicate code in tenant 1: err=%v", err)
	}
	var p *httpx.Problem
	if _, err := s.GetRun(one, ids.New()); !errors.As(err, &p) || p.Status != http.StatusNotFound {
		t.Fatalf("unknown run: err=%v", err)
	}
}

func TestResourceCapacity_ClipsToWindow(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	ivs, err := s.ResourceCapacity(ctx, r.ID, at(0, 12, 0), at(2, 0, 0))
	if err != nil || len(ivs) != 2 {
		t.Fatalf("intervals = %+v err=%v", ivs, err)
	}
	if !ivs[0].Start.Equal(at(0, 12, 0)) || ivs[0].Minutes() != 240 || ivs[1].Minutes() != 480 {
		t.Fatalf("intervals = %+v, want Monday 12:00-16:00 and Tuesday 08:00-16:00", ivs)
	}
}

func TestMigration_EveryTableHasForcedRLSAndATenantPolicy(t *testing.T) {
	_, _, admin := setup(t)
	rows, err := admin.Query(context.Background(), `select c.relname, c.relrowsecurity, c.relforcerowsecurity,
			exists(select 1 from pg_policy p where p.polrelid = c.oid and p.polname = c.relname || '_tenant_access'),
			exists(select 1 from pg_attribute a where a.attrelid = c.oid and a.attname = 'tenant_id' and a.attnotnull)
		from pg_class c where c.relkind = 'r' and c.relnamespace = current_schema()::regnamespace and c.relname <> 'schema_migrations'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name string
		var rls, force, policy, tenantCol bool
		if err := rows.Scan(&name, &rls, &force, &policy, &tenantCol); err != nil {
			t.Fatal(err)
		}
		seen++
		if !rls || !force || !policy || !tenantCol {
			t.Fatalf("%s: rls=%v force=%v tenant policy=%v tenant_id not null=%v", name, rls, force, policy, tenantCol)
		}
	}
	if seen < 14 {
		t.Fatalf("checked %d tables, want at least 14", seen)
	}
}
