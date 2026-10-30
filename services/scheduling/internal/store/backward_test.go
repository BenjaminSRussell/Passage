package store_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func dueOperation(t *testing.T, s *store.Postgres, ctxOrder string, minutes int, due time.Time, resource string) store.Operation {
	t.Helper()
	op, _, err := s.CreateOperation(tenantCtx(1), store.Operation{OrderID: ctxOrder, OperNumber: 10, Minutes: minutes, Priority: 100, DueAt: &due, ResourceIDs: []string{resource}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func TestRunSchedule_BackwardPlacesAtTheLatestStartAndRecordsDirection(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	op := dueOperation(t, s, ids.New(), 240, at(2, 12, 0), r.ID)

	req := siteRun(at(0, 6, 0))
	req.Backward = true
	run, _, err := s.RunSchedule(ctx, req, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Direction != "backward" || run.TotalLatenessMinutes != 0 || len(run.Warnings) != 0 {
		t.Fatalf("run = %+v, want backward and on time", run)
	}
	if stored, err := s.GetRun(ctx, run.ID); err != nil || stored.Direction != "backward" {
		t.Fatalf("stored run direction = %q (err %v), want backward", stored.Direction, err)
	}
	bar := barsOf(t, ctx, s)[op.ID]
	if !bar.Start.Equal(at(2, 8, 0)) || !bar.End.Equal(at(2, 12, 0)) {
		t.Fatalf("bar %v-%v, want Wednesday 08:00-12:00", bar.Start, bar.End)
	}
}

func TestRunSchedule_BackwardFallsBackToForwardWithSched0003(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	op := dueOperation(t, s, ids.New(), 420, at(0, 12, 0), r.ID)

	req := siteRun(at(0, 6, 0))
	req.Backward = true
	run, _, err := s.RunSchedule(ctx, req, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if run.TotalLatenessMinutes <= 0 || len(run.Warnings) != 1 || run.Warnings[0].Code != "SCHED-0003" {
		t.Fatalf("run = %+v, want late with SCHED-0003", run)
	}
	if bar := barsOf(t, ctx, s)[op.ID]; !bar.Start.Equal(at(0, 8, 0)) {
		t.Fatalf("fallback start %v, want Monday 08:00", bar.Start)
	}
}
