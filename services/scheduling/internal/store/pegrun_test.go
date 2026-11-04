package store_test

import (
	"context"
	"slices"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func TestRunSchedule_PlacesDependentBehindParent(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r1 := mustResource(t, ctx, s, "MIX-1", true)
	r2 := mustResource(t, ctx, s, "FILL-1", true)
	parentOrder, depOrder := ids.New(), ids.New()
	par := mustOperation(t, ctx, s, parentOrder, 10, 240, r1.ID)
	dep := mustOperation(t, ctx, s, depOrder, 10, 60, r2.ID)
	if _, _, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: parentOrder, DependentOrderID: depOrder}, store.Idem{}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	if !bars[dep.ID].Start.Equal(bars[par.ID].End) {
		t.Fatalf("dependent %v-%v, parent %v-%v: dependent must start when the parent ends", bars[dep.ID].Start, bars[dep.ID].End, bars[par.ID].Start, bars[par.ID].End)
	}

	// Re-run only the parent's successor: the parent stays where it is outside the selection.
	req := store.RunRequest{Scope: "selection", Mode: "jobs", Finite: true, Start: at(0, 6, 0), HorizonDays: 60, OperationIDs: []string{dep.ID}}
	if _, _, err := s.RunSchedule(ctx, req, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if again := barsOf(t, ctx, s); again[dep.ID].Start.Before(again[par.ID].End) {
		t.Fatalf("selection run put the dependent at %v before the parent ends %v", again[dep.ID].Start, again[par.ID].End)
	}
}

func TestRunSchedule_DependentOfUnplacedParentIsUnplaced(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r1 := mustResource(t, ctx, s, "MIX-1", true)
	r2 := mustResource(t, ctx, s, "FILL-1", true)
	parentOrder, depOrder := ids.New(), ids.New()
	// 100,000 minutes do not fit the 60-day horizon of an 8-hour weekday calendar.
	mustOperation(t, ctx, s, parentOrder, 10, 100000, r1.ID)
	dep := mustOperation(t, ctx, s, depOrder, 10, 60, r2.ID)
	if _, _, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: parentOrder, DependentOrderID: depOrder}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	want := dep.ID + ": pegged parent order " + parentOrder + " is unplaced"

	run, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(run.Messages, want) {
		t.Fatalf("site run messages = %v, want %q", run.Messages, want)
	}

	// A selection run of the dependent alone reads the parent's stored status.
	req := store.RunRequest{Scope: "selection", Mode: "jobs", Finite: true, Start: at(0, 6, 0), HorizonDays: 60, OperationIDs: []string{dep.ID}}
	again, _, err := s.RunSchedule(ctx, req, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(again.Messages, want) {
		t.Fatalf("selection run messages = %v, want %q", again.Messages, want)
	}
	if _, placed := barsOf(t, ctx, s)[dep.ID]; placed {
		t.Fatal("the dependent of an unplaced parent must hold no job")
	}
}

func TestRunSchedule_OrderScopeSchedulesOnlyThatOrder(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r1 := mustResource(t, ctx, s, "MIX-1", true)
	orderA, orderB := ids.New(), ids.New()
	a1 := mustOperation(t, ctx, s, orderA, 10, 60, r1.ID)
	a2 := mustOperation(t, ctx, s, orderA, 20, 60, r1.ID)
	b1 := mustOperation(t, ctx, s, orderB, 10, 60, r1.ID)

	req := store.RunRequest{Scope: "order", OrderID: orderA, Mode: "jobs", Finite: true, Start: at(0, 6, 0), HorizonDays: 60}
	run, created, err := s.RunSchedule(ctx, req, store.Idem{Key: "k1", Fingerprint: "f"})
	if err != nil || !created {
		t.Fatalf("run = %+v created %v err %v", run, created, err)
	}
	if run.Scope != "order" || run.OperationsCount != 2 {
		t.Fatalf("scope %q operations %d, want order and 2", run.Scope, run.OperationsCount)
	}
	bars := barsOf(t, ctx, s)
	if _, ok := bars[a1.ID]; !ok {
		t.Fatal("first operation of the order must be placed")
	}
	if _, ok := bars[a2.ID]; !ok {
		t.Fatal("second operation of the order must be placed")
	}
	if _, ok := bars[b1.ID]; ok {
		t.Fatal("another order must not be scheduled")
	}
	var scope string
	if err := admin.QueryRow(context.Background(), `select payload->'data'->>'scope' from outbox where type = $1 and aggregate_id = $2`,
		"dev.passage.scheduling.run.completed.v1", run.ID).Scan(&scope); err != nil || scope != "order" {
		t.Fatalf("run.completed scope = %q err %v, want order", scope, err)
	}
	again, created, err := s.RunSchedule(ctx, req, store.Idem{Key: "k1", Fingerprint: "f"})
	if err != nil || created || again.ID != run.ID {
		t.Fatalf("replay = %+v created %v err %v, want the first run", again, created, err)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.run.completed.v1"); got != 1 {
		t.Fatalf("a replay must not publish again: run.completed events = %d", got)
	}

	// A firmed job of the order stays where it is; the next order run places
	// only the other operation.
	firmedAt := bars[a1.ID]
	if _, _, err := s.FirmJob(ctx, a1.ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	rerun, _, err := s.RunSchedule(ctx, req, store.Idem{Key: "k2", Fingerprint: "f"})
	if err != nil || rerun.OperationsCount != 1 {
		t.Fatalf("rerun = %+v err %v, want one operation (the firmed one stays)", rerun, err)
	}
	if after := barsOf(t, ctx, s)[a1.ID]; !after.Start.Equal(firmedAt.Start) {
		t.Fatalf("firmed job moved from %s to %s", firmedAt.Start, after.Start)
	}

	req.OrderID = ids.New()
	if _, _, err := s.RunSchedule(ctx, req, store.Idem{}); problemCode(err) != store.CodeOrderHasNoOperations {
		t.Fatalf("unknown order error = %v, want %s", err, store.CodeOrderHasNoOperations)
	}
}
