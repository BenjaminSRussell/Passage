package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	sfevents "github.com/BenjaminSRussell/passage/services/shopfloor/internal/events"
)

func jobStart(worker, job string, at time.Time, bundle *string) Registration {
	return Registration{ID: ids.New(), WorkerID: worker, Kind: KindJobStart, JobID: &job, BundleID: bundle, StartedAt: at}
}

func register(t *testing.T, s *Postgres, ctx context.Context, items ...Registration) []RegistrationResult {
	t.Helper()
	res, err := s.CreateRegistrations(ctx, items)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func jobStatus(t *testing.T, s *Postgres, ctx context.Context, id string) (string, int) {
	t.Helper()
	j, err := s.GetJob(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return j.Status, j.Version
}

// FR-25.3 through Postgres as passage_app: a job-start moves a released job to
// started; starting the next job auto-stops the previous one with a server-written,
// closed job-stop (hours, flag, SF-0002 warning, registration.posted); a replay of
// the start changes nothing; an explicitly closed job is not stopped again.
func TestDispatch_JobStartAutoStopsThePreviousJob(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(80)
	ctx := testkit.Ctx(tenantID)
	w := mustWorker(t, s, ctx, "DISP-1", nil)
	resource := ids.New()
	j1, j2, j3 := mustJob(t, s, ctx, resource, 8), mustJob(t, s, ctx, resource, 9), mustJob(t, s, ctx, resource, 10)

	first := jobStart(w.ID, j1.ID, day(8, 0), nil)
	res := register(t, s, ctx, first)
	if res[0].Status != StatusAccepted || len(res[0].AutoStopped) != 0 || len(res[0].Flags) != 0 || len(res[0].Warnings) != 0 {
		t.Fatalf("first start = %+v", res[0])
	}
	if st, v := jobStatus(t, s, ctx, j1.ID); st != JobStarted || v != j1.Version+1 {
		t.Fatalf("j1 = %s v%d, want started v%d", st, v, j1.Version+1)
	}

	res = register(t, s, ctx, jobStart(w.ID, j2.ID, day(9, 30), nil))
	stops := res[0].AutoStopped
	if len(stops) != 1 || stops[0].JobID != j1.ID || stops[0].Hours.String() != "1.5" || stops[0].BundleID != nil ||
		!stops[0].StartedAt.Equal(day(8, 0)) || !stops[0].StoppedAt.Equal(day(9, 30)) || len(stops[0].Flags) != 1 || stops[0].Flags[0] != domain.FlagAutoStopped {
		t.Fatalf("auto-stop = %+v", stops)
	}
	if len(res[0].Warnings) != 1 || res[0].Warnings[0].Code != domain.CodeJobAutoStopped {
		t.Fatalf("warnings = %+v", res[0].Warnings)
	}
	var raw []byte
	inTenant(t, admin, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(context.Background(), `select payload->'data' from outbox where type = $1 and aggregate_id = $2`,
			sfevents.RegistrationPosted, stops[0].RegistrationID).Scan(&raw)
	})
	var posted sfevents.RegistrationPostedData
	if err := json.Unmarshal(raw, &posted); err != nil || posted.Kind != KindJobStop || posted.JobID == nil || *posted.JobID != j1.ID ||
		posted.Hours == nil || posted.Hours.String() != "1.5" || len(posted.Flags) != 1 || posted.Flags[0] != domain.FlagAutoStopped {
		t.Fatalf("auto-stop event = %s, %v", raw, err)
	}
	if n := countEvents(t, admin, tenantID, sfevents.RegistrationPosted); n != 3 {
		t.Fatalf("registration.posted = %d, want 3 (two starts, one auto-stop)", n)
	}
	regs, err := s.ListRegistrations(ctx, w.ID, day(0, 0), day(23, 0), 50)
	if err != nil {
		t.Fatal(err)
	}
	var stopRow *RegistrationView
	for i := range regs {
		if regs[i].ID == stops[0].RegistrationID {
			stopRow = &regs[i]
		}
	}
	if stopRow == nil || stopRow.Kind != KindJobStop || stopRow.Hours == nil || stopRow.Hours.String() != "1.5" ||
		stopRow.StoppedAt == nil || len(stopRow.Flags) != 1 || stopRow.Note == nil {
		t.Fatalf("stored auto-stop = %+v", stopRow)
	}

	// A replay of the first start is a duplicate: nothing is stopped and j2 stays open.
	res = register(t, s, ctx, first)
	if res[0].Status != StatusDuplicate || len(res[0].AutoStopped) != 0 {
		t.Fatalf("replay = %+v", res[0])
	}
	sess, err := s.BadgeLogin(ctx, "DISP-1")
	if err != nil || len(sess.OpenJobs) != 1 || sess.OpenJobs[0].JobID != j2.ID {
		t.Fatalf("open jobs after replay = %+v, %v", sess.OpenJobs, err)
	}

	// The kiosk stops j2 with a closed job-stop; the next start has nothing to stop.
	stopAt := day(10, 0)
	h := domain.ElapsedHours(day(9, 30), stopAt)
	register(t, s, ctx, Registration{ID: ids.New(), WorkerID: w.ID, Kind: KindJobStop, JobID: &j2.ID, StartedAt: day(9, 30), StoppedAt: &stopAt, Hours: &h})
	res = register(t, s, ctx, jobStart(w.ID, j3.ID, day(10, 0), nil))
	if len(res[0].AutoStopped) != 0 {
		t.Fatalf("start after an explicit stop auto-stopped %+v", res[0].AutoStopped)
	}
	if n := countEvents(t, admin, tenantID, sfevents.RegistrationPosted); n != 5 {
		t.Fatalf("registration.posted = %d, want 5", n)
	}
}

// Regression (BR-25.7, AC-25.1): while kiosk K1 was offline the worker started j6
// on kiosk K2 at 07:20. K1 then replays a start of j5 at 07:03 together with its
// own closed stop 07:03-07:13 (stop first in the request, as a tie may come). The
// j5 start is older than j6, but its own stop closes it, so the server must not
// also close it at 07:20: before the fix j5 carried 0.17 h from K1's stop plus a
// server stop of 0.28 h. A start with no stop of its own (j4 at 07:17) is still
// closed at j6's start (0.05 h), and j6 stays open.
func TestDispatch_ReplayedStartWithOwnStopIsNotStoppedAgain(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(84))
	w := mustWorker(t, s, ctx, "DISP-4", nil)
	resource := ids.New()
	j4, j5, j6 := mustJob(t, s, ctx, resource, 7), mustJob(t, s, ctx, resource, 8), mustJob(t, s, ctx, resource, 9)
	register(t, s, ctx, jobStart(w.ID, j6.ID, day(7, 20), nil))

	start := jobStart(w.ID, j5.ID, day(7, 3), nil)
	stopAt := day(7, 13)
	h := domain.ElapsedHours(day(7, 3), stopAt)
	stop := Registration{ID: ids.New(), WorkerID: w.ID, Kind: KindJobStop, JobID: &j5.ID, StartedAt: day(7, 3), StoppedAt: &stopAt, Hours: &h, Offline: true}
	res := register(t, s, ctx, stop, start)
	if len(res[0].AutoStopped)+len(res[1].AutoStopped) != 0 {
		t.Fatalf("replayed start with its own stop was auto-stopped: %+v", res)
	}
	res = register(t, s, ctx, jobStart(w.ID, j4.ID, day(7, 17), nil))
	if a := res[0].AutoStopped; len(a) != 1 || a[0].JobID != j4.ID || !a[0].StoppedAt.Equal(day(7, 20)) || a[0].Hours.String() != "0.05" {
		t.Fatalf("stale start without a stop = %+v", a)
	}

	regs, err := s.ListRegistrations(ctx, w.ID, day(0, 0), day(23, 0), 50)
	if err != nil {
		t.Fatal(err)
	}
	hours := map[string]decimal.Decimal{}
	for _, r := range regs {
		if r.Kind == KindJobStop && r.Hours != nil {
			hours[*r.JobID] = hours[*r.JobID].Add(*r.Hours)
		}
	}
	if len(hours) != 2 || !hours[j5.ID].Equal(h) || hours[j4.ID].String() != "0.05" {
		t.Fatalf("job hours = %v, want j5 %s (its own stop) and j4 0.05 only", hours, h)
	}
	sess, err := s.BadgeLogin(ctx, "DISP-4")
	if err != nil || len(sess.OpenJobs) != 1 || sess.OpenJobs[0].JobID != j6.ID {
		t.Fatalf("open jobs = %+v, %v", sess.OpenJobs, err)
	}
}

