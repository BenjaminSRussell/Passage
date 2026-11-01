package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// A block is retired and re-prioritised under optimistic versioning: a stale
// version is refused, a replay returns the first answer, one event is written
// per change and another tenant cannot see or change the block.
func TestUpdateCapacityBlock_VersionReplayAndEvent(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "PAINT-1", true)
	white := []domain.Predicate{{Attribute: "color", Op: "=", Value: "white"}}
	b, _, err := s.CreateCapacityBlock(ctx, store.CapacityBlock{ResourceID: r.ID, Code: "MON-WHITE",
		Windows: []domain.Shift{{Weekday: 1, StartMinute: 360, EndMinute: 840}}, Condition: white, Priority: 10, Active: true}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	off := false
	prio := 40
	patch := store.BlockPatch{Active: &off, Priority: &prio}

	if _, _, err := s.UpdateCapacityBlock(ctx, b.ID, 9, patch, at(0, 0, 0), store.Idem{}); problemCode(err) != store.CodeBlockVersion {
		t.Fatalf("stale version: err=%v", err)
	}
	if _, _, err := s.UpdateCapacityBlock(ctx, ids.New(), 1, patch, at(0, 0, 0), store.Idem{}); problemCode(err) != store.CodeBlockNotFound {
		t.Fatalf("unknown block: err=%v", err)
	}
	got, replayed, err := s.UpdateCapacityBlock(ctx, b.ID, 1, patch, at(0, 0, 0), store.Idem{Key: "b1", Fingerprint: "fp"})
	if err != nil || replayed || got.Version != 2 || got.Active || got.Priority != 40 || len(got.Condition) != 1 || got.Code != "MON-WHITE" || len(got.Windows) != 1 {
		t.Fatalf("update = %+v replayed=%v err=%v", got, replayed, err)
	}
	again, replayed, err := s.UpdateCapacityBlock(ctx, b.ID, 1, patch, at(0, 0, 0), store.Idem{Key: "b1", Fingerprint: "fp"})
	if err != nil || !replayed || again.Version != 2 {
		t.Fatalf("replay = %+v replayed=%v err=%v", again, replayed, err)
	}
	if _, _, err := s.UpdateCapacityBlock(ctx, b.ID, 1, patch, at(0, 0, 0), store.Idem{Key: "b1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key: err=%v", err)
	}
	// An empty condition list admits every job; the window is untouched.
	none := []domain.Predicate{}
	cleared, _, err := s.UpdateCapacityBlock(ctx, b.ID, 2, store.BlockPatch{Condition: &none}, at(0, 0, 0), store.Idem{})
	if err != nil || cleared.Version != 3 || len(cleared.Condition) != 0 || cleared.Active || cleared.Priority != 40 {
		t.Fatalf("clear condition = %+v err=%v", cleared, err)
	}
	if n := outboxCount(t, admin, "dev.passage.scheduling.capacityblock.updated.v1"); n != 2 {
		t.Fatalf("capacityblock.updated events = %d, want 2 (the replay writes none)", n)
	}
	read, err := s.GetCapacityBlock(ctx, b.ID)
	if err != nil || read.Version != 3 {
		t.Fatalf("get = %+v err=%v", read, err)
	}
	if _, err := s.GetCapacityBlock(tenantCtx(2), b.ID); problemCode(err) != store.CodeBlockNotFound {
		t.Fatalf("other tenant reads the block: err=%v", err)
	}
	if _, _, err := s.UpdateCapacityBlock(tenantCtx(2), b.ID, 3, patch, at(0, 0, 0), store.Idem{}); problemCode(err) != store.CodeBlockNotFound {
		t.Fatalf("other tenant updates the block: err=%v", err)
	}
}

