package store

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
)

// FR-25.3 and BR-25.7 through Postgres as passage_app: production finishes an operation (w21
// operation.statuschanged.v1), after which an online job-start on its job is SF-0157 and stores
// nothing, while an offline replay of a start after the finish is accepted and flagged
// job-finished-conflict (SF-0004); work that started before the finish and every job-stop pass;
// a resend of a start accepted before the finish arrived is a duplicate, not a refusal; events are
// ordered by order version (an older one never undoes a newer one, even with a later occurredAt);
// the job's order reference matches whatever its case; production's reopen lets the job start again.
func TestProductionFinish_StopsJobStartsUntilReopened(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(74)
	ctx := testkit.Ctx(tenantID)
	worker := mustWorker(t, s, ctx, "B-74", nil).ID
	orderID := ids.New()
	job, _, err := s.UpsertJob(ctx, Job{ID: ids.New(), OrderRef: strings.ToUpper(orderID), OperationRef: "20", ResourceID: ids.New(),
		PlannedQty: decimal.NewFromInt(10), SequenceStart: day(8, 0)})
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := s.UpsertJob(ctx, Job{ID: ids.New(), OrderRef: orderID, OperationRef: "30", ResourceID: ids.New(),
		PlannedQty: decimal.NewFromInt(10), SequenceStart: day(9, 0)})
	if err != nil {
		t.Fatal(err)
	}
	apply := func(in OperationStatusIn) bool {
		t.Helper()
		var changed bool
		if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			var err error
			changed, err = s.ApplyOperationStatus(ctx, tx, in)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return changed
	}
	finishedAt := day(12, 0)
	// A start accepted while production's finish was still on its way (consumer lag).
	lagged := jobStart(worker, job.ID, finishedAt.Add(30*time.Minute), nil)
	register(t, s, ctx, lagged)
	if !apply(OperationStatusIn{OrderID: orderID, OperNumber: 20, Status: "finished", Finished: true, At: finishedAt, OrderVersion: 5, EventID: "e1"}) {
		t.Fatal("finish was not stored")
	}
	got, err := s.GetJob(ctx, job.ID)
	if err != nil || got.ProductionFinishedAt == nil || !got.ProductionFinishedAt.Equal(finishedAt) {
		t.Fatalf("job after finish = %+v, %v", got, err)
	}
	if o, _ := s.GetJob(ctx, other.ID); o.ProductionFinishedAt != nil {
		t.Fatalf("another operation of the order is marked finished: %+v", o)
	}
	if res := register(t, s, ctx, lagged); res[0].Status != StatusDuplicate {
		t.Fatalf("resend of a start accepted before the finish arrived = %+v, want duplicate", res)
	}

	// An online start at or after the finish is refused and nothing in the batch is stored.
	late := jobStart(worker, job.ID, finishedAt.Add(time.Hour), nil)
	clockIn := Registration{ID: ids.New(), WorkerID: worker, Kind: "clock-in", StartedAt: finishedAt.Add(time.Hour)}
	if _, err := s.CreateRegistrations(ctx, []Registration{clockIn, late}); problemCode(err) != CodeOperationFinished {
		t.Fatalf("start after the finish = %v, want %s", err, CodeOperationFinished)
	}
	if res := register(t, s, ctx, clockIn); res[0].Status != StatusAccepted {
		t.Fatalf("the refused batch stored its clock-in: %+v", res)
	}
	// Offline, the same start is work the operator already did: accepted and flagged (BR-25.7).
	late.Offline = true
	res := register(t, s, ctx, late)
	if res[0].Status != StatusAccepted || !slices.Contains(res[0].Flags, domain.ConflictJobFinished) ||
		len(res[0].Warnings) == 0 || res[0].Warnings[0].Code != domain.CodeJobFinishedOffline {
		t.Fatalf("offline start after the finish = %+v", res)
	}
	// A start of another operation, a start before the finish and a stop after it pass unflagged.
	if res := register(t, s, ctx, jobStart(worker, other.ID, finishedAt.Add(time.Hour), nil)); len(res[0].Flags) != 0 {
		t.Fatalf("start of an open operation = %+v", res)
	}
	earlier := jobStart(worker, job.ID, finishedAt.Add(-2*time.Hour), nil)
	end := finishedAt.Add(2 * time.Hour)
	h := decimal.NewFromInt(2)
	earlier.StoppedAt, earlier.Hours = &end, &h
	stopAt := finishedAt.Add(3 * time.Hour)
	jobStop := Registration{ID: ids.New(), WorkerID: worker, Kind: KindJobStop, JobID: &job.ID, StartedAt: finishedAt.Add(2 * time.Hour), StoppedAt: &stopAt, Hours: &h}
	if res := register(t, s, ctx, earlier, jobStop); res[0].Status != StatusAccepted || res[1].Status != StatusAccepted || len(res[0].Flags) != 0 {
		t.Fatalf("start before the finish and a stop after it = %+v", res)
	}

	// An older event changes nothing, even when its occurredAt is later (two production commands
	// racing on the order: the one that committed first can carry the later transaction time);
	// the redelivery of the stored one is harmless.
	if apply(OperationStatusIn{OrderID: orderID, OperNumber: 20, Status: "started", Finished: false, At: finishedAt.Add(time.Hour), OrderVersion: 4, EventID: "e0"}) {
		t.Fatal("an older order version changed the stored status")
	}
	apply(OperationStatusIn{OrderID: orderID, OperNumber: 20, Status: "finished", Finished: true, At: finishedAt, OrderVersion: 5, EventID: "e1"})
	if _, err := s.CreateRegistrations(ctx, []Registration{jobStart(worker, job.ID, finishedAt.Add(3*time.Hour), nil)}); problemCode(err) != CodeOperationFinished {
		t.Fatalf("start after a stale reopen = %v", err)
	}

	// Production reopens the operation (a newer version, here with an earlier time): the job starts
	// again and no longer shows a finish.
	if !apply(OperationStatusIn{OrderID: orderID, OperNumber: 20, Status: "started", Finished: false, At: finishedAt.Add(-time.Minute), OrderVersion: 6, EventID: "e2"}) {
		t.Fatal("reopen was not stored")
	}
	if got, _ := s.GetJob(ctx, job.ID); got.ProductionFinishedAt != nil {
		t.Fatalf("job still shows the finish after the reopen: %+v", got)
	}
	if res := register(t, s, ctx, jobStart(worker, job.ID, finishedAt.Add(5*time.Hour), nil)); res[0].Status != StatusAccepted || len(res[0].Flags) != 0 {
		t.Fatalf("start after the reopen = %+v", res)
	}
}