// jobStopHours sums the stored job-stop hours per job of a worker on day().
func jobStopHours(t *testing.T, s *Postgres, ctx context.Context, workerID string) map[string]string {
	t.Helper()
	regs, err := s.ListRegistrations(ctx, workerID, day(0, 0), day(23, 0), 100)
	if err != nil {
		t.Fatal(err)
	}
	sum := map[string]decimal.Decimal{}
	for _, r := range regs {
		if r.Kind == KindJobStop && r.Hours != nil {
			sum[*r.JobID] = sum[*r.JobID].Add(*r.Hours)
		}
	}
	out := map[string]string{}
	for k, v := range sum {
		out[k] = v.StringFixed(2)
	}
	return out
}

// Regression (BR-25.7, AC-25.1, review of PR #368): the kiosk's chunk boundary
// splits a replayed start from its own stop. Worker A started j6 on kiosk K2 at
// 07:20. K1's first chunk holds only the j5 start of 07:03, which is older than
// j6, so the server closes it at 07:20 (0.28 h, SF-0002). The next chunk brings
// K1's own stop 07:03-07:13 (0.17 h). Before the fix j5 then held 0.28 + 0.17 =
// 0.45 h; now the server stop is corrected to zero length with a correction row
// and registration.corrected, so j5 is 0.17 h. A resend changes nothing more.
//
// Worker B shows the stop is matched to its own start only: B ran j5 from 06:00
// (closed at 06:30 by a j7 start, 0.50 h). K1 then replays a j5 start and its
// stop, both at 07:03, with the stop first in the request; the stop is processed
// before its start is stored and must not cancel the 06:00 session's stop.
func TestDispatch_ReplayedStopInLaterBatchSupersedesAutoStop(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(85)
	ctx := testkit.Ctx(tenantID)
	a := mustWorker(t, s, ctx, "DISP-5", nil)
	b := mustWorker(t, s, ctx, "DISP-6", nil)
	resource := ids.New()
	j5, j6, j7 := mustJob(t, s, ctx, resource, 10), mustJob(t, s, ctx, resource, 11), mustJob(t, s, ctx, resource, 12)
	stopOf := func(worker, job string, from, to time.Time) Registration {
		h := domain.ElapsedHours(from, to)
		return Registration{ID: ids.New(), WorkerID: worker, Kind: KindJobStop, JobID: &job, StartedAt: from, StoppedAt: &to, Hours: &h, Offline: true}
	}

	register(t, s, ctx, jobStart(a.ID, j6.ID, day(7, 20), nil))
	res := register(t, s, ctx, jobStart(a.ID, j5.ID, day(7, 3), nil))
	if as := res[0].AutoStopped; len(as) != 1 || as[0].JobID != j5.ID || as[0].Hours.StringFixed(2) != "0.28" {
		t.Fatalf("chunk 1 auto-stop = %+v", as)
	}
	serverStop := res[0].AutoStopped[0].RegistrationID
	stop := stopOf(a.ID, j5.ID, day(7, 3), day(7, 13))
	if res := register(t, s, ctx, stop); res[0].Status != StatusAccepted {
		t.Fatalf("chunk 2 = %+v", res)
	}
	if res := register(t, s, ctx, stop); res[0].Status != StatusDuplicate {
		t.Fatalf("chunk 2 resend = %+v", res)
	}
	if got := jobStopHours(t, s, ctx, a.ID); len(got) != 1 || got[j5.ID] != "0.17" {
		t.Fatalf("worker A job hours = %v, want j5 0.17 only", got)
	}
	regs, err := s.ListRegistrations(ctx, a.ID, day(0, 0), day(23, 0), 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range regs {
		if r.ID == serverStop && (r.StoppedAt == nil || !r.StoppedAt.Equal(day(7, 3)) || !r.Hours.IsZero()) {
			t.Fatalf("server stop after supersede = %+v", r)
		}
	}
	now := time.Now()
	corr, err := s.ListCorrections(ctx, a.ID, now.Add(-time.Hour), now.Add(time.Hour), 10)
	if err != nil || len(corr) != 1 || corr[0].RegistrationID != serverStop || corr[0].OldHours.StringFixed(2) != "0.28" ||
		!corr[0].NewHours.IsZero() || corr[0].CorrectedBy != autoStopSupersededBy {
		t.Fatalf("corrections = %+v, %v", corr, err)
	}
	if n := countEvents(t, app, tenantID, sfevents.RegistrationCorrected); n != 1 {
		t.Fatalf("registration.corrected rows = %d, want 1", n)
	}
	if sess, err := s.BadgeLogin(ctx, "DISP-5"); err != nil || len(sess.OpenJobs) != 1 || sess.OpenJobs[0].JobID != j6.ID {
		t.Fatalf("worker A open jobs = %+v, %v", sess.OpenJobs, err)
	}

	register(t, s, ctx, jobStart(b.ID, j5.ID, day(6, 0), nil))
	register(t, s, ctx, jobStart(b.ID, j7.ID, day(6, 30), nil))
	register(t, s, ctx, jobStart(b.ID, j6.ID, day(7, 20), nil))
	res = register(t, s, ctx, stopOf(b.ID, j5.ID, day(7, 3), day(7, 13)), jobStart(b.ID, j5.ID, day(7, 3), nil))
	if len(res[0].AutoStopped)+len(res[1].AutoStopped) != 0 {
		t.Fatalf("worker B replay auto-stopped = %+v", res)
	}
	// j5: 0.50 (06:00-06:30, server) + 0.17 (07:03-07:13, K1); j7: 06:30-07:20 = 0.83.
	if got := jobStopHours(t, s, ctx, b.ID); len(got) != 2 || got[j5.ID] != "0.67" || got[j7.ID] != "0.83" {
		t.Fatalf("worker B job hours = %v, want j5 0.67 and j7 0.83", got)
	}
	if corr, err := s.ListCorrections(ctx, b.ID, now.Add(-time.Hour), now.Add(time.Hour), 10); err != nil || len(corr) != 0 {
		t.Fatalf("worker B corrections = %+v, %v", corr, err)
	}
}

// Review of PR #368: a stop belongs to the start it follows, not to any start of
// the job before it. The worker ran j5 07:10-07:15 (start and stop stored) and
// then started j6 at 07:20. K1 replays a j5 start of 07:03 with no stop. The
// 07:10 stop is not this start's, so the server closes it, and at 07:10, the next
// start the worker registered, not at j6's 07:20 (which would overlap the 07:10
// session): 0.12 h. Before the fix the 07:10 stop counted as its own and the
// 07:03-07:10 time was lost.
func TestDispatch_StaleStartIsNotClosedByALaterSessionsStop(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(86))
	w := mustWorker(t, s, ctx, "DISP-7", nil)
	resource := ids.New()
	j5, j6 := mustJob(t, s, ctx, resource, 13), mustJob(t, s, ctx, resource, 14)
	stopAt := day(7, 15)
	h := domain.ElapsedHours(day(7, 10), stopAt)
	register(t, s, ctx, jobStart(w.ID, j5.ID, day(7, 10), nil),
		Registration{ID: ids.New(), WorkerID: w.ID, Kind: KindJobStop, JobID: &j5.ID, StartedAt: day(7, 10), StoppedAt: &stopAt, Hours: &h})
	register(t, s, ctx, jobStart(w.ID, j6.ID, day(7, 20), nil))

	res := register(t, s, ctx, jobStart(w.ID, j5.ID, day(7, 3), nil))
	if as := res[0].AutoStopped; len(as) != 1 || as[0].JobID != j5.ID || !as[0].StartedAt.Equal(day(7, 3)) ||
		!as[0].StoppedAt.Equal(day(7, 10)) || as[0].Hours.StringFixed(2) != "0.12" {
		t.Fatalf("stale start auto-stop = %+v", as)
	}
	// j5: 0.12 (07:03-07:10, server) + 0.08 (07:10-07:15, own stop).
	if got := jobStopHours(t, s, ctx, w.ID); len(got) != 1 || got[j5.ID] != "0.20" {
		t.Fatalf("job hours = %v, want j5 0.20", got)
	}
	if sess, err := s.BadgeLogin(ctx, "DISP-7"); err != nil || len(sess.OpenJobs) != 1 || sess.OpenJobs[0].JobID != j6.ID {
		t.Fatalf("open jobs = %+v, %v", sess.OpenJobs, err)
	}
}

