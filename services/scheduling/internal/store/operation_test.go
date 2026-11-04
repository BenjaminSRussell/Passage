package store_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func idem(k string) store.Idem { return store.Idem{Key: k, Fingerprint: "fp-" + k} }

func jobCount(t *testing.T, ctx context.Context, s *store.Postgres, opID string) int {
	t.Helper()
	n := 0
	for _, b := range barsOf(t, ctx, s) {
		if b.JobID == opID {
			n++
		}
	}
	return n
}

func TestUpdateOperation_ChangeRemovesJobsAndPriorityDoesNot(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	m1, m2 := mustResource(t, ctx, s, "M1", true), mustResource(t, ctx, s, "M2", true)
	order := ids.New()
	op := mustOperation(t, ctx, s, order, 10, 60, m1.ID)
	if op.Version != 1 {
		t.Fatalf("new operation version = %d, want 1", op.Version)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}

	prio := 7
	upd, replayed, err := s.UpdateOperation(ctx, op.ID, 1, store.OperationPatch{Priority: &prio}, idem("p1"))
	if err != nil || replayed || upd.Version != 2 || upd.Priority != 7 || upd.JobsRemoved || upd.Status != "scheduled" || jobCount(t, ctx, s, op.ID) != 1 {
		t.Fatalf("priority change: %+v replayed=%v err=%v, want version 2, jobs kept", upd, replayed, err)
	}

	// Stale version.
	mins := 90
	if _, _, err := s.UpdateOperation(ctx, op.ID, 1, store.OperationPatch{Minutes: &mins}, idem("s1")); problemCode(err) != store.CodeOperationVersion {
		t.Fatalf("stale: %v, want SCHED-0137", err)
	}

	due := at(3, 0, 0)
	upd, _, err = s.UpdateOperation(ctx, op.ID, 2, store.OperationPatch{Minutes: &mins, DueAt: &due, ResourceIDs: []string{m2.ID, m1.ID}}, idem("m1"))
	if err != nil || upd.Version != 3 || !upd.JobsRemoved || upd.Status != "open" || upd.Minutes != 90 || upd.ResourceIDs[0] != m2.ID || upd.DueAt == nil || jobCount(t, ctx, s, op.ID) != 0 {
		t.Fatalf("minutes change: %+v err=%v, want jobs removed and operation open", upd, err)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.operation.updated.v1"); got != 2 {
		t.Fatalf("operation.updated events = %d, want 2", got)
	}

	// The same key replays the first answer even though the version moved on.
	again, replayed, err := s.UpdateOperation(ctx, op.ID, 2, store.OperationPatch{Minutes: &mins, DueAt: &due, ResourceIDs: []string{m2.ID, m1.ID}}, idem("m1"))
	if err != nil || !replayed || again.Version != 3 {
		t.Fatalf("replay: %+v replayed=%v err=%v", again, replayed, err)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.operation.updated.v1"); got != 2 {
		t.Fatalf("replay published again: %d", got)
	}
	// Reusing the key for another body is refused.
	if _, _, err := s.UpdateOperation(ctx, op.ID, 3, store.OperationPatch{Minutes: &mins}, store.Idem{Key: "m1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}

	// Unknown candidate, and a candidate that is also a secondary.
	if _, _, err := s.UpdateOperation(ctx, op.ID, 3, store.OperationPatch{ResourceIDs: []string{ids.New()}}, idem("u1")); problemCode(err) != store.CodeUnknownResource {
		t.Fatalf("unknown candidate: %v", err)
	}
	tool := mustResource(t, ctx, s, "TOOL", true)
	withTool, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 30, Priority: 100, ResourceIDs: []string{m1.ID}, SecondaryResourceIDs: []string{tool.ID}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpdateOperation(ctx, withTool.ID, 1, store.OperationPatch{ResourceIDs: []string{tool.ID}}, idem("c1")); problemCode(err) != store.CodeCandidateSecondary {
		t.Fatalf("candidate that is a secondary: %v", err)
	}
	if _, _, err := s.UpdateOperation(ctx, ids.New(), 1, store.OperationPatch{Priority: &prio}, idem("n1")); problemCode(err) != store.CodeOperationNotFound {
		t.Fatalf("unknown operation: %v", err)
	}
}

func TestUpdateAndDeleteOperation_RefuseFirmedAndOtherTenants(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	op := mustOperation(t, ctx, s, ids.New(), 10, 60, m.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FirmJob(ctx, op.ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	mins := 30
	if _, _, err := s.UpdateOperation(ctx, op.ID, 1, store.OperationPatch{Minutes: &mins}, idem("f1")); problemCode(err) != store.CodeOperationFirmed {
		t.Fatalf("update firmed: %v, want SCHED-0138", err)
	}
	if _, _, err := s.DeleteOperation(ctx, op.ID, 1, idem("f2")); problemCode(err) != store.CodeOperationFirmed {
		t.Fatalf("delete firmed: %v, want SCHED-0138", err)
	}
	// A priority change is allowed on a firmed operation: it moves nothing.
	prio := 3
	if _, _, err := s.UpdateOperation(ctx, op.ID, 1, store.OperationPatch{Priority: &prio}, idem("f3")); err != nil {
		t.Fatalf("priority on firmed: %v", err)
	}
	// Another tenant cannot see or change it.
	other := tenantCtx(2)
	if _, err := s.GetOperation(other, op.ID); problemCode(err) != store.CodeOperationNotFound {
		t.Fatalf("other tenant get: %v", err)
	}
	if _, _, err := s.DeleteOperation(other, op.ID, 2, idem("o1")); problemCode(err) != store.CodeOperationNotFound {
		t.Fatalf("other tenant delete: %v", err)
	}
	got, err := s.GetOperation(ctx, op.ID)
	if err != nil || got.Version != 2 {
		t.Fatalf("get: %+v err=%v", got, err)
	}
}

func TestDeleteOperation_RemovesJobsAndReplays(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	tool := mustResource(t, ctx, s, "TOOL", true)
	op, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 60, Priority: 100, ResourceIDs: []string{m.ID}, SecondaryResourceIDs: []string{tool.ID}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DeleteOperation(ctx, op.ID, 5, idem("d0")); problemCode(err) != store.CodeOperationVersion {
		t.Fatalf("stale delete: %v", err)
	}
	out, replayed, err := s.DeleteOperation(ctx, op.ID, 1, idem("d1"))
	if err != nil || replayed || !out.JobsRemoved || out.OperationID != op.ID || out.OperNumber != 10 {
		t.Fatalf("delete: %+v replayed=%v err=%v", out, replayed, err)
	}
	if _, err := s.GetOperation(ctx, op.ID); problemCode(err) != store.CodeOperationNotFound {
		t.Fatalf("operation still there: %v", err)
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select (select count(*) from scheduled_job) + (select count(*) from scheduled_job_secondary)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("jobs left = %d err=%v", n, err)
	}
	again, replayed, err := s.DeleteOperation(ctx, op.ID, 1, idem("d1"))
	if err != nil || !replayed || again != out {
		t.Fatalf("replay: %+v replayed=%v err=%v", again, replayed, err)
	}
	if _, _, err := s.DeleteOperation(ctx, op.ID, 1, idem("d2")); problemCode(err) != store.CodeOperationNotFound {
		t.Fatalf("second delete with a new key: %v", err)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.operation.deleted.v1"); got != 1 {
		t.Fatalf("operation.deleted events = %d, want 1", got)
	}
}

func TestUnscheduleOrder_OpensOperationsAndKeepsFirmed(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	order := ids.New()
	op1, op2, op3 := mustOperation(t, ctx, s, order, 10, 60, m.ID), mustOperation(t, ctx, s, order, 20, 60, m.ID), mustOperation(t, ctx, s, order, 30, 60, m.ID)
	otherOrder := mustOperation(t, ctx, s, ids.New(), 10, 60, m.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FirmJob(ctx, op2.ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	out, replayed, err := s.UnscheduleOrder(ctx, order, idem("u1"))
	if err != nil || replayed || out.JobsRemoved != 2 || len(out.Operations) != 2 || out.Operations[0] != 10 || out.Operations[1] != 30 || len(out.KeptFirmed) != 1 || out.KeptFirmed[0] != 20 {
		t.Fatalf("unschedule: %+v replayed=%v err=%v, want 10 and 30 open, 20 kept", out, replayed, err)
	}
	bars := barsOf(t, ctx, s)
	if _, ok := bars[op1.ID]; ok {
		t.Fatal("operation 10 still has a bar")
	}
	if _, ok := bars[op3.ID]; ok {
		t.Fatal("operation 30 still has a bar")
	}
	if _, ok := bars[op2.ID]; !ok {
		t.Fatal("the firmed operation lost its bar")
	}
	if _, ok := bars[otherOrder.ID]; !ok {
		t.Fatal("another order lost its bar")
	}
	open, err := s.ListOperations(ctx, store.OperationFilter{OrderID: order, Status: "open", Limit: 10})
	if err != nil || len(open) != 2 {
		t.Fatalf("open operations = %d err=%v, want 2", len(open), err)
	}
	if again, replayed, err := s.UnscheduleOrder(ctx, order, idem("u1")); err != nil || !replayed || again.JobsRemoved != 2 {
		t.Fatalf("replay: %+v replayed=%v err=%v", again, replayed, err)
	}
	if _, _, err := s.UnscheduleOrder(ctx, order, store.Idem{Key: "u1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("key reuse: %v", err)
	}
	if _, _, err := s.UnscheduleOrder(ctx, ids.New(), idem("u2")); problemCode(err) != store.CodeOrderHasNoOperations {
		t.Fatalf("unknown order: %v", err)
	}
	if _, _, err := s.UnscheduleOrder(tenantCtx(2), order, idem("u3")); problemCode(err) != store.CodeOrderHasNoOperations {
		t.Fatalf("other tenant: %v", err)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.order.unscheduled.v1"); got != 1 {
		t.Fatalf("order.unscheduled events = %d, want 1", got)
	}
	// The order can be scheduled again.
	if _, _, err := s.RunSchedule(ctx, store.RunRequest{Scope: "order", OrderID: order, Mode: "jobs", Finite: true, Start: at(0, 6, 0), HorizonDays: 60}, idem("r"+strconv.Itoa(1))); err != nil {
		t.Fatal(err)
	}
	if _, ok := barsOf(t, ctx, s)[op1.ID]; !ok {
		t.Fatal("operation 10 was not placed again")
	}
}

func TestOperationRaces_ConcurrentRetriesReplay(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	op := mustOperation(t, ctx, s, ids.New(), 10, 60, m.ID)
	const n = 6
	type res struct {
		replayed bool
		err      error
	}
	out := make(chan res, n)
	for i := 0; i < n; i++ {
		go func() {
			_, replayed, err := s.DeleteOperation(ctx, op.ID, 1, idem("race"))
			out <- res{replayed, err}
		}()
	}
	fresh := 0
	for i := 0; i < n; i++ {
		r := <-out
		if r.err != nil {
			t.Fatalf("racing retry failed: %v", r.err)
		}
		if !r.replayed {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatalf("%d requests deleted, want exactly 1", fresh)
	}
}

func TestUpdateOperation_ImportedChangeMakesAPlannersVersionStale(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	order := ids.New()
	if _, err := s.ImportProductionOrder(ctx, importReq(order, 1, pjob(10, 1, "process", m.ID, "1"))); err != nil {
		t.Fatal(err)
	}
	ops, err := s.ListOperations(ctx, store.OperationFilter{OrderID: order, Limit: 10})
	if err != nil || len(ops) != 1 || ops[0].Version != 1 {
		t.Fatalf("ops %+v err=%v", ops, err)
	}
	// Production re-dates the order while a planner has version 1 on screen.
	if _, err := s.ImportProductionOrder(ctx, importReq(order, 2, pjob(10, 1, "process", m.ID, "2"))); err != nil {
		t.Fatal(err)
	}
	mins := 45
	if _, _, err := s.UpdateOperation(ctx, ops[0].ID, 1, store.OperationPatch{Minutes: &mins}, idem("stale")); problemCode(err) != store.CodeOperationVersion {
		t.Fatalf("planner change over an imported one: %v, want SCHED-0137", err)
	}
	got, err := s.GetOperation(ctx, ops[0].ID)
	if err != nil || got.Version != 2 || got.Minutes != 120 || got.UpdatedAt == nil {
		t.Fatalf("after import: %+v err=%v, want version 2 and 120 minutes", got, err)
	}
	// An unchanged delivery leaves the version alone.
	if _, err := s.ImportProductionOrder(ctx, importReq(order, 3, pjob(10, 1, "process", m.ID, "2"))); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetOperation(ctx, ops[0].ID); err != nil || got.Version != 2 {
		t.Fatalf("unchanged import moved the version: %+v err=%v", got, err)
	}
}

func TestUpdateOperation_RetryAfterTheOperationWasDeletedReplays(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	op := mustOperation(t, ctx, s, ids.New(), 10, 60, m.ID)
	prio := 5
	first, _, err := s.UpdateOperation(ctx, op.ID, 1, store.OperationPatch{Priority: &prio}, idem("p"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DeleteOperation(ctx, op.ID, 2, idem("d")); err != nil {
		t.Fatal(err)
	}
	again, replayed, err := s.UpdateOperation(ctx, op.ID, 1, store.OperationPatch{Priority: &prio}, idem("p"))
	if err != nil || !replayed || again.Version != first.Version {
		t.Fatalf("retry after delete: %+v replayed=%v err=%v, want the first answer", again, replayed, err)
	}
	if _, _, err := s.UpdateOperation(ctx, op.ID, 2, store.OperationPatch{Priority: &prio}, idem("p2")); problemCode(err) != store.CodeOperationNotFound {
		t.Fatalf("new key on a deleted operation: %v", err)
	}
}
