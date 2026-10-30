package store

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// D2h: the sweep closes a clock-in left open longer than the threshold with a flagged
// clock-out at clock-in plus the threshold, leaves younger shifts and closed ones alone,
// is safe to repeat, publishes one registration.posted per clock-out, lists the result
// for review, and sees only its own tenant.
func TestAutoClockOut_ClosesStaleShiftsOnce(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(90)
	ctx := testkit.Ctx(tenantID)
	stale := mustWorker(t, s, ctx, "AC-STALE", nil)
	fresh := mustWorker(t, s, ctx, "AC-FRESH", nil)
	done := mustWorker(t, s, ctx, "AC-DONE", nil)
	if _, err := s.CreateRegistrations(ctx, []Registration{
		clock(stale.ID, "clock-in", day(6, 0)),
		clock(fresh.ID, "clock-in", day(20, 0)),
		clock(done.ID, "clock-in", day(6, 0)),
		clock(done.ID, "clock-out", day(14, 0)),
	}); err != nil {
		t.Fatal(err)
	}
	now := day(23, 0).Add(2 * time.Hour) // 01:00 the next day: stale is 19 h in, fresh 5 h
	closed, err := s.AutoClockOut(ctx, now, 16, 100)
	if err != nil || len(closed) != 1 {
		t.Fatalf("closed = %+v, %v", closed, err)
	}
	c := closed[0]
	if c.WorkerID != stale.ID || !c.ClockedOutAt.Equal(day(6, 0).Add(16*time.Hour)) {
		t.Fatalf("closed = %+v", c)
	}
	var flags []string
	var kind string
	var at time.Time
	inTenant(t, admin, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select kind, started_at, flags from time_registration where id = $1`, c.RegistrationID).Scan(&kind, &at, &flags)
	})
	if kind != "clock-out" || !at.Equal(c.ClockedOutAt) || len(flags) != 1 || flags[0] != domain.FlagAutoClockOut {
		t.Fatalf("stored = %s %v %v", kind, at, flags)
	}
	if n := countEvents(t, admin, tenantID, sfevents.RegistrationPosted); n < 1 {
		t.Fatalf("registration.posted events = %d", n)
	}
	// Repeating closes nothing: the latest punch is now a clock-out.
	if again, err := s.AutoClockOut(ctx, now, 16, 100); err != nil || len(again) != 0 {
		t.Fatalf("second sweep = %+v, %v", again, err)
	}
	// Later, the fresh worker's shift becomes stale too and is closed on its own threshold.
	if more, err := s.AutoClockOut(ctx, day(20, 0).Add(17*time.Hour), 16, 100); err != nil || len(more) != 1 || more[0].WorkerID != fresh.ID {
		t.Fatalf("later sweep = %+v, %v", more, err)
	}
	review, err := s.ListAutoClockOuts(ctx, "", now.Add(-time.Hour*48), time.Now().Add(time.Hour), 10)
	if err != nil || len(review) != 2 {
		t.Fatalf("review = %+v, %v", review, err)
	}
	for _, r := range review {
		if r.Reviewed || r.ClockedInAt == nil {
			t.Fatalf("review row = %+v", r)
		}
	}
	if one, err := s.ListAutoClockOuts(ctx, stale.ID, now.Add(-time.Hour*48), time.Now().Add(time.Hour), 10); err != nil || len(one) != 1 || one[0].WorkerName != stale.Name {
		t.Fatalf("filtered review = %+v, %v", one, err)
	}
	// BR-25.3: the closed day shows its 16 hours but keeps missing-clock-out, so it
	// cannot be approved until a supervisor confirms or corrects the clock-out.
	flagsOf := func() []string {
		t.Helper()
		in, err := s.LoadPunches(ctx, stale.ID, day(0, 0), day(0, 0).AddDate(0, 0, 1))
		if err != nil || len(in.Punches) != 2 {
			t.Fatalf("punches = %+v, %v", in.Punches, err)
		}
		days := domain.BuildDays(in.Punches, time.UTC, domain.DefaultProfile())
		if len(days) != 1 || len(days[0].Segments) != 1 || days[0].Segments[0].End.Sub(days[0].Segments[0].Start) != 16*time.Hour {
			t.Fatalf("days = %+v", days)
		}
		return days[0].Flags
	}
	if f := flagsOf(); len(f) != 1 || f[0] != domain.FlagMissingClockOut {
		t.Fatalf("flags before review = %v", f)
	}
	// The supervisor confirms the clock-out as it is: the flag clears and the review row says so.
	keep := c.ClockedOutAt
	if _, err := s.CorrectRegistration(ctx, c.RegistrationID, domain.CorrectionRequest{StartedAt: &keep, Reason: "worked the double shift"}, "sup-1", Idem{}); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if f := flagsOf(); len(f) != 0 {
		t.Fatalf("flags after confirmation = %v", f)
	}
	if one, err := s.ListAutoClockOuts(ctx, stale.ID, now.Add(-time.Hour*48), time.Now().Add(time.Hour), 10); err != nil || len(one) != 1 || !one[0].Reviewed {
		t.Fatalf("after confirmation = %+v, %v", one, err)
	}
	// Confirming twice is refused like any correction that changes nothing.
	if _, err := s.CorrectRegistration(ctx, c.RegistrationID, domain.CorrectionRequest{StartedAt: &keep, Reason: "again"}, "sup-1", Idem{}); problemCode(err) != domain.CodeCorrectionInvalid {
		t.Fatalf("second confirmation = %v", err)
	}
	// A later edit of the time still works.
	newOut := day(15, 0)
	if _, err := s.CorrectRegistration(ctx, c.RegistrationID, domain.CorrectionRequest{StartedAt: &newOut, Reason: "left at 15:00"}, "sup-1", Idem{}); err != nil {
		t.Fatal(err)
	}
	// Another tenant sees nothing.
	other := testkit.Ctx(testkit.TenantID(91))
	if o, err := s.AutoClockOut(other, now, 16, 100); err != nil || len(o) != 0 {
		t.Fatalf("other tenant = %+v, %v", o, err)
	}
	if o, err := s.ListAutoClockOuts(other, "", now.Add(-time.Hour*48), time.Now().Add(time.Hour), 10); err != nil || len(o) != 0 {
		t.Fatalf("other tenant review = %+v, %v", o, err)
	}
}

// The limit bounds one sweep and takes the oldest shift first; a worker another
// transaction holds (a kiosk batch, a correction) is skipped and closed next time.
func TestAutoClockOut_LimitAndSkipLocked(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(92)
	ctx := testkit.Ctx(tenantID)
	a := mustWorker(t, s, ctx, "AL-1", nil)
	b := mustWorker(t, s, ctx, "AL-2", nil)
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(a.ID, "clock-in", day(5, 0)), clock(b.ID, "clock-in", day(6, 0))}); err != nil {
		t.Fatal(err)
	}
	now := day(5, 0).Add(30 * time.Hour)
	first, err := s.AutoClockOut(ctx, now, 16, 1)
	if err != nil || len(first) != 1 || first[0].WorkerID != a.ID {
		t.Fatalf("limit 1 closes the oldest: %+v, %v", first, err)
	}
	bg := context.Background()
	held, err := admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := held.Exec(bg, `select 1 from worker where id = $1 for update`, b.ID); err != nil {
		t.Fatal(err)
	}
	if skipped, err := s.AutoClockOut(ctx, now, 16, 10); err != nil || len(skipped) != 0 {
		_ = held.Rollback(bg)
		t.Fatalf("a locked worker is skipped: %+v, %v", skipped, err)
	}
	if err := held.Rollback(bg); err != nil {
		t.Fatal(err)
	}
	second, err := s.AutoClockOut(ctx, now, 16, 1)
	if err != nil || len(second) != 1 || second[0].WorkerID != b.ID {
		t.Fatalf("next sweep closes the other: %+v, %v", second, err)
	}
}
