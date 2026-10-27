package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
	"github.com/BenjaminSRussell/passage/services/shopfloor/migrations"
)

// setup needs PASSAGE_TEST_DATABASE_URL (testkit skips without it) and the
// application-role URL: the repository runs as passage_app, so row-level
// security is enforced exactly as in production. admin is for assertions.
func setup(t *testing.T) (s *Postgres, app, admin *pgxpool.Pool) {
	t.Helper()
	admin, app = testkit.DBWithApp(t)
	if err := dbx.Migrate(context.Background(), admin, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return New(app), app, admin
}

// inTenant runs fn in a transaction with the tenant GUC set, the way the service does.
func inTenant(t *testing.T, pool *pgxpool.Pool, tenantID string, fn func(tx pgx.Tx) error) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true)`, tenantID); err != nil {
		t.Fatal(err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func idem(key string) Idem { return Idem{Key: key, Fingerprint: "fp-" + key} }

func mustWorker(t *testing.T, s *Postgres, ctx context.Context, badge string, userID *string) Worker {
	t.Helper()
	w, err := s.CreateWorker(ctx, Worker{PartyID: ids.New(), Name: "Worker " + badge, BadgeID: &badge, UserID: userID,
		CostRate: decimal.NewFromInt(20), Currency: "USD"}, idem("worker-"+badge+ids.New()))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func countEvents(t *testing.T, pool *pgxpool.Pool, tenantID, eventType string) int {
	t.Helper()
	var n int
	inTenant(t, pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select count(*) from outbox where type = $1`, eventType).Scan(&n)
	})
	return n
}

func day(h, m int) time.Time { return time.Date(2026, 10, 20, h, m, 0, 0, time.UTC) }

func clock(worker, kind string, at time.Time) Registration {
	return Registration{ID: ids.New(), WorkerID: worker, Kind: kind, StartedAt: at}
}

// calculate builds and saves the pay of one UTC day the way the API does.
func calculate(t *testing.T, s *Postgres, ctx context.Context, worker, date string, key Idem) error {
	t.Helper()
	d, _ := time.Parse("2006-01-02", date)
	in, err := s.LoadPunches(ctx, worker, d, d.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	calc := domain.Calculation{Profile: domain.DefaultProfile(), Location: time.UTC, Rules: domain.DefaultRules(), Rate: in.Rate, Currency: in.Currency}
	days, err := calc.Run(domain.BuildDays(in.Punches, time.UTC, calc.Profile))
	if err != nil {
		return err
	}
	return s.SavePay(ctx, PayCalculation{WorkerID: worker, FromDate: date, ToDate: date, TimeZone: "UTC", Days: days}, key, map[string]string{"ok": date})
}

func problemCode(err error) string {
	var p *httpx.Problem
	if errors.As(err, &p) {
		return p.Code
	}
	return ""
}

// AC-25.2 via the database: a 09:00-19:30 clock pair on a Tuesday is 8.00 base and
// 2.50 overtime-1.5; approval emits payday.approved once; a recalculation is refused.
func TestPayRoundTrip_CalculateApproveAndLock(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(7)
	ctx := testkit.Ctx(tenantID)
	worker := mustWorker(t, s, ctx, "B-1", nil).ID

	regs := []Registration{clock(worker, "clock-in", day(9, 0)), clock(worker, "clock-out", day(19, 30))}
	res, err := s.CreateRegistrations(ctx, regs)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Status != StatusAccepted {
			t.Fatalf("first post status %s", r.Status)
		}
	}
	replay, err := s.CreateRegistrations(ctx, regs[:1])
	if err != nil {
		t.Fatal(err)
	}
	if replay[0].Status != StatusDuplicate {
		t.Fatalf("replay status %s, want duplicate", replay[0].Status)
	}

	if err := calculate(t, s, ctx, worker, "2026-10-20", idem("calc-1")); err != nil {
		t.Fatal(err)
	}
	approved, err := s.ApprovePay(ctx, worker, "2026-10-20", "2026-10-20", "supervisor-1", idem("approve-1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(approved.Days) != 1 || !approved.Days[0].NewlyApproved || len(approved.Days[0].PayLines) != 2 {
		t.Fatalf("approved = %+v", approved)
	}
	if got := approved.Days[0].PayLines[1].Amount.String(); got != "75" {
		t.Fatalf("overtime amount %s, want 75", got)
	}

	again, err := s.ApprovePay(ctx, worker, "2026-10-20", "2026-10-20", "supervisor-1", Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if again.Days[0].NewlyApproved {
		t.Fatal("repeated approval reported as new")
	}
	if err := calculate(t, s, ctx, worker, "2026-10-20", Idem{}); problemCode(err) != CodeDayApproved {
		t.Fatalf("recalculating an approved day: %v, want %s", err, CodeDayApproved)
	}
	if n := countEvents(t, app, tenantID, sfevents.RegistrationPosted); n != 2 {
		t.Fatalf("registration.posted rows = %d, want 2", n)
	}
	if n := countEvents(t, app, tenantID, sfevents.PaydayApproved); n != 1 {
		t.Fatalf("payday.approved rows = %d, want 1", n)
	}
}

// Idempotency-Key: the stored outcome replays, and a different request under
// the same key is refused.
func TestIdempotency_StoredOutcomeAndReuse(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(11))
	worker := mustWorker(t, s, ctx, "B-2", nil).ID
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(worker, "clock-in", day(8, 0)), clock(worker, "clock-out", day(12, 0))}); err != nil {
		t.Fatal(err)
	}
	if err := calculate(t, s, ctx, worker, "2026-10-20", idem("calc-x")); err != nil {
		t.Fatal(err)
	}
	rep, err := s.LookupReplay(ctx, idem("calc-x"))
	if err != nil || rep == nil || rep.Status != 200 || string(rep.Body) != `{"ok": "2026-10-20"}` {
		t.Fatalf("replay = %+v err %v", rep, err)
	}
	if _, err := s.LookupReplay(ctx, Idem{Key: "calc-x", Fingerprint: "other"}); problemCode(err) != CodeIdempotencyReuse {
		t.Fatalf("reuse: %v", err)
	}
	if rep, err := s.LookupReplay(testkit.Ctx(testkit.TenantID(12)), idem("calc-x")); err != nil || rep != nil {
		t.Fatalf("another tenant saw the key: %+v %v", rep, err)
	}
}

