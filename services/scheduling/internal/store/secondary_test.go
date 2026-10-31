package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func secOperation(t *testing.T, ctx context.Context, s *store.Postgres, oper, minutes int, primary string, secondary ...string) store.Operation {
	t.Helper()
	op, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: oper, Minutes: minutes, Priority: 100,
		ResourceIDs: []string{primary}, SecondaryResourceIDs: secondary}, store.Idem{})
	if err != nil {
		t.Fatalf("create operation: %v", err)
	}
	return op
}

func bookedMinutes(t *testing.T, ctx context.Context, s *store.Postgres, resourceID string) int {
	t.Helper()
	// Secondary bookings are visible through the load view, which counts them.
	loads, err := s.CapacityLoad(ctx, store.LoadFilter{ResourceIDs: []string{resourceID}, From: monday, Weeks: 1, ThresholdPct: 100, Consecutive: 1})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, l := range loads {
		total += l.BookedMinutes
	}
	return total
}

func TestSecondaryResources_BookedWithPrimaryAndNeverShared(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	m1, m2, tool := mustResource(t, ctx, s, "M1", true), mustResource(t, ctx, s, "M2", true), mustResource(t, ctx, s, "TOOL", true)
	a := secOperation(t, ctx, s, 10, 120, m1.ID, tool.ID)
	b := secOperation(t, ctx, s, 10, 60, m2.ID, tool.ID)
	if len(a.SecondaryResourceIDs) != 1 || a.SecondaryResourceIDs[0] != tool.ID {
		t.Fatalf("operation = %+v", a)
	}

	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	ba, bb := bars[a.ID], bars[b.ID]
	if ba.Start.Before(bb.End) && bb.Start.Before(ba.End) {
		t.Fatalf("both jobs hold TOOL at once: %v-%v and %v-%v", ba.Start, ba.End, bb.Start, bb.End)
	}
	if len(ba.SecondaryResourceIDs) != 1 || ba.SecondaryResourceIDs[0] != tool.ID {
		t.Fatalf("bar secondaries = %v", ba.SecondaryResourceIDs)
	}
	if evt := lastJobEvent(t, admin, a.ID); len(evt.SecondaryResourceIDs) != 1 || evt.SecondaryResourceIDs[0] != tool.ID {
		t.Fatalf("job.scheduled secondaries = %v", evt.SecondaryResourceIDs)
	}
	if got := bookedMinutes(t, ctx, s, tool.ID); got != 180 {
		t.Fatalf("TOOL booked minutes = %d, want 180", got)
	}

	// Running again replaces the secondary bookings instead of stacking them.
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := admin.QueryRow(context.Background(), `select count(*) from scheduled_job_secondary`).Scan(&n); err != nil || n != 2 {
		t.Fatalf("secondary rows = %d err=%v, want 2", n, err)
	}

	// Moving B onto A's slot is refused because TOOL is booked.
	out, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: b.ID, ResourceID: m2.ID, Start: ba.Start, HorizonDays: 30, DryRun: true}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Conflict == nil || out.Conflict.Code != domain.CodeSecondaryBusy {
		t.Fatalf("conflict = %+v, want SCHED-0005", out.Conflict)
	}

	// A committed move to a free slot rewrites the secondary booking with it.
	free := ba.End
	if bb.Start.Before(ba.Start) {
		free = bb.End
	}
	moved, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: b.ID, ResourceID: m2.ID, Start: free.Add(24 * time.Hour), HorizonDays: 30}, store.Idem{})
	if err != nil || moved.Conflict != nil {
		t.Fatalf("move: %v %+v", err, moved.Conflict)
	}
	if err := admin.QueryRow(context.Background(), `select count(*) from scheduled_job_secondary s join scheduled_job j on j.job_id = s.job_id and j.segment_no = s.segment_no
		where s.job_id = $1 and s.start_at = j.start_at and s.end_at = j.end_at`, b.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("moved secondary rows matching the primary = %d err=%v", n, err)
	}
}

func TestSecondaryResources_UnknownSecondaryIsRefused(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	_, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 30, Priority: 100,
		ResourceIDs: []string{m.ID}, SecondaryResourceIDs: []string{ids.New()}}, store.Idem{})
	if problemCode(err) != store.CodeUnknownResource {
		t.Fatalf("err = %v, want %s", err, store.CodeUnknownResource)
	}
	// Another tenant's resource does not exist for this tenant (RLS).
	other := mustResource(t, tenantCtx(2), s, "TOOL", true)
	_, _, err = s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 30, Priority: 100,
		ResourceIDs: []string{m.ID}, SecondaryResourceIDs: []string{other.ID}}, store.Idem{})
	if problemCode(err) != store.CodeUnknownResource {
		t.Fatalf("other tenant's resource: err = %v, want %s", err, store.CodeUnknownResource)
	}
}

func TestSecondaryResources_DowntimeOnASecondaryListsTheJob(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m, tool := mustResource(t, ctx, s, "M1", true), mustResource(t, ctx, s, "TOOL", true)
	a := secOperation(t, ctx, s, 10, 60, m.ID, tool.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bar := barsOf(t, ctx, s)[a.ID]
	res, _, err := s.CreateCapacityOverride(ctx, store.CapacityOverride{ResourceID: tool.ID, Kind: domain.OverrideDowntime, StartAt: bar.Start, EndAt: bar.End, Reason: "repair"}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.AffectedJobs) != 1 || res.AffectedJobs[0].JobID != a.ID {
		t.Fatalf("affected jobs = %+v, want the job that books TOOL", res.AffectedJobs)
	}
	// AC-23.7: the job's bar shows the violation although its primary is open.
	if after := barsOf(t, ctx, s)[a.ID]; !after.OutsideCapacity {
		t.Fatalf("bar = %+v, want outsideCapacity after a downtime on its secondary", after)
	}
}
