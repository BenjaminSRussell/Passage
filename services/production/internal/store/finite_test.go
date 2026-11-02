package store

import (
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/production/internal/domain"
)

func scheduledOrder(t *testing.T, key string, tenantN int) (*stepper, Order, *Postgres, *pgxpool.Pool) {
	t.Helper()
	pool, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(tenantN))
	st, _ := estimatedOrder(t, s, ctx, key, ids.New(), func(in *NewOrder) { in.Operations = scheduledRoute(t, ids.New(), ids.New()) })
	at := time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)
	o := st.must(domain.CmdSchedule, func(tr *Transition) {
		tr.Schedule = &ScheduleParams{Method: domain.MethodJobs, Direction: domain.Forward, At: &at}
	})
	return st, o, s, pool
}

// FR-23.10: scheduling's windows replace the infinite-capacity dates; jobs follow, the order is dated
// from them, the basis turns finite, and a redelivery changes nothing.
func TestFiniteDates_ReplaceTheBaselineAndReplayIsANoOp(t *testing.T) {
	_, o, s, pool := scheduledOrder(t, "fin-1", 2101)
	ctx := tenantCtx(testkit.TenantID(2101))
	if o.ScheduleBasis != domain.BasisInfinite {
		t.Fatalf("basis after :schedule = %s", o.ScheduleBasis)
	}
	// op 10 (13.5 h of work) runs over a weekend gap: Fri 08:00 to Mon 12:00; op 20 follows.
	start := time.Date(2026, 11, 6, 8, 0, 0, 0, time.UTC)
	w := []domain.OperationWindow{
		{OperNumber: 10, Start: start, End: start.Add(76 * time.Hour)},
		{OperNumber: 20, Start: start.Add(76 * time.Hour), End: start.Add(81 * time.Hour)},
	}
	in := FiniteDates{OrderID: o.ID, RunID: "run-1", Windows: w, Actor: "scheduler"}
	got, changed, err := s.ApplyFiniteDates(ctx, in)
	if err != nil || !changed {
		t.Fatalf("apply: %v changed=%v", err, changed)
	}
	if got.ScheduleBasis != domain.BasisFinite || got.FiniteRunID == nil || *got.FiniteRunID != "run-1" || got.FiniteScheduledAt == nil ||
		got.Version != o.Version+1 || *got.StartDate != "2026-11-06" || *got.EndDate != "2026-11-09" {
		t.Fatalf("order: %+v", got)
	}
	if !got.Operations[0].PlannedEnd.Equal(w[0].End) || !got.Operations[1].PlannedStart.Equal(w[1].Start) {
		t.Fatalf("operation windows: %+v %+v", got.Operations[0], got.Operations[1])
	}
	jobs := jobsOf(t, s, ctx, o.ID)
	if len(jobs) != 5 || !jobs[0].PlannedStart.Equal(start) || !jobs[3].PlannedEnd.Equal(w[0].End) || !jobs[4].PlannedEnd.Equal(w[1].End) {
		t.Fatalf("jobs: %+v", jobs)
	}
	for i := 1; i < 4; i++ {
		if !jobs[i].PlannedStart.Equal(jobs[i-1].PlannedEnd) {
			t.Errorf("job %d does not follow job %d", i, i-1)
		}
	}
	// The planned hours are the work content and do not change with the calendar.
	if !jobs[2].PlannedHours.Equal(d("10")) {
		t.Errorf("process hours = %s", jobs[2].PlannedHours)
	}

	// The redelivery carries the version the first call saw: a no-op does not check it.
	in.IfMatch = o.Version
	again, changed, err := s.ApplyFiniteDates(ctx, in)
	if err != nil || changed || again.Version != got.Version {
		t.Fatalf("redelivery: %v changed=%v version %d", err, changed, again.Version)
	}
	evts := scheduledEvents(t, pool, o.ID)
	last := evts[len(evts)-1]
	if len(evts) != 2 || last.Reason != "finite" || last.StartDate != "2026-11-06" || last.EndDate != "2026-11-09" || len(last.Jobs) != 5 ||
		last.Jobs[0].JobID != jobs[0].ID || last.OrderVersion != got.Version || last.FiniteRunID == nil || *last.FiniteRunID != "run-1" ||
		len(last.CompressedOperations) != 0 {
		t.Fatalf("events (a redelivery adds none): %+v", evts)
	}
}

