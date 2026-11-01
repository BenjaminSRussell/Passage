package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func TestRunKPIs_MeasureTheJobsTheRunHolds(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m, n := mustResource(t, ctx, s, "M1", true), mustResource(t, ctx, s, "M2", true)
	due := at(0, 8, 30)
	if _, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 120, Priority: 100, DueAt: &due, ResourceIDs: []string{m.ID}}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	mustOperation(t, ctx, s, ids.New(), 10, 240, m.ID)
	mustOperation(t, ctx, s, ids.New(), 10, 60, n.ID)
	run, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.RunKPIs(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if k.PlacedJobs != 3 || k.Orders != 3 || k.LateJobs != 1 || k.LateOrders != 1 || k.MaxLateMinutes != 90 {
		t.Fatalf("kpis = %+v, want 3 jobs, one 90 minutes late", k)
	}
	if k.SpanStart == nil || !k.SpanStart.Equal(at(0, 8, 0)) || !k.SpanEnd.Equal(at(0, 14, 0)) || k.BookedMinutes != 420 {
		t.Fatalf("span %v..%v booked %d, want Monday 08:00-14:00 and 420 minutes", k.SpanStart, k.SpanEnd, k.BookedMinutes)
	}
	if len(k.Resources) != 2 || k.Resources[0].ResourceCode != "M1" || k.Resources[0].BookedMinutes != 360 || k.Resources[0].OpenMinutes != 360 || k.Resources[0].UtilizationPct != 100 {
		t.Fatalf("resources = %+v, want M1 fully used over the 360-minute span, most used first", k.Resources)
	}
	if k.Resources[1].ResourceCode != "M2" || k.Resources[1].BookedMinutes != 60 || k.Resources[1].UtilizationPct != 16.7 {
		t.Fatalf("M2 = %+v", k.Resources[1])
	}

	// A later run replaces the jobs, so the first run holds none.
	if _, _, err := s.RunSchedule(ctx, siteRun(at(1, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if old, err := s.RunKPIs(ctx, run.ID); err != nil || old.PlacedJobs != 0 || old.SpanStart != nil || len(old.Resources) != 0 {
		t.Fatalf("superseded run kpis = %+v err=%v, want zeros", old, err)
	}
}

func TestRunKPIs_UnknownRunAndOtherTenant(t *testing.T) {
	s, _, _ := setup(t)
	one, two := tenantCtx(1), tenantCtx(2)
	r := mustResource(t, one, s, "M1", true)
	mustOperation(t, one, s, ids.New(), 10, 60, r.ID)
	run, _, err := s.RunSchedule(one, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.RunKPIs(two, run.ID); problemCode(err) != store.CodeRunNotFound {
		t.Fatalf("tenant 2 reads tenant 1 run: err=%v", err)
	}
	if _, err := s.RunKPIs(one, ids.New()); problemCode(err) != store.CodeRunNotFound {
		t.Fatalf("unknown run: err=%v", err)
	}
}

func TestLateOrders_ListsLateThenUnplacedAndHonoursFilters(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	m := mustResource(t, ctx, s, "M1", true)
	lateOrder, mildOrder, onTime, stuck := ids.New(), ids.New(), ids.New(), ids.New()
	due1, due2, due3 := at(0, 8, 30), at(0, 11, 10), at(5, 0, 0)
	for _, o := range []struct {
		order string
		min   int
		due   *time.Time
	}{{lateOrder, 120, &due1}, {mildOrder, 120, &due2}, {onTime, 60, &due3}} {
		d := *o.due
		if _, _, err := s.CreateOperation(ctx, store.Operation{OrderID: o.order, OperNumber: 10, Minutes: o.min, Priority: 100, DueAt: &d, ResourceIDs: []string{m.ID}}, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	stuckOp := mustOperation(t, ctx, s, stuck, 10, 60, m.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	// The scheduler leaves one operation unplaced: drop its job and flag it.
	if _, err := admin.Exec(context.Background(), `delete from scheduled_job where job_id = $1`, stuckOp.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(context.Background(), `update scheduling_operation set status = 'unplaced' where id = $1`, stuckOp.ID); err != nil {
		t.Fatal(err)
	}

	// The on-time order consumes the late order's output: that is the late
	// order's pegged demand.
	if _, _, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: lateOrder, DependentOrderID: onTime}, store.Idem{}); err != nil {
		t.Fatal(err)
	}

	items, truncated, err := s.LateOrders(ctx, store.LateOrderFilter{MinLateMinutes: 1, Limit: 10})
	if err != nil || truncated {
		t.Fatalf("err=%v truncated=%v", err, truncated)
	}
	if len(items) != 3 || items[0].OrderID != lateOrder || items[1].OrderID != mildOrder || items[2].OrderID != stuck {
		t.Fatalf("items = %+v, want the late order, the mildly late order, then the unplaced one", items)
	}
	if items[0].LateMinutes != 90 || items[0].LateOperations != 1 || items[0].Operations != 1 || items[0].DueAt == nil || !items[0].DueAt.Equal(due1) ||
		items[0].PlannedEnd == nil || !items[0].PlannedEnd.Equal(at(0, 10, 0)) || items[0].DependentOrders != 1 || items[1].DependentOrders != 0 {
		t.Fatalf("late order = %+v", items[0])
	}
	if items[2].UnplacedOperations != 1 || items[2].LateMinutes != 0 || items[2].PlannedStart != nil {
		t.Fatalf("unplaced order = %+v", items[2])
	}
	big, _, err := s.LateOrders(ctx, store.LateOrderFilter{MinLateMinutes: 60, Limit: 10})
	if err != nil || len(big) != 2 || big[0].OrderID != lateOrder || big[1].OrderID != stuck {
		t.Fatalf("min 60 = %+v err=%v, want the 90-minute order and the unplaced one", big, err)
	}
	cut, truncated, err := s.LateOrders(ctx, store.LateOrderFilter{MinLateMinutes: 1, Limit: 2})
	if err != nil || len(cut) != 2 || !truncated {
		t.Fatalf("limit 2 = %+v truncated=%v err=%v", cut, truncated, err)
	}
	if other, _, err := s.LateOrders(tenantCtx(2), store.LateOrderFilter{MinLateMinutes: 1, Limit: 10}); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 sees %+v err=%v", other, err)
	}
}

func TestRunKPIs_CountsTheSecondaryResourcesTheRunBooked(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m, tool := mustResource(t, ctx, s, "M1", true), mustResource(t, ctx, s, "TOOL", true)
	secOperation(t, ctx, s, 10, 120, m.ID, tool.ID)
	run, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	k, err := s.RunKPIs(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The run's totals are its jobs; the tool held with the job is used too.
	if k.PlacedJobs != 1 || k.BookedMinutes != 120 || len(k.Resources) != 2 {
		t.Fatalf("kpis = %+v, want one 120-minute job on M1 and TOOL", k)
	}
	for _, r := range k.Resources {
		if r.BookedMinutes != 120 || r.OpenMinutes != 120 || r.Jobs != 1 || r.UtilizationPct != 100 {
			t.Fatalf("%s = %+v, want fully used over the job's span", r.ResourceCode, r)
		}
	}
}
