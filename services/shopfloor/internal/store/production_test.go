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