func TestFiniteDates_EventAndRedateGoBackToInfinite(t *testing.T) {
	st, o, s, pool := scheduledOrder(t, "fin-2", 2102)
	ctx := tenantCtx(testkit.TenantID(2102))
	start := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	if _, _, err := s.ApplyFiniteDates(ctx, FiniteDates{OrderID: o.ID, Windows: []domain.OperationWindow{{OperNumber: 20, Start: start, End: start.Add(9 * time.Hour)}}}); err != nil {
		t.Fatal(err)
	}
	// An operation not in the request keeps its dates; the order spans both.
	got := st.refresh()
	if !got.Operations[0].PlannedStart.Equal(time.Date(2026, 11, 2, 8, 0, 0, 0, time.UTC)) || *got.EndDate != "2026-11-09" || *got.StartDate != "2026-11-02" {
		t.Fatalf("mixed order: %+v dates %v %v", got.Operations[0], got.StartDate, got.EndDate)
	}
	// An order edit re-plans the infinite baseline from the schedule start: the finite plan no longer holds.
	st.version = got.Version
	if _, err := st.edit(domain.OrderEdits{Operations: []domain.OperationEdit{{Op: domain.EditChange, OperNumber: 20, RunHPer: ptrTo(d("0.06"))}}}, false); err != nil {
		t.Fatal(err)
	}
	if after := st.refresh(); after.ScheduleBasis != domain.BasisInfinite || after.FiniteRunID != nil || after.FiniteScheduledAt != nil {
		t.Fatalf("after an edit: %+v", after)
	}
	evts := scheduledEvents(t, pool, o.ID)
	if len(evts) != 3 || evts[1].Reason != "finite" || evts[2].Reason != "redate" {
		t.Fatalf("events: %+v", evts)
	}
}

func TestFiniteDates_Refusals(t *testing.T) {
	st, o, s, _ := scheduledOrder(t, "fin-3", 2103)
	ctx := tenantCtx(testkit.TenantID(2103))
	t0 := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	win := func(oper int) []domain.OperationWindow {
		return []domain.OperationWindow{{OperNumber: oper, Start: t0, End: t0.Add(time.Hour)}}
	}
	check := func(name string, err error, status int) {
		t.Helper()
		if code := problemCode(err); code != status {
			t.Errorf("%s: status %d (%v), want %d", name, code, err, status)
		}
	}
	_, _, err := s.ApplyFiniteDates(ctx, FiniteDates{OrderID: o.ID, Windows: win(99)})
	check("unknown operation", err, http.StatusUnprocessableEntity)
	_, _, err = s.ApplyFiniteDates(ctx, FiniteDates{OrderID: ids.New(), Windows: win(10)})
	check("unknown order", err, http.StatusNotFound)
	_, _, err = s.ApplyFiniteDates(tenantCtx(testkit.TenantID(2104)), FiniteDates{OrderID: o.ID, Windows: win(10)})
	check("another tenant", err, http.StatusNotFound)

	// Unscheduled and cancelled orders have no schedule to replace.
	st.must(domain.CmdResetStatus, func(tr *Transition) { tr.ResetTo = domain.Estimated })
	_, _, err = s.ApplyFiniteDates(ctx, FiniteDates{OrderID: o.ID, Windows: win(10)})
	check("estimated order", err, http.StatusConflict)
}

func TestFiniteDates_FeedbackLocksTheOperation(t *testing.T) {
	_, s := migrated(t)
	ctx := tenantCtx(testkit.TenantID(2105))
	st := startedOrder(t, s, ctx, routedOrder(t, "fin-4", ids.New(), 10, 20))
	job10 := jobOf(t, s, ctx, st.orderID, 10, domain.JobProcess)
	st.mustPost(st.mustJournal(NewJournal{Type: domain.JournalJobCard, Hours: []HoursInput{{JobID: job10.ID, Hours: d("1"), QtyGood: d("1")}}}).ID)
	t0 := time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC)
	_, _, err := s.ApplyFiniteDates(ctx, FiniteDates{OrderID: st.orderID, Windows: []domain.OperationWindow{{OperNumber: 10, Start: t0, End: t0.Add(time.Hour)}}})
	if problemCode(err) != http.StatusConflict {
		t.Fatalf("operation with feedback: %v", err)
	}
	// The untouched operation can still move on a started order.
	if _, changed, err := s.ApplyFiniteDates(ctx, FiniteDates{OrderID: st.orderID, Windows: []domain.OperationWindow{{OperNumber: 20, Start: t0, End: t0.Add(time.Hour)}}}); err != nil || !changed {
		t.Fatalf("operation 20: %v changed=%v", err, changed)
	}
}

