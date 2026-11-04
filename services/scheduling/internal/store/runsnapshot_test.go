package store_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// A run keeps what it placed; comparing two runs lists the jobs that moved
// and rolling back to the first restores its plan under a new run.
func TestRunSnapshots_CompareAndRollback(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	m1 := mustResource(t, ctx, s, "M1", true)
	o1, o2 := ids.New(), ids.New()
	a := mustOperation(t, ctx, s, o1, 10, 60, m1.ID)
	b := mustOperation(t, ctx, s, o2, 10, 60, m1.ID)
	run1, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil || !run1.Snapshot {
		t.Fatalf("run1 = %+v err=%v, want a snapshot", run1, err)
	}
	first := barsOf(t, ctx, s)

	// A higher priority operation arrives; the second site run puts it first.
	c, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 120, Priority: 1, ResourceIDs: []string{m1.ID}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	run2, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	second := barsOf(t, ctx, s)
	if second[a.ID].Start.Equal(first[a.ID].Start) && second[b.ID].Start.Equal(first[b.ID].Start) {
		t.Fatalf("test setup: the second run moved nothing: %v", second)
	}

	cmp, err := s.CompareRuns(ctx, run1.ID, run2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cmp.Base.Measures.Jobs != 2 || cmp.Other.Measures.Jobs != 3 || cmp.AddedCount != 1 || cmp.Added[0].JobID != c.ID ||
		cmp.RemovedCount != 0 || cmp.ChangedCount+cmp.UnchangedCount != 2 || cmp.ChangedCount == 0 {
		t.Fatalf("comparison = %+v", cmp)
	}
	for _, ch := range cmp.Changed {
		if ch.StartShiftMinutes <= 0 {
			t.Errorf("job %s shift = %d, want it later in the second run", ch.JobID, ch.StartShiftMinutes)
		}
	}
	if _, err := s.CompareRuns(ctx, run1.ID, ids.New()); problemCode(err) != store.CodeRunNotFound {
		t.Fatalf("unknown run: %v, want SCHED-0104", err)
	}

	// Rolling back to run 1 cannot restore a and b while c holds their slots...
	// c is not part of run 1, so it stays where run 2 put it: the restore overlaps it.
	if _, _, err := s.RollbackRun(ctx, run1.ID, idem("rb0")); problemCode(err) != store.CodeRollbackConflict {
		t.Fatalf("rollback over c: %v, want SCHED-0144", err)
	}
	if got := barsOf(t, ctx, s); !got[a.ID].Start.Equal(second[a.ID].Start) {
		t.Fatalf("a failed rollback changed the plan: %v", got[a.ID])
	}

	// Taking c off the schedule frees its slot; the rollback then succeeds.
	if _, _, err := s.UnscheduleOrder(ctx, c.OrderID, idem("un")); err != nil {
		t.Fatal(err)
	}
	out, replayed, err := s.RollbackRun(ctx, run1.ID, idem("rb1"))
	if err != nil || replayed || out.Restored != 2 || len(out.Skipped) != 0 || out.Run.Scope != "rollback" || out.Run.RollbackOf == nil || *out.Run.RollbackOf != run1.ID || !out.Run.Snapshot {
		t.Fatalf("rollback = %+v replayed=%v err=%v", out, replayed, err)
	}
	after := barsOf(t, ctx, s)
	for _, id := range []string{a.ID, b.ID} {
		if !after[id].Start.Equal(first[id].Start) || !after[id].End.Equal(first[id].End) || after[id].ResourceID != first[id].ResourceID {
			t.Errorf("job %s = %v..%v, want %v..%v", id, after[id].Start, after[id].End, first[id].Start, first[id].End)
		}
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.run.completed.v1"); got != 3 {
		t.Fatalf("run.completed events = %d, want 3 (two runs and the rollback)", got)
	}

	// The same key replays; the rollback run is itself comparable and equals run 1.
	again, replayed, err := s.RollbackRun(ctx, run1.ID, idem("rb1"))
	if err != nil || !replayed || again.Run.ID != out.Run.ID {
		t.Fatalf("replay = %+v replayed=%v err=%v", again, replayed, err)
	}
	same, err := s.CompareRuns(ctx, run1.ID, out.Run.ID)
	if err != nil || same.ChangedCount+same.AddedCount+same.RemovedCount != 0 || same.UnchangedCount != 2 {
		t.Fatalf("run1 against its rollback = %+v err=%v", same, err)
	}
}

func TestRollbackRun_SkipsFirmedAndMissingAndRefusesMoveRuns(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m1 := mustResource(t, ctx, s, "M1", true)
	a := mustOperation(t, ctx, s, ids.New(), 10, 60, m1.ID)
	b := mustOperation(t, ctx, s, ids.New(), 10, 60, m1.ID)
	c := mustOperation(t, ctx, s, ids.New(), 10, 60, m1.ID)
	run1, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	first := barsOf(t, ctx, s)
	// Re-run later in the week so the jobs move away from where run 1 put them.
	if _, _, err := s.RunSchedule(ctx, siteRun(at(1, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if barsOf(t, ctx, s)[b.ID].Start.Equal(first[b.ID].Start) {
		t.Fatal("test setup: the second run did not move b")
	}
	if _, _, err := s.FirmJob(ctx, a.ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DeleteOperation(ctx, c.ID, c.Version, idem("dc")); err != nil {
		t.Fatal(err)
	}
	out, _, err := s.RollbackRun(ctx, run1.ID, idem("rb"))
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]string{}
	for _, sk := range out.Skipped {
		reasons[sk.JobID] = sk.Reason
	}
	if out.Restored != 1 || reasons[a.ID] != store.SkipFirmed || reasons[c.ID] != store.SkipOperationMissing || len(reasons) != 2 {
		t.Fatalf("rollback = %+v, want b restored, a skipped as firmed and c as operation_missing", out)
	}
	if got := barsOf(t, ctx, s); !got[b.ID].Start.Equal(first[b.ID].Start) {
		t.Fatalf("b = %v, want it back where run 1 put it (%v)", got[b.ID].Start, first[b.ID].Start)
	}

	// A move run keeps no snapshot.
	mv, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: b.ID, ResourceID: m1.ID, Start: at(3, 8, 0), HorizonDays: 60}, idem("mv"))
	if err != nil || mv.Run == nil {
		t.Fatalf("move did not commit a run (%+v, %v)", mv, err)
	}
	if _, err := s.CompareRuns(ctx, run1.ID, mv.Run.ID); problemCode(err) != store.CodeRunNoSnapshot {
		t.Fatalf("compare with a move run: %v, want SCHED-0143", err)
	}
	if _, _, err := s.RollbackRun(ctx, mv.Run.ID, idem("rbm")); problemCode(err) != store.CodeRunNoSnapshot {
		t.Fatalf("rollback to a move run: %v, want SCHED-0143", err)
	}
	if _, _, err := s.RollbackRun(ctx, ids.New(), idem("rbu")); problemCode(err) != store.CodeRunNotFound {
		t.Fatalf("rollback to an unknown run: %v, want SCHED-0104", err)
	}
}

// RLS: a run of another tenant is not found.
func TestRunSnapshots_AreTenantScoped(t *testing.T) {
	s, _, _ := setup(t)
	ctx1, ctx2 := tenantCtx(1), tenantCtx(2)
	m1 := mustResource(t, ctx1, s, "M1", true)
	mustOperation(t, ctx1, s, ids.New(), 10, 60, m1.ID)
	run, _, err := s.RunSchedule(ctx1, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RollbackRun(ctx2, run.ID, idem("x")); problemCode(err) != store.CodeRunNotFound {
		t.Fatalf("other tenant rollback: %v, want SCHED-0104", err)
	}
}

// Rollback re-checks the plan it would leave: an operation changed since the
// run is skipped, and a restored job in a downtime added since, or out of
// sequence with a firmed job of its order, refuses the whole rollback.
func TestRollbackRun_RevalidatesAgainstThePlanAsItIsNow(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m1 := mustResource(t, ctx, s, "M1", true)
	m2 := mustResource(t, ctx, s, "M2", true)
	order := ids.New()
	a := mustOperation(t, ctx, s, order, 10, 60, m1.ID)
	b := mustOperation(t, ctx, s, order, 20, 60, m2.ID)
	solo := mustOperation(t, ctx, s, ids.New(), 10, 60, m2.ID)
	run1, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	first := barsOf(t, ctx, s)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(2, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	second := barsOf(t, ctx, s)

	// A downtime over where run 1 put a: nothing changes.
	down := store.CapacityOverride{ResourceID: m1.ID, Kind: domain.OverrideDowntime, StartAt: first[a.ID].Start, EndAt: first[a.ID].End, Reason: "repair"}
	ov, _, err := s.CreateCapacityOverride(ctx, down, idem("down"))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.RollbackRun(ctx, run1.ID, idem("rb-down"))
	var p *httpx.Problem
	if !errors.As(err, &p) || p.Code != store.CodeRollbackBreaks || p.Status != 409 || !strings.Contains(p.Errors[a.ID], "outside the open capacity") {
		t.Fatalf("rollback into a downtime: %v (%+v), want 409 SCHED-0146 naming a", err, p)
	}
	if got := barsOf(t, ctx, s); !got[a.ID].Start.Equal(second[a.ID].Start) || !got[b.ID].Start.Equal(second[b.ID].Start) {
		t.Fatal("a refused rollback changed the plan")
	}
	if err := s.DeleteCapacityOverride(ctx, m1.ID, ov.ID); err != nil {
		t.Fatal(err)
	}

	// a firmed where run 2 put it (day 2): b restored to day 0 would run before a.
	if _, _, err := s.FirmJob(ctx, a.ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.RollbackRun(ctx, run1.ID, idem("rb-seq"))
	if !errors.As(err, &p) || p.Code != store.CodeRollbackBreaks || p.Errors[b.ID] == "" {
		t.Fatalf("rollback of b ahead of firmed a: %v (%+v), want SCHED-0146 naming b", err, p)
	}
	if _, _, err := s.FirmJob(ctx, a.ID, false, store.Idem{}); err != nil {
		t.Fatal(err)
	}

	// solo's minutes change after run 1: its old placement is out of date and
	// stays off; a and b come back where run 1 put them.
	cur, err := s.GetOperation(ctx, solo.ID)
	if err != nil {
		t.Fatal(err)
	}
	mins := 90
	if _, _, err := s.UpdateOperation(ctx, solo.ID, cur.Version, store.OperationPatch{Minutes: &mins}, idem("patch")); err != nil {
		t.Fatal(err)
	}
	out, _, err := s.RollbackRun(ctx, run1.ID, idem("rb-ok"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Restored != 2 || len(out.Skipped) != 1 || out.Skipped[0].JobID != solo.ID || out.Skipped[0].Reason != store.SkipOperationChanged {
		t.Fatalf("rollback = %+v, want a and b restored and solo skipped as operation_changed", out)
	}
	got := barsOf(t, ctx, s)
	if !got[a.ID].Start.Equal(first[a.ID].Start) || !got[b.ID].Start.Equal(first[b.ID].Start) {
		t.Fatalf("a, b = %v, %v; want run 1's %v, %v", got[a.ID].Start, got[b.ID].Start, first[a.ID].Start, first[b.ID].Start)
	}
	if _, ok := got[solo.ID]; ok {
		t.Fatalf("solo = %+v, want it left off the schedule", got[solo.ID])
	}
}

// The comparison carries each run's utilization of the resources it booked.
func TestCompareRuns_Utilization(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m1 := mustResource(t, ctx, s, "M1", true)
	mustOperation(t, ctx, s, ids.New(), 10, 240, m1.ID)
	mustOperation(t, ctx, s, ids.New(), 10, 240, m1.ID)
	run1, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	run2, _, err := s.RunSchedule(ctx, siteRun(at(0, 12, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	cmp, err := s.CompareRuns(ctx, run1.ID, run2.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Run 1 fills Monday 08:00-16:00; run 2 runs 12:00-16:00 and Tuesday 08:00-12:00,
	// with Monday 16:00 to Tuesday 08:00 closed in its span.
	if b := cmp.Base.Measures; b.BookedMinutes != 480 || b.OpenMinutes != 480 || b.UtilizationPct != 100 {
		t.Fatalf("base measures = %+v, want 480 of 480 min, 100%%", b)
	}
	if o := cmp.Other.Measures; o.BookedMinutes != 480 || o.OpenMinutes != 480 || o.UtilizationPct != 100 {
		t.Fatalf("other measures = %+v, want 480 of 480 min", o)
	}
}

// A dependent order restored ahead of where its parent order now ends (the
// parent firmed where a later run put it) refuses the rollback.
func TestRollbackRun_RefusesToBreakAPeg(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m1 := mustResource(t, ctx, s, "M1", true)
	m2 := mustResource(t, ctx, s, "M2", true)
	parent, dependent := ids.New(), ids.New()
	p := mustOperation(t, ctx, s, parent, 10, 60, m1.ID)
	d := mustOperation(t, ctx, s, dependent, 10, 60, m2.ID)
	if _, _, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: parent, DependentOrderID: dependent}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	run1, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(2, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FirmJob(ctx, p.ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.RollbackRun(ctx, run1.ID, idem("rb"))
	var pr *httpx.Problem
	if !errors.As(err, &pr) || pr.Code != store.CodeRollbackBreaks || !strings.Contains(pr.Errors[d.ID], "parent order") {
		t.Fatalf("rollback of the dependent ahead of its parent: %v (%+v), want SCHED-0146 naming d", err, pr)
	}
}
