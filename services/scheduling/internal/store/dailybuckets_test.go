package store_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func TestRunSchedule_OperationsModeStartsFollowersOnTheNextDay(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	order := ids.New()
	a := mustOperation(t, ctx, s, order, 10, 60, r.ID)
	b := mustOperation(t, ctx, s, order, 20, 60, r.ID)

	req := siteRun(at(0, 6, 0))
	req.Mode = "operations"
	run, _, err := s.RunSchedule(ctx, req, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if run.Mode != "operations" {
		t.Fatalf("run mode = %q", run.Mode)
	}
	bars := barsOf(t, ctx, s)
	if !bars[a.ID].Start.Equal(at(0, 8, 0)) || !bars[b.ID].Start.Equal(at(1, 8, 0)) || !bars[b.ID].End.Equal(at(1, 9, 0)) {
		t.Fatalf("a %v, b %v-%v: want Monday 08:00 then Tuesday 08:00-09:00", bars[a.ID].Start, bars[b.ID].Start, bars[b.ID].End)
	}
}

// A move honours the mode of the run that placed the job: in operations mode
// the follower dropped on its predecessor's day lands on the next day, the
// successor behind it follows by whole days, and the move's run records the
// mode so a later move keeps it. A job placed in jobs mode moves as before.
func TestMoveJob_HonoursTheModeOfTheRunThatPlacedTheJob(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	order := ids.New()
	a := mustOperation(t, ctx, s, order, 10, 60, r.ID)
	b := mustOperation(t, ctx, s, order, 20, 60, r.ID)
	c := mustOperation(t, ctx, s, order, 30, 60, r.ID)
	req := siteRun(at(0, 6, 0))
	req.Mode = "operations"
	if _, _, err := s.RunSchedule(ctx, req, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	out, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: b.ID, Start: at(0, 10, 0), HorizonDays: 60}, store.Idem{})
	if err != nil || out.Conflict != nil || out.Run == nil || out.Run.Mode != "operations" {
		t.Fatalf("move: %+v err=%v", out, err)
	}
	bars := barsOf(t, ctx, s)
	if !bars[a.ID].Start.Equal(at(0, 8, 0)) || !bars[b.ID].Start.Equal(at(1, 8, 0)) || !bars[c.ID].Start.Equal(at(2, 8, 0)) {
		t.Fatalf("a %v b %v c %v: want Mon, Tue, Wed 08:00", bars[a.ID].Start, bars[b.ID].Start, bars[c.ID].Start)
	}
	// The move's own run carries the mode, so the next move keeps it.
	out, _, err = s.MoveJob(ctx, store.MoveRequest{JobID: b.ID, Start: at(2, 9, 0), HorizonDays: 60}, store.Idem{})
	if err != nil || out.Conflict != nil {
		t.Fatalf("second move: %+v err=%v", out, err)
	}
	if bars = barsOf(t, ctx, s); !bars[c.ID].Start.Equal(at(3, 8, 0)) {
		t.Fatalf("c = %v, want Thursday 08:00", bars[c.ID].Start)
	}

	jobsOrder := ids.New()
	x := mustOperation(t, ctx, s, jobsOrder, 10, 60, r.ID)
	y := mustOperation(t, ctx, s, jobsOrder, 20, 60, r.ID)
	if _, _, err := s.RunSchedule(ctx, store.RunRequest{Scope: "order", OrderID: jobsOrder, Mode: "jobs", Finite: true, Start: at(7, 6, 0), HorizonDays: 60}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars = barsOf(t, ctx, s)
	if !bars[y.ID].Start.Equal(bars[x.ID].End) {
		t.Fatalf("jobs mode chains x %v y %v", bars[x.ID].End, bars[y.ID].Start)
	}
	if out, _, err = s.MoveJob(ctx, store.MoveRequest{JobID: y.ID, Start: bars[x.ID].End.Add(time.Hour), HorizonDays: 60}, store.Idem{}); err != nil || out.Conflict != nil || out.Run.Mode != "jobs" {
		t.Fatalf("jobs move: %+v err=%v", out, err)
	}
	if got := barsOf(t, ctx, s)[y.ID].Start; !got.Equal(bars[x.ID].End.Add(time.Hour)) {
		t.Fatalf("jobs mode move kept on the same day: %v", got)
	}
}