// FR-25.3 through Postgres as passage_app: when production finishes an operation, the open
// job-starts of every worker on its jobs are closed in the consumer's transaction by a job-stop
// flagged production-finished (hours from the start to the finish, zero for a start after it),
// with registration.posted; a bundled start, a start on another operation and a start that already
// has its stop are left alone; a repeat of the event writes nothing; a kiosk stop that arrives
// later for the same start supersedes the server's, so the hours count once. Feedback on the job
// is SF-0161 while production's finish stands, a resend of stored feedback is still a duplicate,
// and production's reopen lets the job take feedback again.
func TestProductionFinish_ClosesOpenStartsAndRefusesFeedback(t *testing.T) {
	s, app, admin := setup(t)
	tenantID := testkit.TenantID(76)
	ctx := testkit.Ctx(tenantID)
	orderID := ids.New()
	mk := func(op string) Job {
		j, _, err := s.UpsertJob(ctx, Job{ID: ids.New(), OrderRef: orderID, OperationRef: op, ResourceID: ids.New(),
			PlannedQty: decimal.NewFromInt(10), SequenceStart: day(6, 0)})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	job, other := mk("20"), mk("30")
	ana := mustWorker(t, s, ctx, "A-76", nil).ID
	ben := mustWorker(t, s, ctx, "B-76", nil).ID
	cy := mustWorker(t, s, ctx, "C-76", nil).ID
	dee := mustWorker(t, s, ctx, "D-76", nil).ID
	finishedAt := day(12, 0)
	bundle := ids.New()
	register(t, s, ctx, jobStart(ana, job.ID, day(8, 0), nil))
	late := jobStart(ben, job.ID, finishedAt.Add(30*time.Minute), nil)
	register(t, s, ctx, late)
	register(t, s, ctx, jobStart(cy, job.ID, day(9, 0), &bundle))
	register(t, s, ctx, jobStart(cy, other.ID, day(9, 0), &bundle))
	register(t, s, ctx, jobStart(dee, other.ID, day(9, 0), nil))
	fb := ids.New()
	if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: fb, GoodQty: decimal.NewFromInt(3), RecordedBy: "op"}); err != nil {
		t.Fatal(err)
	}
	apply := func(version int, finished bool, at time.Time) {
		t.Helper()
		status := "started"
		if finished {
			status = "finished"
		}
		if err := dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error {
			_, err := s.ApplyOperationStatus(ctx, tx, OperationStatusIn{OrderID: orderID, OperNumber: 20, Status: status, Finished: finished, At: at,
				OrderVersion: version, EventID: ids.New()})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	stopsOf := func(worker string) []RegistrationView {
		t.Helper()
		all, err := s.ListRegistrations(ctx, worker, day(0, 0), day(0, 0).AddDate(0, 0, 1), 50)
		if err != nil {
			t.Fatal(err)
		}
		var out []RegistrationView
		for _, r := range all {
			if r.Kind == KindJobStop {
				out = append(out, r)
			}
		}
		return out
	}
	before := countEvents(t, admin, tenantID, "dev.passage.shopfloor.registration.posted.v1")
	apply(5, true, finishedAt)
	apply(5, true, finishedAt) // the same event again: stored status is not newer, nothing more is written

	a := stopsOf(ana)
	if len(a) != 1 || a[0].Hours == nil || a[0].Hours.String() != "4" || !slices.Equal(a[0].Flags, []string{domain.FlagProductionFinished}) ||
		!a[0].StartedAt.Equal(day(8, 0)) || a[0].StoppedAt == nil || !a[0].StoppedAt.Equal(finishedAt) {
		t.Fatalf("Ana's stop = %+v", a)
	}
	if b := stopsOf(ben); len(b) != 1 || b[0].Hours == nil || !b[0].Hours.IsZero() || !b[0].StoppedAt.Equal(late.StartedAt) ||
		!slices.Equal(b[0].Flags, []string{domain.FlagProductionFinished, domain.ConflictJobFinished}) {
		t.Fatalf("Ben's start after the finish = %+v", b)
	}
	// Cy's bundle holds a job of the finished operation: the whole bundle closes at the finish and its
	// three hours are split again over both members (1.50 h each), flagged production-finished.
	c := stopsOf(cy)
	if len(c) != 2 {
		t.Fatalf("Cy's bundle stops = %+v", c)
	}
	for _, r := range c {
		if r.Hours == nil || r.Hours.String() != "1.5" || r.BundleID == nil || *r.BundleID != bundle || !r.StoppedAt.Equal(finishedAt) ||
			!slices.Equal(r.Flags, []string{domain.FlagProductionFinished}) {
			t.Fatalf("Cy's bundle stop = %+v", r)
		}
	}
	if sess, err := s.BadgeLogin(ctx, "C-76"); err != nil || len(sess.OpenJobs) != 0 {
		t.Fatalf("Cy's open jobs after the bundle closed = %+v, %v", sess.OpenJobs, err)
	}
	if sess, err := s.BadgeLogin(ctx, "D-76"); err != nil || len(sess.OpenJobs) != 1 || sess.OpenJobs[0].ProductionFinishedAt != nil {
		t.Fatalf("Dee's open job on another operation = %+v, %v", sess.OpenJobs, err)
	}
	if d := stopsOf(dee); len(d) != 0 {
		t.Fatalf("a start on another operation was closed: %+v", d)
	}
	if got := countEvents(t, admin, tenantID, "dev.passage.shopfloor.registration.posted.v1") - before; got != 4 {
		t.Fatalf("registration.posted after the finish = %d, want 4", got)
	}
	register(t, s, ctx, Registration{ID: ids.New(), WorkerID: cy, Kind: "clock-in", StartedAt: day(7, 0)})
	board, err := s.ClockedIn(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(board) != 1 || board[0].WorkerID != cy || len(board[0].Jobs) != 0 {
		t.Fatalf("clocked-in board = %+v, want Cy clocked in with no open job", board)
	}

	// The kiosk's own stop for Ana's start, sent later, wins: the server's stop becomes zero length.
	end, two := day(10, 0), decimal.NewFromInt(2)
	own := Registration{ID: ids.New(), WorkerID: ana, Kind: KindJobStop, JobID: &job.ID, StartedAt: day(8, 0), StoppedAt: &end, Hours: &two}
	register(t, s, ctx, own)
	hours := decimal.Zero
	for _, r := range stopsOf(ana) {
		if r.Hours != nil {
			hours = hours.Add(*r.Hours)
		}
	}
	if !hours.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("Ana's hours after her own stop = %s, want 2 (the server's stop superseded)", hours)
	}

	// Cy stopped the bundle at 10:00 on a kiosk that was offline when the finish arrived: the replay
	// is accepted (not SF-0113), the server's bundle stops become 0.00 h with registration.corrected,
	// so the bundle counts the kiosk's hour once; a replay of the batch is a duplicate, and another
	// batch closing the bundle again is still SF-0113.
	half := decimal.RequireFromString("0.5")
	bundleStop := func(job string) Registration {
		return Registration{ID: ids.New(), WorkerID: cy, Kind: KindJobStop, JobID: &job, BundleID: &bundle, StartedAt: day(9, 0),
			StoppedAt: &end, Hours: &half, Offline: true}
	}
	upper := strings.ToUpper(bundle)
	replay := []Registration{bundleStop(job.ID), bundleStop(other.ID)}
	for i := range replay {
		replay[i].BundleID = &upper // the kiosk's case of the bundle id does not matter
	}
	corrected := countEvents(t, admin, tenantID, "dev.passage.shopfloor.registration.corrected.v1")
	if res := register(t, s, ctx, replay...); res[0].Status != StatusAccepted || res[1].Status != StatusAccepted {
		t.Fatalf("offline bundle stop after the finish = %+v", res)
	}
	if got := countEvents(t, admin, tenantID, "dev.passage.shopfloor.registration.corrected.v1") - corrected; got != 2 {
		t.Fatalf("registration.corrected for the superseded bundle stops = %d, want 2", got)
	}
	sum := decimal.Zero
	for _, r := range stopsOf(cy) {
		sum = sum.Add(*r.Hours)
		if slices.Contains(r.Flags, domain.FlagProductionFinished) && (!r.Hours.IsZero() || !r.StoppedAt.Equal(r.StartedAt)) {
			t.Fatalf("server bundle stop not superseded: %+v", r)
		}
	}
	if !sum.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("Cy's bundle hours after the kiosk's stop = %s, want 1", sum)
	}
	if res := register(t, s, ctx, replay...); res[0].Status != StatusDuplicate || res[1].Status != StatusDuplicate {
		t.Fatalf("replay of the bundle stop = %+v", res)
	}
	if _, err := s.CreateRegistrations(ctx, []Registration{bundleStop(job.ID), bundleStop(other.ID)}); problemCode(err) != domain.CodeBundleInvalid {
		t.Fatalf("a second batch closing the bundle = %v, want %s", err, domain.CodeBundleInvalid)
	}

	// Feedback: refused while production's finish stands, a stored resend is a duplicate.
	if _, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), GoodQty: decimal.NewFromInt(1), RecordedBy: "op"}); problemCode(err) != CodeJobProductionClosed {
		t.Fatalf("feedback on a production-closed job = %v, want %s", err, CodeJobProductionClosed)
	}
	if res, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: fb, GoodQty: decimal.NewFromInt(3), RecordedBy: "op"}); err != nil || !res.Duplicate {
		t.Fatalf("resend of stored feedback = %+v, %v", res, err)
	}
	if res, err := s.RecordFeedback(ctx, other.ID, FeedbackInput{ID: ids.New(), GoodQty: decimal.NewFromInt(1), RecordedBy: "op"}); err != nil || res.Duplicate {
		t.Fatalf("feedback on another operation = %+v, %v", res, err)
	}
	apply(6, false, finishedAt.Add(time.Hour))
	if res, err := s.RecordFeedback(ctx, job.ID, FeedbackInput{ID: ids.New(), GoodQty: decimal.NewFromInt(1), RecordedBy: "op"}); err != nil || res.Duplicate {
		t.Fatalf("feedback after the reopen = %+v, %v", res, err)
	}
}

