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