// The database refuses a pay line change on an approved day, even outside the service.
func TestPayLineTrigger_BlocksChangesToApprovedDay(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(8)
	ctx := testkit.Ctx(tenantID)
	worker := mustWorker(t, s, ctx, "B-3", nil).ID
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(worker, "clock-in", day(9, 0)), clock(worker, "clock-out", day(12, 0))}); err != nil {
		t.Fatal(err)
	}
	if err := calculate(t, s, ctx, worker, "2026-10-20", Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePay(ctx, worker, "2026-10-20", "2026-10-20", "supervisor-1", Idem{}); err != nil {
		t.Fatal(err)
	}
	tx, err := app.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(context.Background(), `select set_config('passage.tenant_id', $1, true)`, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `update pay_line set hours = 1 where worker_id = $1`, worker); err == nil {
		t.Fatal("update of an approved pay line succeeded")
	}
}

// An open flag blocks approval of the whole range (SF-0006).
func TestApprove_FlaggedDayIsRefused(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(9))
	worker := mustWorker(t, s, ctx, "B-4", nil).ID
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(worker, "clock-in", day(9, 0))}); err != nil {
		t.Fatal(err)
	}
	if err := calculate(t, s, ctx, worker, "2026-10-20", Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePay(ctx, worker, "2026-10-20", "2026-10-20", "supervisor-1", Idem{}); problemCode(err) != CodeDayFlagged {
		t.Fatalf("err = %v, want %s", err, CodeDayFlagged)
	}
}

// A registration that arrives after the calculation (an offline replay) makes
// the day stale: approval is refused until the day is recalculated (SF-0104).
func TestApprove_StaleDayNeedsRecalculation(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(13)
	ctx := testkit.Ctx(tenantID)
	worker := mustWorker(t, s, ctx, "B-5", nil).ID
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(worker, "clock-in", day(7, 0)), clock(worker, "clock-out", day(11, 0))}); err != nil {
		t.Fatal(err)
	}
	if err := calculate(t, s, ctx, worker, "2026-10-20", Idem{}); err != nil {
		t.Fatal(err)
	}
	// Move the calculation into the past so the late registrations are newer.
	if _, err := admin.Exec(context.Background(), `update worker_day set calculated_at = now() - interval '1 minute' where worker_id = $1`, worker); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(worker, "clock-in", day(12, 0)), clock(worker, "clock-out", day(16, 0))}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePay(ctx, worker, "2026-10-20", "2026-10-20", "supervisor-1", Idem{}); problemCode(err) != CodeDayStale {
		t.Fatalf("err = %v, want %s", err, CodeDayStale)
	}
	if err := calculate(t, s, ctx, worker, "2026-10-20", Idem{}); err != nil {
		t.Fatal(err)
	}
	out, err := s.ApprovePay(ctx, worker, "2026-10-20", "2026-10-20", "supervisor-1", Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Days[0].Hours.StringFixed(2) != "8.00" {
		t.Fatalf("approved hours %s, want 8.00", out.Days[0].Hours)
	}
}

// Segregation of duties: a worker cannot approve their own day (SF-0106).
func TestApprove_OwnDayIsRefused(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(14))
	user := "supervisor-1"
	worker := mustWorker(t, s, ctx, "B-6", &user).ID
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(worker, "clock-in", day(9, 0)), clock(worker, "clock-out", day(12, 0))}); err != nil {
		t.Fatal(err)
	}
	if err := calculate(t, s, ctx, worker, "2026-10-20", Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePay(ctx, worker, "2026-10-20", "2026-10-20", user, Idem{}); problemCode(err) != CodeOwnApproval {
		t.Fatalf("err = %v, want %s", err, CodeOwnApproval)
	}
}

