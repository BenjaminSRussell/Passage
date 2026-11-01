package store

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

func absenceReg(worker, code string, from, to time.Time) Registration {
	h := decimal.NewFromInt(int64(to.Sub(from) / time.Second)).Div(decimal.NewFromInt(3600))
	return Registration{ID: ids.New(), WorkerID: worker, Kind: KindAbsence, AbsenceCodeID: &code, StartedAt: from, StoppedAt: &to, Hours: &h}
}

func TestAbsenceCode_CreateUpdateVersionAndEvents(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(41)
	ctx := testkit.Ctx(tenantID)
	created, err := s.CreateAbsenceCode(ctx, AbsenceCodeInput{Code: "SICK", Name: "Sick leave", Paid: true, PayMultiplier: decimal.NewFromInt(1), Active: true}, "admin-1", idem("ab-1"))
	if err != nil || created.Version != 1 || !created.Paid {
		t.Fatalf("created = %+v, %v", created, err)
	}
	if _, err := s.CreateAbsenceCode(ctx, AbsenceCodeInput{Code: "SICK", Name: "Again", Paid: false, PayMultiplier: decimal.Zero, Active: true}, "admin-1", idem("ab-2")); problemCode(err) != CodeSetupCodeTaken {
		t.Fatalf("duplicate code: %v", err)
	}
	updated, err := s.UpdateAbsenceCode(ctx, created.ID, 1, AbsenceCodeInput{Name: "Sick (paid 80%)", Paid: true, PayMultiplier: decimal.RequireFromString("0.8"), Active: true}, "admin-2")
	if err != nil || updated.Version != 2 || updated.PayMultiplier.String() != "0.8" || updated.Code != "SICK" {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	if _, err := s.UpdateAbsenceCode(ctx, created.ID, 1, AbsenceCodeInput{Name: "Stale", Paid: true, PayMultiplier: decimal.NewFromInt(1), Active: true}, "admin-2"); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("stale: %v", err)
	}
	if _, err := s.UpdateAbsenceCode(ctx, ids.New(), 1, AbsenceCodeInput{Name: "x", PayMultiplier: decimal.NewFromInt(1)}, "admin-2"); problemCode(err) != CodeAbsenceNotFound {
		t.Fatalf("unknown: %v", err)
	}
	if l, err := s.ListAbsenceCodes(ctx, true, 10); err != nil || len(l) != 1 {
		t.Fatalf("list = %+v, %v", l, err)
	}
	if n := countEvents(t, app, tenantID, sfevents.AbsenceCodeSaved); n != 2 {
		t.Fatalf("absencecode.saved events = %d, want 2", n)
	}
	other := testkit.Ctx(testkit.TenantID(42))
	if _, err := s.GetAbsenceCode(other, created.ID); problemCode(err) != CodeAbsenceNotFound {
		t.Fatalf("other tenant: %v", err)
	}
}

