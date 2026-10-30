package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

// revise recalculates one UTC day and applies it with RevisePay, as the API does.
func revise(t *testing.T, s *Postgres, ctx context.Context, worker, date, by string) (RevisionResult, error) {
	t.Helper()
	d, _ := time.Parse("2006-01-02", date)
	in, err := s.LoadPunches(ctx, worker, d, d.AddDate(0, 0, 1))
	if err != nil {
		t.Fatal(err)
	}
	calc := domain.Calculation{Profile: domain.DefaultProfile(), Location: time.UTC, Rules: domain.DefaultRules(), Rate: in.Rate, Currency: in.Currency}
	days, err := calc.Run(domain.BuildDays(in.Punches, time.UTC, calc.Profile))
	if err != nil {
		t.Fatal(err)
	}
	return s.RevisePay(ctx, PayCalculation{WorkerID: worker, FromDate: date, ToDate: date, TimeZone: "UTC", Days: days}, "missed punch", by, idem("rev-"+ids.New()))
}

func sumAmount(lines []PayLine) decimal.Decimal {
	total := decimal.Zero
	for _, l := range lines {
		total = total.Add(l.Amount)
	}
	return total
}

// AC-25.8: an approved 09:00-19:30 day is corrected to end at 18:30. The approved lines
// stay, reversal lines cancel the overtime that no longer applies, and the sum of the
// day's lines is the new pay.
func TestCorrectApprovedDay_ReversalAndNewLine_AC25_8(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(21)
	ctx := testkit.Ctx(tenantID)
	worker := mustWorker(t, s, ctx, "R-1", nil).ID
	out := clock(worker, "clock-out", day(19, 30))
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(worker, "clock-in", day(9, 0)), out}); err != nil {
		t.Fatal(err)
	}
	if err := calculate(t, s, ctx, worker, "2026-10-20", Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePay(ctx, worker, "2026-10-20", "2026-10-20", "supervisor-1", Idem{}); err != nil {
		t.Fatal(err)
	}
	newStop := day(18, 30)
	c, err := s.CorrectRegistration(ctx, out.ID, domain.CorrectionRequest{StartedAt: &newStop, Reason: "left at 18:30"}, "supervisor-2", idem("c-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.OldStartedAt.Equal(day(19, 30)) || !c.NewStartedAt.Equal(newStop) || c.Reason != "left at 18:30" {
		t.Fatalf("correction = %+v", c)
	}
	res, err := revise(t, s, ctx, worker, "2026-10-20", "supervisor-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Days) != 1 || !res.Days[0].Changed || res.Days[0].Revision != 1 || res.Days[0].Status != "approved" {
		t.Fatalf("revision = %+v", res)
	}
	if len(res.Days[0].ReversedLines) != 1 || res.Days[0].ReversedLines[0].Hours.String() != "-2.5" || len(res.Days[0].AddedLines) != 1 {
		t.Fatalf("reversed %+v added %+v", res.Days[0].ReversedLines, res.Days[0].AddedLines)
	}
	days, err := s.ListWorkerDays(ctx, DayFilter{WorkerID: worker, FromDate: "2026-10-20", ToDate: "2026-10-20", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	d := days[0]
	if d.Status != "approved" || d.Revision != 1 || d.Hours.String() != "9.5" || len(d.PayLines) != 4 {
		t.Fatalf("day = %+v", d)
	}
	// 8 h base (160) + 1.5 h x 1.5 overtime (45) = 205 after the revision.
	if got := sumAmount(d.PayLines); !got.Equal(decimal.NewFromInt(205)) {
		t.Fatalf("net pay %s, want 205", got)
	}
	// A second revision of the same data changes nothing (SF-0129).
	if _, err := revise(t, s, ctx, worker, "2026-10-20", "supervisor-2"); problemCode(err) != domain.CodeRevisionNothingChanged {
		t.Fatalf("second revision: %v", err)
	}
	// The next payroll export carries the reversal and the new line along with the approved ones.
	exp, err := s.ExportPay(ctx, "2026-10-20", "2026-10-20", "payroll-1", idem("exp-1"))
	if err != nil {
		t.Fatal(err)
	}
	if exp.LineCount != 4 {
		t.Fatalf("export lines %d, want 4", exp.LineCount)
	}
	if countEvents(t, app, tenantID, sfevents.RegistrationCorrected) != 1 || countEvents(t, app, tenantID, sfevents.PaydayRevised) != 1 {
		t.Fatal("corrected and revised events must each be published once")
	}
	// A third change reverses only the lines still effective: the earlier reversal pair stays.
	later := day(17, 30)
	if _, err := s.CorrectRegistration(ctx, out.ID, domain.CorrectionRequest{StartedAt: &later, Reason: "again"}, "supervisor-2", idem("c-2")); err != nil {
		t.Fatal(err)
	}
	res, err = revise(t, s, ctx, worker, "2026-10-20", "supervisor-2")
	if err != nil {
		t.Fatal(err)
	}
	days, _ = s.ListWorkerDays(ctx, DayFilter{WorkerID: worker, FromDate: "2026-10-20", ToDate: "2026-10-20", Limit: 10})
	if days[0].Revision != 2 || !sumAmount(days[0].PayLines).Equal(decimal.NewFromInt(175)) {
		t.Fatalf("after second revision: revision %d net %s", days[0].Revision, sumAmount(days[0].PayLines))
	}
	// The day was already exported: the next batch carries only the second revision's
	// reversal of the 1.5 h overtime and the new 0.5 h line, never the exported lines again.
	exp2, err := s.ExportPay(ctx, "2026-10-20", "2026-10-20", "payroll-1", idem("exp-2"))
	if err != nil {
		t.Fatal(err)
	}
	if exp2.LineCount != 2 || !strings.Contains(exp2.CSV, "-1.5") || !strings.Contains(exp2.CSV, "0.5") {
		t.Fatalf("second export %d lines:\n%s", exp2.LineCount, exp2.CSV)
	}
	// Original lines are never edited.
	inTenant(t, app, tenantID, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(context.Background(), `select count(*) from pay_line where revision = 0`).Scan(&n); err != nil {
			return err
		}
		if n != 2 {
			t.Fatalf("original lines %d, want 2", n)
		}
		return nil
	})
}

func TestCorrectRegistration_RulesOwnUserAndStaleness(t *testing.T) {
	s, _, _ := setup(t)
	tenantID := testkit.TenantID(22)
	ctx := testkit.Ctx(tenantID)
	user := "worker-user"
	worker := mustWorker(t, s, ctx, "R-2", &user).ID
	in, out := clock(worker, "clock-in", day(9, 0)), clock(worker, "clock-out", day(17, 0))
	if _, err := s.CreateRegistrations(ctx, []Registration{in, out}); err != nil {
		t.Fatal(err)
	}
	t0 := day(8, 30)
	if _, err := s.CorrectRegistration(ctx, ids.New(), domain.CorrectionRequest{StartedAt: &t0, Reason: "x"}, "sup", Idem{}); problemCode(err) != CodeRegistrationNotFound {
		t.Fatalf("unknown id: %v", err)
	}
	if _, err := s.CorrectRegistration(ctx, in.ID, domain.CorrectionRequest{StartedAt: &t0, Reason: "x"}, user, Idem{}); problemCode(err) != CodeOwnApproval {
		t.Fatalf("own correction: %v", err)
	}
	stop := day(18, 0)
	if _, err := s.CorrectRegistration(ctx, in.ID, domain.CorrectionRequest{StoppedAt: &stop, Reason: "x"}, "sup", Idem{}); problemCode(err) != domain.CodeRegistrationNotCorrectable {
		t.Fatalf("stop on a clock-in: %v", err)
	}
	// A correction after calculation makes the day stale until it is recalculated.
	if err := calculate(t, s, ctx, worker, "2026-10-20", Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CorrectRegistration(ctx, in.ID, domain.CorrectionRequest{StartedAt: &t0, Reason: "early start"}, "sup", Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePay(ctx, worker, "2026-10-20", "2026-10-20", "sup", Idem{}); problemCode(err) != CodeDayStale {
		t.Fatalf("approve after correction: %v, want %s", err, CodeDayStale)
	}
	if err := calculate(t, s, ctx, worker, "2026-10-20", Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePay(ctx, worker, "2026-10-20", "2026-10-20", "sup", Idem{}); err != nil {
		t.Fatalf("approve after recalculation: %v", err)
	}
	list, err := s.ListCorrections(ctx, worker, day(0, 0).AddDate(0, 0, -400), time.Now().Add(time.Hour), 10)
	if err != nil || len(list) != 1 || list[0].Reason != "early start" || list[0].OldHours != nil {
		t.Fatalf("corrections = %+v, %v", list, err)
	}
	// Revising your own pay is refused.
	if _, err := revise(t, s, ctx, worker, "2026-10-20", user); problemCode(err) != CodeOwnApproval {
		t.Fatalf("own revision: %v", err)
	}
	// Another tenant sees nothing.
	other, _ := s.ListCorrections(testkit.Ctx(testkit.TenantID(23)), "", day(0, 0).AddDate(0, 0, -400), time.Now().Add(time.Hour), 10)
	if len(other) != 0 {
		t.Fatalf("tenant isolation: %+v", other)
	}
}

func TestReviseCalculatedDayReplacesLines(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(24))
	worker := mustWorker(t, s, ctx, "R-3", nil).ID
	out := clock(worker, "clock-out", day(17, 0))
	if _, err := s.CreateRegistrations(ctx, []Registration{clock(worker, "clock-in", day(9, 0)), out}); err != nil {
		t.Fatal(err)
	}
	if err := calculate(t, s, ctx, worker, "2026-10-20", Idem{}); err != nil {
		t.Fatal(err)
	}
	later := day(16, 0)
	if _, err := s.CorrectRegistration(ctx, out.ID, domain.CorrectionRequest{StartedAt: &later, Reason: "x"}, "sup", Idem{}); err != nil {
		t.Fatal(err)
	}
	res, err := revise(t, s, ctx, worker, "2026-10-20", "sup")
	if err != nil || res.Days[0].Status != "calculated" || !res.Days[0].Changed {
		t.Fatalf("revise: %+v %v", res, err)
	}
	days, _ := s.ListWorkerDays(ctx, DayFilter{WorkerID: worker, FromDate: "2026-10-20", ToDate: "2026-10-20", Limit: 10})
	if len(days[0].PayLines) != 1 || days[0].PayLines[0].Hours.String() != "7" {
		t.Fatalf("lines = %+v", days[0].PayLines)
	}
}

// A job-start that a job-stop closed is refused (its hours live on the job-stop);
// the job-stop itself is corrected and its hours recomputed; an open job-start is
// closed by a stop time and no longer counts as open on the kiosk.
func TestCorrectRegistration_JobStartsAndStops(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(25)
	ctx := testkit.Ctx(tenantID)
	w := mustWorker(t, s, ctx, "R-5", nil)
	resource := ids.New()
	j1, j2 := mustJob(t, s, ctx, resource, 8), mustJob(t, s, ctx, resource, 9)
	start1 := jobStart(w.ID, j1.ID, day(8, 0), nil)
	register(t, s, ctx, start1)
	stopAt := day(10, 0)
	h := domain.ElapsedHours(day(8, 0), stopAt)
	stop1 := Registration{ID: ids.New(), WorkerID: w.ID, Kind: KindJobStop, JobID: &j1.ID, StartedAt: day(8, 0), StoppedAt: &stopAt, Hours: &h}
	register(t, s, ctx, stop1)

	later := day(11, 0)
	if _, err := s.CorrectRegistration(ctx, start1.ID, domain.CorrectionRequest{StoppedAt: &later, Reason: "x"}, "sup", Idem{}); problemCode(err) != domain.CodeRegistrationNotCorrectable {
		t.Fatalf("closing a stopped job-start: %v, want %s", err, domain.CodeRegistrationNotCorrectable)
	}
	c, err := s.CorrectRegistration(ctx, stop1.ID, domain.CorrectionRequest{StoppedAt: &later, Reason: "ran over"}, "sup", Idem{})
	if err != nil || c.NewHours == nil || c.NewHours.String() != "3" || c.OldHours == nil || c.OldHours.String() != "2" {
		t.Fatalf("job-stop correction = %+v, %v", c, err)
	}
	var raw []byte
	inTenant(t, app, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select payload->'data' from outbox where type = $1 and aggregate_id = $2`,
			sfevents.RegistrationCorrected, stop1.ID).Scan(&raw)
	})
	var ev sfevents.RegistrationCorrectedData
	if err := json.Unmarshal(raw, &ev); err != nil || ev.JobID == nil || *ev.JobID != j1.ID || ev.Hours == nil || ev.Hours.String() != "3" || ev.Reason != "ran over" {
		t.Fatalf("registration.corrected = %s, %v", raw, err)
	}

	start2 := jobStart(w.ID, j2.ID, day(12, 0), nil)
	register(t, s, ctx, start2)
	end := day(13, 30)
	if c, err := s.CorrectRegistration(ctx, start2.ID, domain.CorrectionRequest{StoppedAt: &end, Reason: "forgot to stop"}, "sup", Idem{}); err != nil || c.NewHours == nil || c.NewHours.String() != "1.5" {
		t.Fatalf("closing an open job-start = %+v, %v", c, err)
	}
	if sess, err := s.BadgeLogin(ctx, "R-5"); err != nil || len(sess.OpenJobs) != 0 {
		t.Fatalf("open jobs after the correction = %+v, %v", sess.OpenJobs, err)
	}
}