func TestRegistration_UnknownWorkerIsSF0101(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(10))
	_, err := s.CreateRegistrations(ctx, []Registration{clock(ids.New(), "clock-in", day(9, 0))})
	if problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("err = %v, want %s", err, CodeWorkerNotFound)
	}
}

// Workers: a badge is unique per tenant, and each tenant sees only its own rows.
func TestWorkers_BadgeUniqueAndTenantIsolation(t *testing.T) {
	s, _, _ := setup(t)
	a, b := testkit.Ctx(testkit.TenantID(21)), testkit.Ctx(testkit.TenantID(22))
	w := mustWorker(t, s, a, "B-7", nil)
	badge := "B-7"
	if _, err := s.CreateWorker(a, Worker{PartyID: ids.New(), Name: "Dup", BadgeID: &badge, CostRate: decimal.NewFromInt(1), Currency: "USD"}, idem("dup")); problemCode(err) != CodeBadgeTaken {
		t.Fatalf("duplicate badge: %v", err)
	}
	// The same badge in another tenant is fine.
	mustWorker(t, s, b, "B-7", nil)

	listA, err := s.ListWorkers(a, false, 100)
	if err != nil || len(listA) != 1 || listA[0].ID != w.ID {
		t.Fatalf("tenant A workers = %+v err %v", listA, err)
	}
	if _, err := s.GetWorker(b, w.ID); problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("tenant B read tenant A's worker: %v", err)
	}
	if _, err := s.CreateRegistrations(b, []Registration{clock(w.ID, "clock-in", day(9, 0))}); problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("tenant B registered time for tenant A's worker: %v", err)
	}
}

// The approval list returns days with their pay lines, filtered by status.
func TestListWorkerDaysAndRegistrations(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(15))
	w1 := mustWorker(t, s, ctx, "B-8", nil).ID
	w2 := mustWorker(t, s, ctx, "B-9", nil).ID
	stop := day(10, 30)
	hours := decimal.RequireFromString("1.5")
	job := ids.New()
	reason := "SCR-1"
	regs := []Registration{
		clock(w1, "clock-in", day(9, 0)), clock(w1, "clock-out", day(19, 30)),
		clock(w2, "clock-in", day(9, 0)),
		{ID: ids.New(), WorkerID: w1, Kind: "job-stop", JobID: &job, StartedAt: day(9, 0), StoppedAt: &stop, Hours: &hours,
			QtyGood: decimal.NewFromInt(10), QtyScrap: decimal.NewFromInt(1), ScrapReasonID: &reason},
	}
	if _, err := s.CreateRegistrations(ctx, regs); err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{w1, w2} {
		if err := calculate(t, s, ctx, w, "2026-10-20", Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	days, err := s.ListWorkerDays(ctx, DayFilter{FromDate: "2026-10-20", ToDate: "2026-10-20", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 {
		t.Fatalf("days = %+v", days)
	}
	byWorker := map[string]WorkerDay{}
	for _, d := range days {
		byWorker[d.WorkerID] = d
	}
	if d := byWorker[w1]; len(d.PayLines) != 2 || d.Hours.StringFixed(2) != "10.50" || len(d.Flags) != 0 {
		t.Fatalf("worker 1 day = %+v", d)
	}
	if d := byWorker[w2]; len(d.Flags) != 1 || d.Flags[0] != domain.FlagMissingClockOut {
		t.Fatalf("worker 2 day = %+v", d)
	}
	if _, err := s.ApprovePay(ctx, w1, "2026-10-20", "2026-10-20", "supervisor-1", Idem{}); err != nil {
		t.Fatal(err)
	}
	pending, err := s.ListWorkerDays(ctx, DayFilter{Status: "calculated", FromDate: "2026-10-20", ToDate: "2026-10-20", Limit: 100})
	if err != nil || len(pending) != 1 || pending[0].WorkerID != w2 {
		t.Fatalf("pending = %+v err %v", pending, err)
	}

	list, err := s.ListRegistrations(ctx, w1, day(0, 0), day(0, 0).AddDate(0, 0, 1), 100)
	if err != nil || len(list) != 3 {
		t.Fatalf("registrations = %+v err %v", list, err)
	}
	var jobStop *RegistrationView
	for i := range list {
		if list[i].Kind == "job-stop" {
			jobStop = &list[i]
		}
	}
	if jobStop == nil || jobStop.Hours == nil || jobStop.Hours.StringFixed(4) != "1.5000" || jobStop.QtyScrap.String() != "1" {
		t.Fatalf("job-stop = %+v", jobStop)
	}
}