func TestAbsenceRegistration_CodeChecksAndLoad(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(43)
	ctx := testkit.Ctx(tenantID)
	w := mustWorker(t, s, ctx, "ab-w1", nil)
	paid, err := s.CreateAbsenceCode(ctx, AbsenceCodeInput{Code: "HOL", Name: "Holiday", Paid: true, PayMultiplier: decimal.NewFromInt(1), Active: true}, "admin", idem("h"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRegistrations(ctx, []Registration{absenceReg(w.ID, ids.New(), day(8, 0), day(16, 0))}); problemCode(err) != CodeAbsenceNotFound {
		t.Fatalf("unknown code: %v", err)
	}
	reg := absenceReg(w.ID, paid.ID, day(8, 0), day(16, 0))
	if res, err := s.CreateRegistrations(ctx, []Registration{reg}); err != nil || res[0].Status != StatusAccepted {
		t.Fatalf("absence: %+v, %v", res, err)
	}
	// Only the other kinds of registration ignore the code check: a plain clock-in has none.
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(w.ID, domain.KindClockIn, day(9, 0))}); err != nil {
		t.Fatal(err)
	}
	in, err := s.LoadPunches(ctx, w.ID, day(0, 0), day(0, 0).AddDate(0, 0, 1))
	if err != nil || len(in.Absences) != 1 || in.Absences[0].ID != reg.ID || !in.Absences[0].Paid || in.Absences[0].Hours.String() != "8" || in.Absences[0].Multiplier.String() != "1" {
		t.Fatalf("absences = %+v, %v", in.Absences, err)
	}
	if len(in.Punches) != 1 {
		t.Fatalf("punches = %+v (an absence is not a punch)", in.Punches)
	}
	views, err := s.ListRegistrations(ctx, w.ID, day(0, 0), day(0, 0).AddDate(0, 0, 1), 10)
	if err != nil || len(views) != 2 || views[0].AbsenceCodeID == nil || *views[0].AbsenceCodeID != paid.ID {
		t.Fatalf("views = %+v, %v", views, err)
	}

	// An inactive code refuses a new absence but a replay of a stored one is still a duplicate.
	if _, err := s.UpdateAbsenceCode(ctx, paid.ID, 1, AbsenceCodeInput{Name: "Holiday", Paid: true, PayMultiplier: decimal.NewFromInt(1), Active: false}, "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRegistrations(ctx, []Registration{absenceReg(w.ID, paid.ID, day(8, 0).AddDate(0, 0, 1), day(16, 0).AddDate(0, 0, 1))}); problemCode(err) != CodeAbsenceInactive {
		t.Fatalf("inactive: %v", err)
	}
	if res, err := s.CreateRegistrations(ctx, []Registration{reg}); err != nil || res[0].Status != StatusDuplicate {
		t.Fatalf("replay: %+v, %v", res, err)
	}
	if n := countEvents(t, app, tenantID, sfevents.RegistrationPosted); n != 2 {
		t.Fatalf("registration.posted events = %d, want 2", n)
	}
	// Another tenant cannot read the worker's absences (RLS).
	other := testkit.Ctx(testkit.TenantID(44))
	if in, err := s.LoadPunches(other, w.ID, day(0, 0), day(0, 0).AddDate(0, 0, 1)); err == nil {
		t.Fatalf("other tenant loaded %+v", in)
	}
}

// The pay terms of a code are fixed once an absence names it (SF-0147), so a recalculated or
// revised day pays the absence as registered; the name and status can still change, and an
// unused code can change its terms. The table refuses a multiplier on an unpaid code.
func TestAbsenceCode_PayTermsLockedOnceUsed(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(45))
	w := mustWorker(t, s, ctx, "ab-w2", nil)
	one := decimal.NewFromInt(1)
	used, err := s.CreateAbsenceCode(ctx, AbsenceCodeInput{Code: "SICK", Name: "Sick", Paid: true, PayMultiplier: one, Active: true}, "admin", idem("lk-1"))
	if err != nil {
		t.Fatal(err)
	}
	unused, err := s.CreateAbsenceCode(ctx, AbsenceCodeInput{Code: "HOL", Name: "Holiday", Paid: true, PayMultiplier: one, Active: true}, "admin", idem("lk-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRegistrations(ctx, []Registration{absenceReg(w.ID, used.ID, day(8, 0), day(12, 0))}); err != nil {
		t.Fatal(err)
	}
	for name, in := range map[string]AbsenceCodeInput{
		"share":  {Name: "Sick", Paid: true, PayMultiplier: decimal.RequireFromString("0.8"), Active: true},
		"unpaid": {Name: "Sick", Paid: false, PayMultiplier: decimal.Zero, Active: true},
	} {
		if _, err := s.UpdateAbsenceCode(ctx, used.ID, 1, in, "admin"); problemCode(err) != CodeAbsenceTermsLocked {
			t.Errorf("%s: %v, want SF-0147", name, err)
		}
	}
	renamed, err := s.UpdateAbsenceCode(ctx, used.ID, 1, AbsenceCodeInput{Name: "Sick leave", Paid: true, PayMultiplier: decimal.RequireFromString("1.0000"), Active: false}, "admin")
	if err != nil || renamed.Version != 2 || renamed.Name != "Sick leave" || renamed.Active {
		t.Fatalf("rename and deactivate: %+v, %v", renamed, err)
	}
	if changed, err := s.UpdateAbsenceCode(ctx, unused.ID, 1, AbsenceCodeInput{Name: "Holiday", Paid: false, PayMultiplier: decimal.Zero, Active: true}, "admin"); err != nil || changed.Paid || !changed.PayMultiplier.IsZero() {
		t.Fatalf("unused code: %+v, %v", changed, err)
	}
	if _, err := s.CreateAbsenceCode(ctx, AbsenceCodeInput{Code: "BAD", Name: "Bad", Paid: false, PayMultiplier: one, Active: true}, "admin", idem("lk-3")); err == nil {
		t.Fatal("an unpaid code with a multiplier was stored")
	}
}

