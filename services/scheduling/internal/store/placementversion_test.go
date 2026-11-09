package store_test

import (
	"sync"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// A stale placement version is refused for moves and firms and changes nothing;
// the current one is accepted, and every writer moves it on.
func TestPlacementVersion_StaleMoveAndFirmAreRefused(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	order, other := ids.New(), ids.New()
	op1 := mustOperation(t, ctx, s, order, 10, 60, r.ID)
	op2 := mustOperation(t, ctx, s, order, 20, 60, r.ID)
	mustOperation(t, ctx, s, other, 10, 60, r.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	v1 := bars[op1.ID].PlacementVersion
	if v1 == "" || v1 != bars[op2.ID].PlacementVersion {
		t.Fatalf("versions %q / %q, want one non-empty token for the order", v1, bars[op2.ID].PlacementVersion)
	}
	if o := bars[op1.ID].OrderID; o != order {
		t.Fatalf("order = %s", o)
	}
	for _, b := range bars {
		if b.OrderID == other && b.PlacementVersion == v1 {
			t.Fatal("two orders share a placement version")
		}
	}

	// A preview with a wrong version is refused; the right one passes.
	req := store.MoveRequest{JobID: op1.ID, Start: at(1, 10, 0), HorizonDays: 60, DryRun: true, IfMatch: `"deadbeef"`}
	if _, _, err := s.MoveJob(ctx, req, store.Idem{}); problemCode(err) != store.CodePlacementVersion {
		t.Fatalf("stale preview: err=%v, want %s", err, store.CodePlacementVersion)
	}
	req.IfMatch = `"` + v1 + `"`
	req.DryRun = false
	if _, _, err := s.MoveJob(ctx, req, store.Idem{}); err != nil {
		t.Fatalf("move with the current version: %v", err)
	}
	v2 := barsOf(t, ctx, s)[op1.ID].PlacementVersion
	if v2 == v1 {
		t.Fatal("the move did not change the placement version")
	}

	// The old version no longer moves or firms, and nothing was written.
	req.Start = at(2, 10, 0)
	if _, _, err := s.MoveJob(ctx, req, store.Idem{}); problemCode(err) != store.CodePlacementVersion {
		t.Fatalf("stale move: err=%v, want %s", err, store.CodePlacementVersion)
	}
	if _, _, err := s.FirmJobIfMatch(ctx, op1.ID, true, v1, store.Idem{}); problemCode(err) != store.CodePlacementVersion {
		t.Fatalf("stale firm: err=%v, want %s", err, store.CodePlacementVersion)
	}
	if b := barsOf(t, ctx, s)[op1.ID]; b.Firmed || !b.Start.Equal(at(1, 10, 0)) {
		t.Fatalf("a refused command wrote: %+v", b)
	}
	if _, _, err := s.FirmJobIfMatch(ctx, op1.ID, true, v2, store.Idem{}); err != nil {
		t.Fatalf("firm with the current version: %v", err)
	}
	if v3 := barsOf(t, ctx, s)[op1.ID].PlacementVersion; v3 == v2 {
		t.Fatal("firming did not change the placement version")
	}
}

// A firm and a move of the same order sent with the same placement version at
// the same time serialize on the order: one is applied and the other is
// refused as stale, never both.
func TestPlacementVersion_ConcurrentFirmAndMoveOneWins(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	var jobs []string
	for range 6 {
		jobs = append(jobs, mustOperation(t, ctx, s, ids.New(), 10, 60, r.ID).ID)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	for i, job := range jobs {
		v := bars[job].PlacementVersion
		errs := make([]error, 2)
		var moved store.MoveOutcome
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _, errs[0] = s.FirmJobIfMatch(ctx, job, true, v, store.Idem{})
		}()
		go func() {
			defer wg.Done()
			moved, _, errs[1] = s.MoveJob(ctx, store.MoveRequest{JobID: job, Start: at(2, 8+i, 0), HorizonDays: 60, IfMatch: v}, store.Idem{})
		}()
		wg.Wait()
		if moved.Conflict != nil {
			t.Fatalf("job %d: move planned a conflict %+v; pick a free slot", i, moved.Conflict)
		}
		ok := 0
		for _, err := range errs {
			switch {
			case err == nil:
				ok++
			case problemCode(err) != store.CodePlacementVersion:
				t.Fatalf("job %d: err=%v, want nil or %s", i, err, store.CodePlacementVersion)
			}
		}
		if ok != 1 {
			t.Fatalf("job %d: firm err=%v, move err=%v; want exactly one applied", i, errs[0], errs[1])
		}
	}
}