// Review of PR #368: a replayed bundle whose members are older than the worker's
// open j6 and where only jA has its own stop. jA's stop carries jA's time; jB has
// no stop and is closed at j6's start (07:03-07:20, 0.28 h). Before the fix the
// whole bundle unit was skipped, so jB stayed open next to j6.
func TestDispatch_StaleBundleMemberWithoutStopIsClosed(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(87))
	w := mustWorker(t, s, ctx, "DISP-8", nil)
	resource := ids.New()
	ja, jb, j6 := mustJob(t, s, ctx, resource, 15), mustJob(t, s, ctx, resource, 16), mustJob(t, s, ctx, resource, 17)
	register(t, s, ctx, jobStart(w.ID, j6.ID, day(7, 20), nil))

	bundle := ids.New()
	stopAt := day(7, 13)
	h := domain.ElapsedHours(day(7, 3), stopAt)
	res := register(t, s, ctx, jobStart(w.ID, ja.ID, day(7, 3), &bundle), jobStart(w.ID, jb.ID, day(7, 3), &bundle),
		Registration{ID: ids.New(), WorkerID: w.ID, Kind: KindJobStop, JobID: &ja.ID, BundleID: &bundle, StartedAt: day(7, 3), StoppedAt: &stopAt, Hours: &h, Offline: true})
	var stopped []AutoStop
	for _, r := range res {
		stopped = append(stopped, r.AutoStopped...)
	}
	if len(stopped) != 1 || stopped[0].JobID != jb.ID || !stopped[0].StoppedAt.Equal(day(7, 20)) || stopped[0].Hours.StringFixed(2) != "0.28" {
		t.Fatalf("auto-stops = %+v, want jB 07:03-07:20 0.28 only", stopped)
	}
	if sess, err := s.BadgeLogin(ctx, "DISP-8"); err != nil || len(sess.OpenJobs) != 1 || sess.OpenJobs[0].JobID != j6.ID {
		t.Fatalf("open jobs = %+v, %v", sess.OpenJobs, err)
	}
}