// A change against a stale version is refused (a person's form typed before newer dates); windows
// out of route order are refused; a window shorter than the hours is applied and flagged until the
// next infinite re-date.
func TestFiniteDates_VersionRouteOrderAndCompression(t *testing.T) {
	st, o, s, pool := scheduledOrder(t, "fin-5", 2106)
	ctx := tenantCtx(testkit.TenantID(2106))
	t0 := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	later := []domain.OperationWindow{{OperNumber: 10, Start: t0, End: t0.Add(14 * time.Hour)}, {OperNumber: 20, Start: t0.Add(14 * time.Hour), End: t0.Add(20 * time.Hour)}}
	_, _, err := s.ApplyFiniteDates(ctx, FiniteDates{OrderID: o.ID, Windows: later, IfMatch: o.Version + 1})
	if problemCode(err) != http.StatusPreconditionFailed {
		t.Fatalf("stale If-Match: %v", err)
	}
	// Operation 10 alone moved past operation 20, which keeps its 2 November dates.
	_, _, err = s.ApplyFiniteDates(ctx, FiniteDates{OrderID: o.ID, Windows: later[:1], IfMatch: o.Version})
	if problemCode(err) != http.StatusUnprocessableEntity {
		t.Fatalf("operation 10 after operation 20: %v", err)
	}
	// 13.5 h of operation 10 in a 10 h window.
	short := []domain.OperationWindow{{OperNumber: 10, Start: t0, End: t0.Add(10 * time.Hour)}, later[1]}
	got, changed, err := s.ApplyFiniteDates(ctx, FiniteDates{OrderID: o.ID, Windows: short, IfMatch: o.Version, Actor: "planner"})
	if err != nil || !changed || len(got.FiniteCompressedOperations) != 1 || got.FiniteCompressedOperations[0] != 10 {
		t.Fatalf("compressed: %v changed=%v %+v", err, changed, got.FiniteCompressedOperations)
	}
	if evts := scheduledEvents(t, pool, o.ID); len(evts[len(evts)-1].CompressedOperations) != 1 || evts[len(evts)-1].FiniteRunID != nil {
		t.Fatalf("event: %+v", evts[len(evts)-1])
	}
	st.version = got.Version
	if _, err := st.edit(domain.OrderEdits{Operations: []domain.OperationEdit{{Op: domain.EditChange, OperNumber: 20, RunHPer: ptrTo(d("0.06"))}}}, false); err != nil {
		t.Fatal(err)
	}
	if after := st.refresh(); after.ScheduleBasis != domain.BasisInfinite || len(after.FiniteCompressedOperations) != 0 {
		t.Fatalf("after a re-date: %s %v", after.ScheduleBasis, after.FiniteCompressedOperations)
	}
}

// A split re-dates both orders from the schedule start with infinite capacity (FR-21.15): the
// finite plan scheduling sent for the original quantity holds for neither order, so both go back
// to basis infinite with no run, and the new order never inherits the original's run.
func TestFiniteDates_SplitGoesBackToInfinite(t *testing.T) {
	st, o, s, _ := scheduledOrder(t, "fin-split", 2105)
	ctx := tenantCtx(testkit.TenantID(2105))
	start := time.Date(2026, 11, 9, 8, 0, 0, 0, time.UTC)
	got, _, err := s.ApplyFiniteDates(ctx, FiniteDates{OrderID: o.ID, RunID: "run-s", Windows: []domain.OperationWindow{{OperNumber: 20, Start: start, End: start.Add(9 * time.Hour)}}})
	if err != nil || got.ScheduleBasis != domain.BasisFinite {
		t.Fatalf("apply: %v %s", err, got.ScheduleBasis)
	}
	st.refresh()
	res, err := s.SplitOrder(ctx, Split{OrderID: o.ID, IfMatch: st.version, SplitQty: d("40"), Actor: "planner", IdempotencyKey: "fin-split-1", RequestHash: "h"})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range []Order{res.Original, res.New} {
		if x.ScheduleBasis != domain.BasisInfinite || x.FiniteRunID != nil || x.FiniteScheduledAt != nil || x.StartDate == nil || len(jobsOf(t, s, ctx, x.ID)) == 0 {
			t.Fatalf("after the split %s: basis %s run %v dates %v, %d jobs", x.OrderNumber, x.ScheduleBasis, x.FiniteRunID, x.StartDate, len(jobsOf(t, s, ctx, x.ID)))
		}
	}
}
