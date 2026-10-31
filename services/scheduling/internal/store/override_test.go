package store_test

import (
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// A downtime lists the booked jobs it overlaps (AC-23.7); the next run lays
// work around it, an extra shift adds capacity, and removing the override
// restores the calendar. Overrides are per tenant.
func TestCapacityOverrides_DowntimeAffectsJobsAndRuns(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	order := ids.New()
	op := mustOperation(t, ctx, s, order, 10, 120, r.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if b := barsOf(t, ctx, s)[op.ID]; !b.Start.Equal(at(0, 8, 0)) {
		t.Fatalf("before the downtime the job starts %v, want 08:00", b.Start)
	}

	down := store.CapacityOverride{ResourceID: r.ID, Kind: domain.OverrideDowntime, StartAt: at(0, 8, 0), EndAt: at(0, 12, 0), Reason: "press repair"}
	res, created, err := s.CreateCapacityOverride(ctx, down, store.Idem{Key: "ov-1", Fingerprint: "fp"})
	if err != nil || !created || len(res.AffectedJobs) != 1 || res.AffectedJobs[0].JobID != op.ID {
		t.Fatalf("downtime = %+v created=%v err=%v, want the booked job listed", res, created, err)
	}
	if again, created, err := s.CreateCapacityOverride(ctx, down, store.Idem{Key: "ov-1", Fingerprint: "fp"}); err != nil || created || again.ID != res.ID {
		t.Fatalf("replay = %+v created=%v err=%v", again, created, err)
	}
	if _, _, err := s.CreateCapacityOverride(ctx, down, store.Idem{Key: "ov-1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key: err=%v", err)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.capacityoverride.created.v1"); got != 1 {
		t.Fatalf("capacityoverride.created events = %d, want 1 (a replay writes none)", got)
	}
	if _, _, err := s.CreateCapacityOverride(ctx, store.CapacityOverride{ResourceID: ids.New(), Kind: domain.OverrideExtra, StartAt: at(0, 8, 0), EndAt: at(0, 9, 0)}, store.Idem{}); problemCode(err) != store.CodeResourceNotFound {
		t.Fatalf("unknown resource: err=%v", err)
	}

	// Until it is moved, the booked job shows as outside open capacity on the
	// Gantt (SCHED-0002, AC-23.7).
	if b := barsOf(t, ctx, s)[op.ID]; !b.OutsideCapacity {
		t.Fatalf("bar in the downtime = %+v, want outsideCapacity", b)
	}

	capacity, err := s.ResourceCapacity(ctx, r.ID, at(0, 0, 0), at(1, 0, 0))
	if err != nil || len(capacity) != 1 || !capacity[0].Start.Equal(at(0, 12, 0)) || !capacity[0].End.Equal(at(0, 16, 0)) {
		t.Fatalf("capacity = %+v err=%v, want 12:00-16:00", capacity, err)
	}

	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if b := barsOf(t, ctx, s)[op.ID]; !b.Start.Equal(at(0, 12, 0)) || b.OutsideCapacity {
		t.Fatalf("after the downtime the job is %+v, want it at 12:00 inside open capacity", b)
	}

	extra, _, err := s.CreateCapacityOverride(ctx, store.CapacityOverride{ResourceID: r.ID, Kind: domain.OverrideExtra, StartAt: at(0, 16, 0), EndAt: at(0, 18, 0)}, store.Idem{})
	if err != nil || len(extra.AffectedJobs) != 0 {
		t.Fatalf("extra = %+v err=%v", extra, err)
	}
	list, err := s.ListCapacityOverrides(ctx, r.ID, time.Time{}, time.Time{})
	if err != nil || len(list) != 2 || list[0].ID != res.ID {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if later, err := s.ListCapacityOverrides(ctx, r.ID, at(3, 0, 0), time.Time{}); err != nil || len(later) != 0 {
		t.Fatalf("list from day 3 = %+v err=%v", later, err)
	}
	if other, err := s.ListCapacityOverrides(tenantCtx(2), r.ID, time.Time{}, time.Time{}); problemCode(err) != store.CodeResourceNotFound {
		t.Fatalf("tenant 2 lists tenant 1 overrides: %+v err=%v", other, err)
	}

	if err := s.DeleteCapacityOverride(tenantCtx(2), r.ID, res.ID); problemCode(err) != store.CodeOverrideNotFound {
		t.Fatalf("tenant 2 deletes: err=%v", err)
	}
	if err := s.DeleteCapacityOverride(ctx, r.ID, res.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCapacityOverride(ctx, r.ID, res.ID); problemCode(err) != store.CodeOverrideNotFound {
		t.Fatalf("second delete: err=%v", err)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.capacityoverride.deleted.v1"); got != 1 {
		t.Fatalf("capacityoverride.deleted events = %d, want 1", got)
	}
	capacity, err = s.ResourceCapacity(ctx, r.ID, at(0, 0, 0), at(1, 0, 0))
	if err != nil || len(capacity) != 1 || !capacity[0].Start.Equal(at(0, 8, 0)) || !capacity[0].End.Equal(at(0, 18, 0)) {
		t.Fatalf("capacity after delete = %+v err=%v, want 08:00-18:00 (the extra shift merges with the shift)", capacity, err)
	}
}

// A downtime that touches only the second segment of a split job reports
// the whole job, from its first segment to its last.
func TestCapacityOverride_AffectedJobSpansAllSegments(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "OVEN-1", true)
	op := mustOperation(t, ctx, s, ids.New(), 10, 600, r.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	res, _, err := s.CreateCapacityOverride(ctx, store.CapacityOverride{ResourceID: r.ID, Kind: domain.OverrideDowntime, StartAt: at(1, 9, 0), EndAt: at(1, 9, 30)}, store.Idem{})
	if err != nil || len(res.AffectedJobs) != 1 {
		t.Fatalf("downtime = %+v err=%v, want one affected job", res, err)
	}
	if j := res.AffectedJobs[0]; j.JobID != op.ID || !j.Start.Equal(at(0, 8, 0)) || !j.End.Equal(at(1, 10, 0)) {
		t.Fatalf("affected job = %+v, want %s from day 0 08:00 to day 1 10:00", j, op.ID)
	}
}