// FR-25.3 through Postgres as passage_app, concurrently: production's finish and a kiosk batch on
// the same job serialize, so the job's hours count once and no start stays open whichever commits
// first. A finish still uncommitted makes the kiosk's stop wait, and the stop then supersedes the
// server's (registration.corrected). A kiosk start still in flight makes the finish wait, and the
// finish then closes it.
func TestProductionFinish_SerializesWithKioskBatches(t *testing.T) {
	s, app, _ := setup(t)
	tenantID := testkit.TenantID(77)
	ctx := testkit.Ctx(tenantID)
	begin := func() pgx.Tx {
		t.Helper()
		tx, err := app.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `select set_config('passage.tenant_id', $1, true)`, tenantID); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(ctx) })
		return tx
	}
	blocked := func(done <-chan error) {
		t.Helper()
		select {
		case err := <-done:
			t.Fatalf("finished without waiting for the other transaction: %v", err)
		case <-time.After(300 * time.Millisecond):
		}
	}
	wait := func(done <-chan error) {
		t.Helper()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("still waiting after the other transaction committed")
		}
	}
	hoursOf := func(worker string) (decimal.Decimal, int) {
		t.Helper()
		all, err := s.ListRegistrations(ctx, worker, day(0, 0), day(0, 0).AddDate(0, 0, 1), 50)
		if err != nil {
			t.Fatal(err)
		}
		sum, stops := decimal.Zero, 0
		for _, r := range all {
			if r.Kind == KindJobStop && r.Hours != nil {
				sum, stops = sum.Add(*r.Hours), stops+1
			}
		}
		return sum, stops
	}
	finish := func(tx pgx.Tx, orderID string, version int) error {
		_, err := s.ApplyOperationStatus(ctx, tx, OperationStatusIn{OrderID: orderID, OperNumber: 20, Status: "finished", Finished: true,
			At: day(12, 0), OrderVersion: version, EventID: ids.New()})
		return err
	}
	mkJob := func(orderID string) string {
		j, _, err := s.UpsertJob(ctx, Job{ID: ids.New(), OrderRef: orderID, OperationRef: "20", ResourceID: ids.New(),
			PlannedQty: decimal.NewFromInt(10), SequenceStart: day(6, 0)})
		if err != nil {
			t.Fatal(err)
		}
		return j.ID
	}
	two, end := decimal.NewFromInt(2), day(10, 0)

	// The finish holds the job: the kiosk's stop waits, then supersedes the server's 4-hour stop.
	orderA := ids.New()
	jobA := mkJob(orderA)
	ana := mustWorker(t, s, ctx, "A-77", nil).ID
	register(t, s, ctx, jobStart(ana, jobA, day(8, 0), nil))
	consumer := begin()
	if err := finish(consumer, orderA, 3); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.CreateRegistrations(ctx, []Registration{{ID: ids.New(), WorkerID: ana, Kind: KindJobStop, JobID: &jobA,
			StartedAt: day(8, 0), StoppedAt: &end, Hours: &two}})
		done <- err
	}()
	blocked(done)
	if err := consumer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wait(done)
	if sum, stops := hoursOf(ana); !sum.Equal(two) || stops != 2 {
		t.Fatalf("Ana's job hours = %s over %d stops, want 2 (the kiosk's stop; the server's superseded to 0)", sum, stops)
	}

	// A kiosk start is in flight when the finish arrives (it read the operation as open and waits on
	// the job row, held here by another writer): the finish waits for the batch and closes the start.
	orderB := ids.New()
	jobB := mkJob(orderB)
	ben := mustWorker(t, s, ctx, "B-77", nil).ID
	holder := begin()
	if _, err := holder.Exec(ctx, `select 1 from shop_job where tenant_id = $1 and id = $2 for update`, tenantID, jobB); err != nil {
		t.Fatal(err)
	}
	kioskDone := make(chan error, 1)
	go func() {
		_, err := s.CreateRegistrations(ctx, []Registration{jobStart(ben, jobB, day(8, 0), nil)})
		kioskDone <- err
	}()
	blocked(kioskDone)
	go func() {
		done <- dbx.WithTenantTx(ctx, app, func(tx pgx.Tx) error { return finish(tx, orderB, 3) })
	}()
	blocked(done)
	if err := holder.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wait(kioskDone)
	wait(done)
	if sum, stops := hoursOf(ben); !sum.Equal(decimal.NewFromInt(4)) || stops != 1 {
		t.Fatalf("Ben's job hours = %s over %d stops, want 4 from the server's stop at the finish", sum, stops)
	}

	// The finish holds a bundle with a member on the operation: the kiosk's stop of the bundle waits,
	// then supersedes the server's bundle stops, so the bundle counts the kiosk's 2 hours once.
	orderC := ids.New()
	jobC, jobD := mkJob(orderC), mkJob(ids.New())
	cy := mustWorker(t, s, ctx, "C-77", nil).ID
	bundle := ids.New()
	register(t, s, ctx, jobStart(cy, jobC, day(8, 0), &bundle), jobStart(cy, jobD, day(8, 0), &bundle))
	consumer = begin()
	if err := finish(consumer, orderC, 3); err != nil {
		t.Fatal(err)
	}
	one := decimal.NewFromInt(1)
	go func() {
		_, err := s.CreateRegistrations(ctx, []Registration{
			{ID: ids.New(), WorkerID: cy, Kind: KindJobStop, JobID: &jobC, BundleID: &bundle, StartedAt: day(8, 0), StoppedAt: &end, Hours: &one},
			{ID: ids.New(), WorkerID: cy, Kind: KindJobStop, JobID: &jobD, BundleID: &bundle, StartedAt: day(8, 0), StoppedAt: &end, Hours: &one},
		})
		done <- err
	}()
	blocked(done)
	if err := consumer.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wait(done)
	if sum, stops := hoursOf(cy); !sum.Equal(two) || stops != 4 {
		t.Fatalf("Cy's bundle hours = %s over %d stops, want 2 (the kiosk's; the server's two superseded to 0)", sum, stops)
	}
}
