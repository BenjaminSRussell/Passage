package store_test

import (
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