// FR-25.3 and BR-25.1: the jobs of a bundle start together without stopping each
// other; the next start stops the bundle as one unit and splits its time by the
// good quantity on the starts. A start for a job of another tenant moves nothing.
func TestDispatch_BundleStopsAsOneUnit(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(81)
	ctx := testkit.Ctx(tenantID)
	w := mustWorker(t, s, ctx, "DISP-2", nil)
	resource := ids.New()
	a, b, next := mustJob(t, s, ctx, resource, 8), mustJob(t, s, ctx, resource, 8), mustJob(t, s, ctx, resource, 9)
	bundle := ids.New()

	startA, startB := jobStart(w.ID, a.ID, day(10, 0), &bundle), jobStart(w.ID, b.ID, day(10, 0), &bundle)
	startA.QtyGood, startB.QtyGood = decimal.NewFromInt(3), decimal.NewFromInt(1)
	res := register(t, s, ctx, startA, startB)
	if len(res[0].AutoStopped)+len(res[1].AutoStopped) != 0 {
		t.Fatalf("bundle members stopped each other: %+v", res)
	}
	sess, err := s.BadgeLogin(ctx, "DISP-2")
	if err != nil || len(sess.OpenJobs) != 2 {
		t.Fatalf("open bundle = %+v, %v", sess.OpenJobs, err)
	}

	res = register(t, s, ctx, jobStart(w.ID, next.ID, day(12, 0), nil))
	hours := map[string]string{}
	for _, st := range res[0].AutoStopped {
		if st.BundleID == nil || *st.BundleID != bundle || !st.StoppedAt.Equal(day(12, 0)) {
			t.Fatalf("bundle stop = %+v", st)
		}
		hours[st.JobID] = st.Hours.String()
	}
	if len(hours) != 2 || hours[a.ID] != "1.5" || hours[b.ID] != "0.5" || len(res[0].Warnings) != 2 {
		t.Fatalf("bundle split = %v, warnings %+v", hours, res[0].Warnings)
	}
	members, err := s.ListBundleRegistrations(ctx, bundle, 10)
	if err != nil || len(members) != 4 {
		t.Fatalf("bundle read-back = %d rows, %v", len(members), err)
	}

	// Tenant isolation: the same job id started in another tenant is unknown there,
	// so it is accepted without moving or flagging this tenant's job.
	other := testkit.Ctx(testkit.TenantID(82))
	ow := mustWorker(t, s, other, "DISP-2", nil)
	if r := register(t, s, other, jobStart(ow.ID, next.ID, day(13, 0), nil)); r[0].Status != StatusAccepted || len(r[0].Flags) != 0 {
		t.Fatalf("other tenant start = %+v", r[0])
	}
	if st, v := jobStatus(t, s, ctx, next.ID); st != JobStarted || v != next.Version+1 {
		t.Fatalf("next = %s v%d", st, v)
	}
	// Counted as passage_app, so row-level security shows the other tenant only its own event.
	if n := countEvents(t, app, testkit.TenantID(82), sfevents.RegistrationPosted); n != 1 {
		t.Fatalf("other tenant events = %d", n)
	}
}

