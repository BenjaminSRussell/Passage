package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// Queue times are stored on the operation, change the placement stale, ride on
// operation.updated, and a run leaves the wait between the two operations
// without booking a resource.
func TestOperationQueueTimes_RunHonoursThemAndPatchStalesTheJobs(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	m1, m2 := mustResource(t, ctx, s, "M1", true), mustResource(t, ctx, s, "M2", true)
	order := ids.New()
	op1, _, err := s.CreateOperation(ctx, store.Operation{OrderID: order, OperNumber: 10, Minutes: 60, Priority: 100, ResourceIDs: []string{m1.ID}, QueueAfterMinutes: 30}, store.Idem{})
	if err != nil || op1.QueueAfterMinutes != 30 || op1.QueueBeforeMinutes != 0 {
		t.Fatalf("op1 = %+v err=%v, want queue after 30", op1, err)
	}
	op2, _, err := s.CreateOperation(ctx, store.Operation{OrderID: order, OperNumber: 20, Minutes: 60, Priority: 100, ResourceIDs: []string{m2.ID}, QueueBeforeMinutes: 45}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	b1, b2 := bars[op1.ID], bars[op2.ID]
	if !b1.End.Equal(at(0, 9, 0)) || !b2.Start.Equal(at(0, 10, 15)) {
		t.Fatalf("o1 ends %v, o2 starts %v; want 09:00 and 10:15 (30 + 45 min of queue)", b1.End, b2.Start)
	}

	// Changing a queue time takes the jobs off the schedule and is published.
	before := 0
	upd, _, err := s.UpdateOperation(ctx, op2.ID, 1, store.OperationPatch{QueueBeforeMinutes: &before}, idem("q1"))
	if err != nil || !upd.JobsRemoved || upd.QueueBeforeMinutes != 0 || upd.Status != "open" {
		t.Fatalf("patch = %+v err=%v, want jobs removed and queue before 0", upd, err)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.operation.updated.v1"); got != 1 {
		t.Fatalf("operation.updated events = %d, want 1", got)
	}
	// Only o2 is selected: o1 keeps its slot and its queue after still holds o2.
	if _, _, err := s.RunSchedule(ctx, store.RunRequest{Scope: "selection", Mode: "jobs", Finite: true, OperationIDs: []string{op2.ID},
		Start: at(0, 6, 0), HorizonDays: 60}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if got := barsOf(t, ctx, s)[op2.ID].Start; !got.Equal(at(0, 9, 30)) {
		t.Fatalf("o2 starts %v after the change, want 09:30 (o1's 30 min queue after)", got)
	}

	// A firmed job is never taken off the schedule by a queue change.
	if _, _, err := s.FirmJob(ctx, op1.ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	after := 90
	if _, _, err := s.UpdateOperation(ctx, op1.ID, 1, store.OperationPatch{QueueAfterMinutes: &after}, idem("q2")); problemCode(err) != store.CodeOperationFirmed {
		t.Fatalf("queue change on a firmed operation: %v, want SCHED-0138", err)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.operation.updated.v1"); got != 1 {
		t.Fatalf("operation.updated events = %d after the refusal, want still 1", got)
	}
	if bar, ok := barsOf(t, ctx, s)[op1.ID]; !ok || !bar.End.Equal(at(0, 9, 0)) {
		t.Fatalf("firmed o1 bar = %+v ok=%v, want it kept ending 09:00", bar, ok)
	}

	// The column checks hold the range in the database too.
	if _, err := admin.Exec(context.Background(), `update scheduling_operation set queue_before_minutes = 10081 where id = $1`, op2.ID); err == nil {
		t.Fatal("queue_before_minutes 10081 was stored, want the check constraint to refuse it")
	}
}
