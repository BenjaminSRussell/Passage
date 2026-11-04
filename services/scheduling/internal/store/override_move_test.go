package store_test

import (
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// A move into closed time is refused, and committed once it overrides the
// constraints: the job is one span over the closed time, flagged outside
// capacity on the Gantt, the violation is in the answer and in the move run's
// messages, and a replay returns the same answer. Double booking still refuses.
func TestMoveJob_OverrideCommitsOverClosedTimeButNotOverABooking(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	a := mustOperation(t, ctx, s, ids.New(), 10, 60, r.ID)
	b := mustOperation(t, ctx, s, ids.New(), 10, 120, r.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	req := store.MoveRequest{JobID: b.ID, Start: at(0, 17, 0), HorizonDays: 60}
	if out, _, err := s.MoveJob(ctx, req, store.Idem{}); err != nil || out.Conflict == nil || out.Conflict.Code != domain.CodeOutsideCapacity {
		t.Fatalf("without override: %+v err=%v", out, err)
	}
	req.Override, req.DryRun = true, true
	preview, _, err := s.MoveJob(ctx, req, store.Idem{})
	if err != nil || preview.Conflict != nil || len(preview.Overridden) != 1 || preview.Run != nil {
		t.Fatalf("override preview: %+v err=%v", preview, err)
	}
	if bars := barsOf(t, ctx, s); bars[b.ID].Start.Equal(at(0, 17, 0)) {
		t.Fatal("preview moved the job")
	}
	req.DryRun = false
	idem := store.Idem{Key: "ov-1", Fingerprint: "fp"}
	out, replayed, err := s.MoveJob(ctx, req, idem)
	if err != nil || replayed || out.Conflict != nil || len(out.Overridden) != 1 || out.Overridden[0].Code != domain.CodeOutsideCapacity || out.Run == nil {
		t.Fatalf("override commit: %+v replayed=%v err=%v", out, replayed, err)
	}
	if len(out.Run.Messages) != 1 || !strings.Contains(out.Run.Messages[0], "SCHED-0002 overridden for job "+b.ID) {
		t.Fatalf("run messages = %v", out.Run.Messages)
	}
	moved := barsOf(t, ctx, s)[b.ID]
	if !moved.Start.Equal(at(0, 17, 0)) || !moved.End.Equal(at(0, 19, 0)) || !moved.OutsideCapacity {
		t.Fatalf("bar = %+v, want 17:00-19:00 flagged outside capacity", moved)
	}
	again, replayed, err := s.MoveJob(ctx, req, idem)
	if err != nil || !replayed || len(again.Overridden) != 1 {
		t.Fatalf("replay: %+v replayed=%v err=%v", again, replayed, err)
	}
	// Onto a's booking: never overridden.
	onto := store.MoveRequest{JobID: b.ID, Start: barsOf(t, ctx, s)[a.ID].Start, HorizonDays: 60, Override: true}
	if out, _, err := s.MoveJob(ctx, onto, store.Idem{}); err != nil || out.Conflict == nil || out.Conflict.Code != domain.CodeOverlap {
		t.Fatalf("override onto a booking: %+v err=%v", out, err)
	}
}

// The Gantt marks a bar that sits in open capacity a block now keeps from its
// attributes, and only that bar; a bar outside the calendar carries
// outsideCapacity instead.
func TestGantt_MarksBlockBreaches(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "PAINT-1", true)
	b, _, err := s.CreateCapacityBlock(ctx, store.CapacityBlock{ResourceID: r.ID, Code: "MON-WHITE",
		Windows:   []domain.Shift{{Weekday: 1, StartMinute: 8 * 60, EndMinute: 12 * 60}},
		Condition: []domain.Predicate{{Attribute: "color", Op: "=", Value: "white"}}, Priority: 10, Active: true}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	jobs := map[string]string{}
	for _, color := range []string{"white", "red"} {
		op, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 120, Priority: 100,
			Attributes: map[string]string{"color": color}, ResourceIDs: []string{r.ID}}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		jobs[color] = op.ID
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	if bars[jobs["white"]].BlockBreach || bars[jobs["red"]].BlockBreach {
		t.Fatalf("a fresh schedule has no breach: %+v %+v", bars[jobs["white"]], bars[jobs["red"]])
	}
	// The block now admits black only: the white job sits in the block's time.
	black := []domain.Predicate{{Attribute: "color", Op: "=", Value: "black"}}
	if _, _, err := s.UpdateCapacityBlock(ctx, b.ID, 1, store.BlockPatch{Condition: &black}, at(0, 0, 0), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars = barsOf(t, ctx, s)
	if !bars[jobs["white"]].BlockBreach || bars[jobs["white"]].OutsideCapacity {
		t.Fatalf("white bar = %+v, want a block breach inside open capacity", bars[jobs["white"]])
	}
	if bars[jobs["red"]].BlockBreach {
		t.Fatalf("red bar = %+v, placed in unblocked time", bars[jobs["red"]])
	}
	// Retiring the block frees its time again.
	off := false
	if _, _, err := s.UpdateCapacityBlock(ctx, b.ID, 2, store.BlockPatch{Active: &off}, at(0, 0, 0), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if bars = barsOf(t, ctx, s); bars[jobs["white"]].BlockBreach {
		t.Fatalf("retired block still marks %+v", bars[jobs["white"]])
	}
}