// BR-25.7 and SF-0004: a job-start for a job the server already finished is kept,
// flagged job-finished-conflict on the row, the result and the event, and the job
// stays finished. A start left open for more than 24 hours is stopped at 24 hours
// and flagged for review.
func TestDispatch_FinishedConflictAndCappedAutoStop(t *testing.T) {
	s, _, admin := setup(t)
	tenantID := testkit.TenantID(83)
	ctx := testkit.Ctx(tenantID)
	w := mustWorker(t, s, ctx, "DISP-3", nil)
	resource := ids.New()
	done, later := mustJob(t, s, ctx, resource, 8), mustJob(t, s, ctx, resource, 9)
	if _, err := s.RecordFeedback(ctx, done.ID, FeedbackInput{ID: ids.New(), GoodQty: decimal.NewFromInt(100), Finished: true, RecordedBy: "op"}); err != nil {
		t.Fatal(err)
	}
	finished, version := jobStatus(t, s, ctx, done.ID)

	start := jobStart(w.ID, done.ID, day(6, 0), nil)
	start.Offline = true
	res := register(t, s, ctx, start)
	if res[0].Status != StatusAccepted || len(res[0].Flags) != 1 || res[0].Flags[0] != domain.ConflictJobFinished ||
		len(res[0].Warnings) != 1 || res[0].Warnings[0].Code != domain.CodeJobFinishedOffline {
		t.Fatalf("finished-job start = %+v", res[0])
	}
	if st, v := jobStatus(t, s, ctx, done.ID); st != finished || st != JobFinished || v != version {
		t.Fatalf("finished job moved to %s v%d", st, v)
	}
	var posted sfevents.RegistrationPostedData
	lastEventData(t, admin, tenantID, sfevents.RegistrationPosted, &posted)
	if posted.RegistrationID != start.ID || len(posted.Flags) != 1 || posted.Flags[0] != domain.ConflictJobFinished {
		t.Fatalf("event = %+v", posted)
	}

	// The conflicting start is still open work; the next day's start stops it at 24 h.
	res = register(t, s, ctx, jobStart(w.ID, later.ID, day(6, 0).Add(30*time.Hour), nil))
	stops := res[0].AutoStopped
	if len(stops) != 1 || stops[0].Hours.String() != "24" || !stops[0].StoppedAt.Equal(day(6, 0).Add(24*time.Hour)) ||
		len(stops[0].Flags) != 2 || stops[0].Flags[1] != domain.FlagAutoStopCapped {
		t.Fatalf("capped stop = %+v", stops)
	}
}