// calculateWithAbsences builds and saves one UTC day with its absences, as the API does.
func calculateWithAbsences(t *testing.T, s *Postgres, ctx context.Context, worker, date string) error {
	t.Helper()
	d, _ := time.Parse("2006-01-02", date)
	in, err := s.LoadPunches(ctx, worker, d, d.AddDate(0, 0, 1))
	if err != nil {
		return err
	}
	calc := domain.Calculation{Profile: domain.DefaultProfile(), Location: time.UTC, Rules: domain.DefaultRules(), Rate: in.Rate, Currency: in.Currency}
	days, err := calc.Run(domain.AddAbsences(domain.BuildDays(in.Punches, time.UTC, calc.Profile), in.Absences, time.UTC))
	if err != nil {
		return err
	}
	return s.SavePay(ctx, PayCalculation{WorkerID: worker, FromDate: date, ToDate: date, TimeZone: "UTC", Days: days}, Idem{}, map[string]string{"ok": date})
}

// An absence-only day is stored and approved with its lines (the unpaid line with multiplier 0);
// an absence registered after the calculation makes the day stale (SF-0104); an absence that
// overlaps clocked time holds the day (SF-0006).
func TestAbsencePay_SaveApproveStaleAndOverlap(t *testing.T) {
	s, _, admin := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(46))
	w := mustWorker(t, s, ctx, "ab-w3", nil)
	paid, err := s.CreateAbsenceCode(ctx, AbsenceCodeInput{Code: "SICK", Name: "Sick", Paid: true, PayMultiplier: decimal.RequireFromString("0.8"), Active: true}, "admin", idem("ap-1"))
	if err != nil {
		t.Fatal(err)
	}
	unpaid, err := s.CreateAbsenceCode(ctx, AbsenceCodeInput{Code: "UNP", Name: "Unpaid", Paid: false, PayMultiplier: decimal.Zero, Active: true}, "admin", idem("ap-2"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRegistrations(ctx, []Registration{absenceReg(w.ID, paid.ID, day(8, 0), day(12, 0))}); err != nil {
		t.Fatal(err)
	}
	if err := calculateWithAbsences(t, s, ctx, w.ID, "2026-10-20"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `update worker_day set calculated_at = now() - interval '1 minute' where worker_id = $1`, w.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateRegistrations(ctx, []Registration{absenceReg(w.ID, unpaid.ID, day(13, 0), day(15, 0))}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePay(ctx, w.ID, "2026-10-20", "2026-10-20", "supervisor-1", Idem{}); problemCode(err) != CodeDayStale {
		t.Fatalf("late absence: %v, want %s", err, CodeDayStale)
	}
	if err := calculateWithAbsences(t, s, ctx, w.ID, "2026-10-20"); err != nil {
		t.Fatal(err)
	}
	out, err := s.ApprovePay(ctx, w.ID, "2026-10-20", "2026-10-20", "supervisor-1", Idem{})
	if err != nil {
		t.Fatal(err)
	}
	lines := map[string]PayLine{}
	for _, l := range out.Days[0].PayLines {
		lines[l.Type] = l
	}
	if !out.Days[0].Hours.IsZero() || len(lines) != 2 {
		t.Fatalf("day = %+v", out.Days[0])
	}
	if l := lines[domain.TypeAbsencePaid]; l.Amount.StringFixed(2) != "64.00" || l.Multiplier.String() != "0.8" {
		t.Fatalf("paid line = %+v", l)
	}
	if l := lines[domain.TypeAbsenceUnpaid]; l.Hours.StringFixed(2) != "2.00" || !l.Multiplier.IsZero() || !l.Amount.IsZero() {
		t.Fatalf("unpaid line = %+v", l)
	}

	// The next day: clocked 07:00-15:00 with a paid absence 10:00-12:00 inside it.
	next := func(h int) time.Time { return day(h, 0).AddDate(0, 0, 1) }
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(w.ID, domain.KindClockIn, next(7)), clock(w.ID, domain.KindClockOut, next(15)), absenceReg(w.ID, paid.ID, next(10), next(12))}); err != nil {
		t.Fatal(err)
	}
	if err := calculateWithAbsences(t, s, ctx, w.ID, "2026-10-21"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePay(ctx, w.ID, "2026-10-21", "2026-10-21", "supervisor-1", Idem{}); problemCode(err) != CodeDayFlagged {
		t.Fatalf("overlap: %v, want %s", err, CodeDayFlagged)
	}
}
