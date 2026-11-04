package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

func input(name, badge, rate string) WorkerInput {
	var b *string
	if badge != "" {
		b = &badge
	}
	return WorkerInput{Name: name, BadgeID: b, CostRate: decimal.RequireFromString(rate), Currency: "USD"}
}

// D2a': a worker is edited under If-Match, a taken badge is SF-0105, the party link and the
// pay setup stay as they were, and each change publishes worker.saved.
func TestUpdateWorker_VersionBadgeAndEvent(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(40)
	ctx := testkit.Ctx(tenantID)
	a := mustWorker(t, s, ctx, "UPD-A", nil)
	b := mustWorker(t, s, ctx, "UPD-B", nil)

	if _, err := s.UpdateWorker(ctx, a.ID, 9, input("Ann", "UPD-A", "21"), "sup", idem("u-0")); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("stale version = %v, want %s", err, CodeVersionMismatch)
	}
	if _, err := s.UpdateWorker(ctx, ids.New(), 1, input("Ann", "X", "21"), "sup", idem("u-1")); problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("unknown worker = %v", err)
	}
	if _, err := s.UpdateWorker(ctx, a.ID, 1, input("Ann", "UPD-B", "21"), "sup", idem("u-2")); problemCode(err) != CodeBadgeTaken {
		t.Fatalf("badge of another worker = %v, want %s", err, CodeBadgeTaken)
	}
	user := "ann@plant"
	in := input("Ann Lee", "UPD-A2", "22.5")
	in.UserID = &user
	got, err := s.UpdateWorker(ctx, a.ID, 1, in, "sup", idem("u-3"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ann Lee" || got.Version != 2 || got.PartyID != a.PartyID || !got.CostRate.Equal(decimal.RequireFromString("22.5")) ||
		got.BadgeID == nil || *got.BadgeID != "UPD-A2" || got.UserID == nil || *got.UserID != user || !got.Active {
		t.Fatalf("updated worker = %+v", got)
	}
	// The old badge is free again and the new one signs in.
	if _, err := s.BadgeLogin(ctx, "UPD-A"); problemCode(err) != CodeBadgeUnknown {
		t.Fatalf("old badge = %v", err)
	}
	if sess, err := s.BadgeLogin(ctx, "UPD-A2"); err != nil || sess.WorkerID != a.ID {
		t.Fatalf("new badge = %+v, %v", sess, err)
	}
	// A cleared badge (null) is allowed; two workers may both have none.
	if _, err := s.UpdateWorker(ctx, a.ID, 2, input("Ann Lee", "", "22.5"), "sup", idem("u-4")); err != nil {
		t.Fatalf("clear badge: %v", err)
	}
	if _, err := s.UpdateWorker(ctx, b.ID, 1, input("Bo", "", "20"), "sup", idem("u-5")); err != nil {
		t.Fatalf("second worker without badge: %v", err)
	}
	if n := countEvents(t, app, tenantID, sfevents.WorkerSaved); n != 3 {
		t.Fatalf("worker.saved events = %d, want 3", n)
	}
}

// Deactivation needs a reason, is refused while the worker is clocked in or has an open job start
// (SF-0166), blocks the badge and new registrations, still lets the days already worked be
// calculated and approved, and reactivation brings the worker back.
func TestDeactivateWorker_ClockedInRefusedPayContinuesReactivate(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(41)
	ctx := testkit.Ctx(tenantID)
	w := mustWorker(t, s, ctx, "DEACT-1", nil)

	if _, err := s.CreateRegistrations(ctx, []Registration{clock(w.ID, "clock-in", day(9, 0))}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeactivateWorker(ctx, w.ID, 1, "left", "sup", idem("d-0")); problemCode(err) != CodeWorkerClockedIn {
		t.Fatalf("clocked in = %v, want %s", err, CodeWorkerClockedIn)
	}
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(w.ID, "clock-out", day(17, 0))}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeactivateWorker(ctx, w.ID, 7, "left", "sup", idem("d-1")); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("stale version = %v", err)
	}
	got, err := s.DeactivateWorker(ctx, w.ID, 1, "left the company", "sup-1", idem("d-2"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Active || got.Version != 2 || got.DeactivatedAt == nil || got.DeactivatedBy == nil || *got.DeactivatedBy != "sup-1" ||
		got.DeactivationReason == nil || *got.DeactivationReason != "left the company" {
		t.Fatalf("deactivated = %+v", got)
	}
	if _, err := s.DeactivateWorker(ctx, w.ID, 2, "again", "sup", idem("d-3")); problemCode(err) != CodeWorkerInactive {
		t.Fatalf("second deactivation = %v, want %s", err, CodeWorkerInactive)
	}
	if _, err := s.BadgeLogin(ctx, "DEACT-1"); problemCode(err) != CodeBadgeUnknown {
		t.Fatalf("badge of an inactive worker = %v", err)
	}
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(w.ID, "clock-in", day(9, 0).AddDate(0, 0, 1))}); problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("registration of an inactive worker = %v", err)
	}
	// The final pay of the days worked is calculated and approved after the worker left.
	if err := calculate(t, s, ctx, w.ID, "2026-10-20", idem("d-calc")); err != nil {
		t.Fatalf("calculate for an inactive worker: %v", err)
	}
	if res, err := s.ApprovePay(ctx, w.ID, "2026-10-20", "2026-10-20", "sup-2", idem("d-appr")); err != nil || len(res.Days) != 1 {
		t.Fatalf("approve for an inactive worker = %+v, %v", res, err)
	}
	// An inactive worker can be edited, and is hidden from the active list.
	if _, err := s.UpdateWorker(ctx, w.ID, 2, input("Worker DEACT-1", "", "20"), "sup", idem("d-4")); err != nil {
		t.Fatalf("edit inactive worker: %v", err)
	}
	if list, err := s.ListWorkers(ctx, true, 10); err != nil || len(list) != 0 {
		t.Fatalf("active list = %+v, %v", list, err)
	}

	if _, err := s.ReactivateWorker(ctx, w.ID, 1, "sup", idem("r-0")); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("stale reactivation = %v", err)
	}
	back, err := s.ReactivateWorker(ctx, w.ID, 3, "sup", idem("r-1"))
	if err != nil || !back.Active || back.DeactivatedAt != nil || back.DeactivationReason != nil || back.Version != 4 {
		t.Fatalf("reactivated = %+v, %v", back, err)
	}
	if _, err := s.ReactivateWorker(ctx, w.ID, 4, "sup", idem("r-2")); problemCode(err) != CodeWorkerActive {
		t.Fatalf("second reactivation = %v, want %s", err, CodeWorkerActive)
	}
	if n := countEvents(t, app, tenantID, sfevents.WorkerSaved); n != 3 {
		t.Fatalf("worker.saved events = %d, want 3 (deactivated, updated, reactivated)", n)
	}
}