// A block change lists the booked jobs the resource's active blocks now keep
// out, and only those: the white job inside the Monday block is listed once
// the block admits black only, the red job in unblocked time never is, a job
// already outside the calendar is left to the calendar change, and retiring
// the block frees its time for every job again.
func TestUpdateCapacityBlock_ListsJobsTheBlocksNowKeepOut(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "PAINT-1", true)
	white := []domain.Predicate{{Attribute: "color", Op: "=", Value: "white"}}
	b, _, err := s.CreateCapacityBlock(ctx, store.CapacityBlock{ResourceID: r.ID, Code: "MON-WHITE",
		Windows: []domain.Shift{{Weekday: 1, StartMinute: 8 * 60, EndMinute: 12 * 60}}, Condition: white, Priority: 10, Active: true}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	jobs := map[string]string{}
	for _, color := range []string{"red", "white"} {
		due := at(0, 10, 0)
		op, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 120, Priority: 100, DueAt: &due,
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
	if w := bars[jobs["white"]]; !w.Start.Equal(at(0, 8, 0)) {
		t.Fatalf("white job at %v, want inside the block at 08:00", w.Start)
	}

	black := []domain.Predicate{{Attribute: "color", Op: "=", Value: "black"}}
	got, _, err := s.UpdateCapacityBlock(ctx, b.ID, 1, store.BlockPatch{Condition: &black}, at(0, 0, 0), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.AffectedJobs) != 1 || got.AffectedJobs[0].JobID != jobs["white"] || !got.AffectedJobs[0].Start.Equal(at(0, 8, 0)) {
		t.Fatalf("affected = %+v, want only the white job inside the block", got.AffectedJobs)
	}
	var evt schedevents.CapacityBlockUpdatedData
	if err := admin.QueryRow(context.Background(), `select payload->'data' from outbox where type = $1 and aggregate_id = $2 order by created_at desc limit 1`,
		schedevents.CapacityBlockUpdated, b.ID).Scan(&evt); err != nil {
		t.Fatal(err)
	}
	if len(evt.AffectedJobs) != 1 || evt.AffectedJobs[0] != jobs["white"] || evt.Version != 2 {
		t.Fatalf("capacityblock.updated data = %+v, want the white job listed", evt)
	}
	// From 10:00 the white job is over, so nothing is listed.
	prio := 20
	if later, _, err := s.UpdateCapacityBlock(ctx, b.ID, 2, store.BlockPatch{Priority: &prio}, at(0, 10, 0), store.Idem{}); err != nil || len(later.AffectedJobs) != 0 {
		t.Fatalf("after the job ended: %+v err=%v", later.AffectedJobs, err)
	}
	// Once the calendar opens at 09:00 the white job is the calendar's to
	// report (AC-23.7), not the block's.
	if _, _, err := s.UpdateResource(ctx, r.ID, 1, store.ResourcePatch{Shifts: weekdays(9*60, 16*60)}, at(0, 0, 0), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	prio = 30
	if moved, _, err := s.UpdateCapacityBlock(ctx, b.ID, 3, store.BlockPatch{Priority: &prio}, at(0, 0, 0), store.Idem{}); err != nil || len(moved.AffectedJobs) != 0 {
		t.Fatalf("job outside the calendar: %+v err=%v", moved.AffectedJobs, err)
	}
	off := false
	retired, _, err := s.UpdateCapacityBlock(ctx, b.ID, 4, store.BlockPatch{Active: &off}, at(0, 0, 0), store.Idem{})
	if err != nil || retired.AffectedJobs == nil || len(retired.AffectedJobs) != 0 {
		t.Fatalf("retired block: affected = %#v err=%v, want an empty list", retired.AffectedJobs, err)
	}
}

// A network is renamed and ended on a date; the end cannot precede the start.
func TestUpdateNetwork_RenameEndAndRefuseEarlyEnd(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	v1, f1 := mustResource(t, ctx, s, "V1", true), mustResource(t, ctx, s, "F1", true)
	n, _, err := s.CreateNetwork(ctx, store.Network{Code: "MIX-FILL", Name: "Mixing to filling", ActiveFrom: "2027-01-01",
		Edges: []domain.NetworkEdge{{From: v1.ID, To: f1.ID}}}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	name, end := "Mixing line", "2027-06-30"
	if _, _, err := s.UpdateNetwork(ctx, n.ID, 5, store.NetworkPatch{Name: &name}, store.Idem{}); problemCode(err) != store.CodeNetworkVersion {
		t.Fatalf("stale version: err=%v", err)
	}
	if _, _, err := s.UpdateNetwork(ctx, ids.New(), 1, store.NetworkPatch{Name: &name}, store.Idem{}); problemCode(err) != store.CodeNetworkNotFound {
		t.Fatalf("unknown network: err=%v", err)
	}
	early := "2026-12-31"
	if _, _, err := s.UpdateNetwork(ctx, n.ID, 1, store.NetworkPatch{ActiveTo: &early}, store.Idem{}); problemCode(err) != store.CodeNetworkEnd {
		t.Fatalf("end before start: err=%v", err)
	}
	got, replayed, err := s.UpdateNetwork(ctx, n.ID, 1, store.NetworkPatch{Name: &name, ActiveTo: &end}, store.Idem{Key: "n1", Fingerprint: "fp"})
	if err != nil || replayed || got.Version != 2 || got.Name != name || got.ActiveTo == nil || *got.ActiveTo != end || len(got.Edges) != 1 || got.Code != "MIX-FILL" {
		t.Fatalf("update = %+v replayed=%v err=%v", got, replayed, err)
	}
	// Renaming alone keeps the end date.
	other := "Mixing line B"
	renamed, _, err := s.UpdateNetwork(ctx, n.ID, 2, store.NetworkPatch{Name: &other}, store.Idem{})
	if err != nil || renamed.ActiveTo == nil || *renamed.ActiveTo != end || renamed.Name != other {
		t.Fatalf("rename = %+v err=%v", renamed, err)
	}
	again, replayed, err := s.UpdateNetwork(ctx, n.ID, 1, store.NetworkPatch{Name: &name, ActiveTo: &end}, store.Idem{Key: "n1", Fingerprint: "fp"})
	if err != nil || !replayed || again.Version != 2 {
		t.Fatalf("replay = %+v replayed=%v err=%v", again, replayed, err)
	}
	if n := outboxCount(t, admin, "dev.passage.scheduling.network.updated.v1"); n != 2 {
		t.Fatalf("network.updated events = %d, want 2", n)
	}
	if _, _, err := s.UpdateNetwork(tenantCtx(2), n.ID, 3, store.NetworkPatch{Name: &name}, store.Idem{}); problemCode(err) != store.CodeNetworkNotFound {
		t.Fatalf("other tenant: err=%v", err)
	}
}
