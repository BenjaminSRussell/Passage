package store_test

import (
	"context"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// Shortening a resource's calendar lists the booked jobs it no longer covers
// (AC-23.7), bumps the version, refuses a stale version, replays by key and
// writes one resource.updated event.
func TestUpdateResource_CalendarChangeListsUncoveredJobs(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	op := mustOperation(t, ctx, s, ids.New(), 10, 120, r.ID)
	late := mustOperation(t, ctx, s, ids.New(), 10, 120, r.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	if !bars[op.ID].Start.Equal(at(0, 8, 0)) || !bars[late.ID].Start.Equal(at(0, 10, 0)) {
		t.Fatalf("setup bars %+v %+v", bars[op.ID], bars[late.ID])
	}
	from := at(0, 6, 0)
	name := "Mixer east"
	short := weekdays(8*60, 10*60)
	patch := store.ResourcePatch{Name: &name, Shifts: short}

	if _, _, err := s.UpdateResource(ctx, r.ID, 7, patch, from, store.Idem{}); problemCode(err) != store.CodeResourceVersion {
		t.Fatalf("stale version: err=%v", err)
	}
	if _, _, err := s.UpdateResource(ctx, ids.New(), 1, patch, from, store.Idem{}); problemCode(err) != store.CodeResourceNotFound {
		t.Fatalf("unknown resource: err=%v", err)
	}
	got, replayed, err := s.UpdateResource(ctx, r.ID, 1, patch, from, store.Idem{Key: "u1", Fingerprint: "fp"})
	if err != nil || replayed || got.Version != 2 || got.Name != name || got.Code != "MIX-1" {
		t.Fatalf("update = %+v replayed=%v err=%v", got, replayed, err)
	}
	if len(got.AffectedJobs) != 1 || got.AffectedJobs[0].JobID != late.ID || !got.AffectedJobs[0].Start.Equal(at(0, 10, 0)) {
		t.Fatalf("affected = %+v, want only the 10:00 job", got.AffectedJobs)
	}
	again, replayed, err := s.UpdateResource(ctx, r.ID, 1, patch, from, store.Idem{Key: "u1", Fingerprint: "fp"})
	if err != nil || !replayed || again.Version != 2 {
		t.Fatalf("replay = %+v replayed=%v err=%v", again, replayed, err)
	}
	if _, _, err := s.UpdateResource(ctx, r.ID, 1, patch, from, store.Idem{Key: "u1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key: err=%v", err)
	}
	if n := outboxCount(t, admin, "dev.passage.scheduling.resource.updated.v1"); n != 1 {
		t.Fatalf("resource.updated events = %d, want 1", n)
	}
	if b := barsOf(t, ctx, s)[late.ID]; !b.OutsideCapacity {
		t.Fatalf("bar after the change = %+v, want outsideCapacity", b)
	}

	// Jobs that already ended are not reported, and another tenant cannot
	// touch the resource.
	past, _, err := s.UpdateResource(ctx, r.ID, 2, store.ResourcePatch{Shifts: weekdays(8*60, 9*60)}, at(0, 12, 0), store.Idem{})
	if err != nil || len(past.AffectedJobs) != 0 {
		t.Fatalf("past jobs = %+v err=%v", past.AffectedJobs, err)
	}
	if _, _, err := s.UpdateResource(tenantCtx(2), r.ID, 3, patch, from, store.Idem{}); problemCode(err) != store.CodeResourceNotFound {
		t.Fatalf("other tenant: err=%v", err)
	}

	// Time zone changes move the open hours: 08:00-10:00 in Berlin is
	// 07:00-09:00 UTC in January, so the 08:00 job is no longer covered.
	tz := "Europe/Berlin"
	moved, _, err := s.UpdateResource(ctx, r.ID, 3, store.ResourcePatch{TimeZone: &tz, Shifts: short}, from, store.Idem{})
	if err != nil || len(moved.AffectedJobs) != 2 || moved.TimeZone != tz {
		t.Fatalf("time zone change = %+v err=%v, want both jobs affected", moved, err)
	}
}

// A calendar change on a secondary resource lists the jobs that book it,
// and a job already running counts only from the request time on.
func TestUpdateResource_SecondaryAndRunningJobs(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m, tool := mustResource(t, ctx, s, "M1", true), mustResource(t, ctx, s, "TOOL", true)
	a := secOperation(t, ctx, s, 10, 120, m.ID, tool.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if b := barsOf(t, ctx, s)[a.ID]; !b.Start.Equal(at(0, 8, 0)) || !b.End.Equal(at(0, 10, 0)) {
		t.Fatalf("setup bar %+v", b)
	}
	// At 09:00 the job has used 08:00-09:00; a tool calendar from 09:00
	// still covers the rest, so nothing is affected.
	got, _, err := s.UpdateResource(ctx, tool.ID, 1, store.ResourcePatch{Shifts: weekdays(9*60, 16*60)}, at(0, 9, 0), store.Idem{})
	if err != nil || len(got.AffectedJobs) != 0 {
		t.Fatalf("running job = %+v err=%v, want none affected", got.AffectedJobs, err)
	}
	// From 09:30 the tool is held outside its hours: the job is listed with
	// the span of its primary segments.
	got, _, err = s.UpdateResource(ctx, tool.ID, 2, store.ResourcePatch{Shifts: weekdays(8*60, 9*60+30)}, at(0, 9, 0), store.Idem{})
	if err != nil || len(got.AffectedJobs) != 1 || got.AffectedJobs[0].JobID != a.ID ||
		!got.AffectedJobs[0].Start.Equal(at(0, 8, 0)) || !got.AffectedJobs[0].End.Equal(at(0, 10, 0)) {
		t.Fatalf("secondary = %+v err=%v, want the job over 08:00-10:00", got.AffectedJobs, err)
	}
	// The primary's calendar is untouched, so changing M1's name alone
	// lists nothing.
	name := "Mixer"
	got, _, err = s.UpdateResource(ctx, m.ID, 1, store.ResourcePatch{Name: &name}, at(0, 9, 0), store.Idem{})
	if err != nil || len(got.AffectedJobs) != 0 || got.Name != name || len(got.Shifts) != 5 {
		t.Fatalf("rename = %+v err=%v", got, err)
	}
}

// Efficiency (BR-23.1) is stored on the resource, changed by a patch, and
// stretches the job a run books on it; job.scheduled carries the shop floor
// keys (order id and operation number as text).
func TestResourceEfficiency_StretchesRunsAndJobEventCarriesShopFloorKeys(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "PRESS-1", true)
	if r.EfficiencyPct != 100 {
		t.Fatalf("default efficiency = %d, want 100", r.EfficiencyPct)
	}
	upd, _, err := s.UpdateResource(ctx, r.ID, 1, store.ResourcePatch{EfficiencyPct: 80}, at(0, 6, 0), store.Idem{})
	if err != nil || upd.EfficiencyPct != 80 || upd.Version != 2 {
		t.Fatalf("patch = %+v err=%v", upd, err)
	}
	order := ids.New()
	op := mustOperation(t, ctx, s, order, 10, 60, r.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bar := barsOf(t, ctx, s)[op.ID]
	if !bar.Start.Equal(at(0, 8, 0)) || !bar.End.Equal(at(0, 9, 15)) {
		t.Fatalf("bar %v-%v, want 08:00-09:15 (60 min at 80 percent)", bar.Start, bar.End)
	}
	if bar.OrderRef != order || bar.OperationRef != "10" {
		t.Fatalf("bar refs = %q %q, want %q 10", bar.OrderRef, bar.OperationRef, order)
	}
	var orderRef, operationRef string
	if err := admin.QueryRow(context.Background(), `select payload->'data'->>'orderRef', payload->'data'->>'operationRef' from outbox where type = $1`,
		"dev.passage.scheduling.job.scheduled.v1").Scan(&orderRef, &operationRef); err != nil {
		t.Fatal(err)
	}
	if orderRef != order || operationRef != "10" {
		t.Fatalf("job.scheduled refs = %q %q", orderRef, operationRef)
	}
	var efficiency int
	if err := admin.QueryRow(context.Background(), `select (payload->'data'->>'efficiencyPct')::int from outbox where type = $1`,
		"dev.passage.scheduling.resource.updated.v1").Scan(&efficiency); err != nil || efficiency != 80 {
		t.Fatalf("resource.updated efficiencyPct = %d err=%v, want 80", efficiency, err)
	}
	if _, _, err := s.UpdateResource(ctx, r.ID, 2, store.ResourcePatch{EfficiencyPct: 501}, at(0, 6, 0), store.Idem{}); err == nil {
		t.Fatal("efficiency 501 stored; the check constraint should refuse it")
	}
}