// An open job start with no clock-in also blocks deactivation (SF-0166).
func TestDeactivateWorker_OpenJobStartRefused(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(42))
	w := mustWorker(t, s, ctx, "DEACT-2", nil)
	job := ids.New()
	if _, err := s.CreateRegistrations(ctx, []Registration{{ID: ids.New(), WorkerID: w.ID, Kind: "job-start", JobID: &job, StartedAt: day(8, 0)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeactivateWorker(ctx, w.ID, 1, "left", "sup", idem("o-0")); problemCode(err) != CodeWorkerClockedIn {
		t.Fatalf("open job start = %v, want %s", err, CodeWorkerClockedIn)
	}
}

// Editing the cost rate rewrites no pay: the approved day keeps its lines, approving it again is
// a no-op (the edit does not make it stale) and a recalculation of it is still SF-0102. The
// currency cannot change once a day is approved (SF-0169), and the worker's own user cannot edit
// the worker (SF-0106), which would let them raise their rate or unlink themselves to approve
// their own time.
func TestUpdateWorker_RateEditKeepsApprovedPayCurrencyLockedNotOwnRecord(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(43)
	ctx := testkit.Ctx(tenantID)
	own := "own-user"
	w := mustWorker(t, s, ctx, "RATE-1", &own)
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(w.ID, "clock-in", day(9, 0)), clock(w.ID, "clock-out", day(17, 0))}); err != nil {
		t.Fatal(err)
	}
	if err := calculate(t, s, ctx, w.ID, "2026-10-20", idem("r-calc")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePay(ctx, w.ID, "2026-10-20", "2026-10-20", "sup", idem("r-appr")); err != nil {
		t.Fatal(err)
	}
	total := func() string {
		var sum string
		inTenant(t, app, tenantID, func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), `select coalesce(sum(amount), 0)::text from pay_line where worker_id = $1`, w.ID).Scan(&sum)
		})
		return sum
	}
	before := total()

	if _, err := s.UpdateWorker(ctx, w.ID, 1, input("Ann", "RATE-1", "30"), own, idem("r-own")); problemCode(err) != CodeOwnApproval {
		t.Fatalf("own record = %v, want %s", err, CodeOwnApproval)
	}
	eur := input("Ann", "RATE-1", "30")
	eur.Currency = "EUR"
	eur.UserID = &own
	if _, err := s.UpdateWorker(ctx, w.ID, 1, eur, "sup", idem("r-eur")); problemCode(err) != CodeCurrencyLocked {
		t.Fatalf("currency change after approval = %v, want %s", err, CodeCurrencyLocked)
	}
	in := input("Ann", "RATE-1", "30")
	in.UserID = &own
	if _, err := s.UpdateWorker(ctx, w.ID, 1, in, "sup", idem("r-rate")); err != nil {
		t.Fatal(err)
	}
	if after := total(); after != before {
		t.Fatalf("approved pay changed with the rate edit: %s -> %s", before, after)
	}
	if res, err := s.ApprovePay(ctx, w.ID, "2026-10-20", "2026-10-20", "sup", idem("r-appr-2")); err != nil || len(res.Days) != 1 || res.Days[0].NewlyApproved {
		t.Fatalf("approve again after the rate edit = %+v, %v", res, err)
	}
	if err := calculate(t, s, ctx, w.ID, "2026-10-20", idem("r-calc-2")); problemCode(err) != CodeDayApproved {
		t.Fatalf("recalculating an approved day = %v, want %s", err, CodeDayApproved)
	}
}

// A punch that arrives while a deactivation holds the worker row waits for it and is then
// refused (SF-0101): without the shared lock the insert read the worker as still active and an
// inactive worker was left clocked in.
func TestCreateRegistrations_WaitsForDeactivation(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(44)
	ctx := testkit.Ctx(tenantID)
	w := mustWorker(t, s, ctx, "RACE-1", nil)

	bg := context.Background()
	tx, err := admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err := tx.Exec(bg, `select 1 from worker where id = $1 for update`, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(bg, `update worker set active = false, version = version + 1 where id = $1`, w.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.CreateRegistrations(ctx, []Registration{clock(w.ID, "clock-in", day(9, 0))})
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("the punch did not wait for the deactivation: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(bg); err != nil {
		t.Fatal(err)
	}
	if err := <-done; problemCode(err) != CodeWorkerNotFound {
		t.Fatalf("punch after the deactivation = %v, want %s", err, CodeWorkerNotFound)
	}
}
