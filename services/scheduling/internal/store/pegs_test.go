package store_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	schedevents "github.com/BenjaminSRussell/passage/services/scheduling/internal/events"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func TestPegs_CreateRulesListDeleteAndTenants(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "MIX-1", true)
	a, b, c := ids.New(), ids.New(), ids.New()
	for _, o := range []string{a, b, c} {
		mustOperation(t, ctx, s, o, 10, 60, r.ID)
	}
	ab, created, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: a, DependentOrderID: b}, store.Idem{Key: "peg-1", Fingerprint: "fp1"})
	if err != nil || !created || ab.ParentOrderID != a || ab.DependentOrderID != b {
		t.Fatalf("peg = %+v created=%v err=%v", ab, created, err)
	}
	if again, created, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: a, DependentOrderID: b}, store.Idem{Key: "peg-1", Fingerprint: "fp1"}); err != nil || created || again.ID != ab.ID {
		t.Fatalf("replay = %+v created=%v err=%v", again, created, err)
	}
	if _, _, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: a, DependentOrderID: c}, store.Idem{Key: "peg-1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reused key: err=%v", err)
	}
	if same, created, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: a, DependentOrderID: b}, store.Idem{Key: "peg-2", Fingerprint: "fp2"}); err != nil || created || same.ID != ab.ID {
		t.Fatalf("pegging the same pair again = %+v created=%v err=%v", same, created, err)
	}
	if _, _, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: b, DependentOrderID: c}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: c, DependentOrderID: a}, store.Idem{}); problemCode(err) != store.CodePegCycle {
		t.Fatalf("c -> a closes a -> b -> c: err=%v", err)
	}
	if _, _, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: a, DependentOrderID: ids.New()}, store.Idem{}); problemCode(err) != store.CodePegOrderUnknown {
		t.Fatalf("order without operations: err=%v", err)
	}

	pegs, err := s.ListPegs(ctx, b)
	if err != nil || len(pegs) != 2 {
		t.Fatalf("pegs touching b = %+v err=%v, want a->b and b->c", pegs, err)
	}
	if all, err := s.ListPegs(ctx, ""); err != nil || len(all) != 2 {
		t.Fatalf("all pegs = %+v err=%v", all, err)
	}

	two := tenantCtx(2)
	if other, err := s.ListPegs(two, ""); err != nil || len(other) != 0 {
		t.Fatalf("tenant 2 sees pegs %+v err=%v", other, err)
	}
	if _, _, err := s.CreatePeg(two, store.Peg{ParentOrderID: a, DependentOrderID: c}, store.Idem{}); problemCode(err) != store.CodePegOrderUnknown {
		t.Fatalf("tenant 2 pegs tenant 1 orders: err=%v", err)
	}
	if err := s.DeletePeg(two, ab.ID); problemCode(err) != store.CodePegNotFound {
		t.Fatalf("tenant 2 deletes a tenant 1 peg: err=%v", err)
	}
	if err := s.DeletePeg(ctx, ab.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePeg(ctx, ab.ID); problemCode(err) != store.CodePegNotFound {
		t.Fatalf("second delete: err=%v", err)
	}
	// With a -> b gone, c -> a no longer closes a loop.
	if _, _, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: c, DependentOrderID: a}, store.Idem{}); err != nil {
		t.Fatalf("c -> a after removing a -> b: err=%v", err)
	}
}

// AC-23.3 through the store: B consumes A's output. Moving A's last job later
// with propagate previews B's delay, and the commit moves B and announces new
// dates for both orders. Without propagate B stays where it was.
func TestMoveJob_PropagatesToPeggedOrders(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)
	mix, fill := mustResource(t, ctx, s, "MIX-1", true), mustResource(t, ctx, s, "FILL-1", true)
	a, b := ids.New(), ids.New()
	a1 := mustOperation(t, ctx, s, a, 10, 60, mix.ID)
	a2 := mustOperation(t, ctx, s, a, 20, 60, mix.ID)
	b1 := mustOperation(t, ctx, s, b, 10, 60, fill.ID)
	b2 := mustOperation(t, ctx, s, b, 20, 30, fill.ID)
	if _, _, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: a, DependentOrderID: b}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if bars := barsOf(t, ctx, s); !bars[a1.ID].Start.Equal(at(0, 8, 0)) || !bars[b1.ID].Start.Equal(at(0, 8, 0)) {
		t.Fatalf("setup: a1 %v, b1 %v; want both at 08:00", bars[a1.ID].Start, bars[b1.ID].Start)
	}

	req := store.MoveRequest{JobID: a2.ID, Start: at(1, 9, 0), HorizonDays: 60, DryRun: true}
	plain, _, err := s.MoveJob(ctx, req, store.Idem{})
	if err != nil || plain.Conflict != nil || len(plain.Dependents) != 0 || len(plain.Changes) != 1 {
		t.Fatalf("move without propagate = %+v err=%v, want only a2", plain, err)
	}
	req.Propagate = true
	preview, _, err := s.MoveJob(ctx, req, store.Idem{})
	if err != nil || preview.Conflict != nil || preview.Truncated || len(preview.Dependents) != 1 {
		t.Fatalf("preview = %+v err=%v", preview, err)
	}
	dep := preview.Dependents[0]
	if dep.OrderID != b || !dep.OldEnd.Equal(at(0, 9, 30)) || !dep.NewStart.Equal(at(1, 10, 0)) || !dep.NewEnd.Equal(at(1, 11, 30)) {
		t.Fatalf("dependent = %+v, want B moving from ending 09:30 to 10:00-11:30 the next day", dep)
	}
	if !preview.OrderEnd.Equal(at(1, 10, 0)) {
		t.Fatalf("order end %v, want A's own end 10:00 (not B's)", preview.OrderEnd)
	}
	if bars := barsOf(t, ctx, s); !bars[b1.ID].Start.Equal(at(0, 8, 0)) {
		t.Fatal("a preview must not move the dependent")
	}

	datesBefore := outboxCount(t, admin, schedevents.OrderDatesChanged)
	req.DryRun = false
	done, _, err := s.MoveJob(ctx, req, store.Idem{Key: "move-p", Fingerprint: "fp"})
	if err != nil || done.Conflict != nil || done.Run == nil || len(done.Changes) != 3 {
		t.Fatalf("commit = %+v err=%v, want a2, b1 and b2 changed", done, err)
	}
	bars := barsOf(t, ctx, s)
	if !bars[b1.ID].Start.Equal(at(1, 10, 0)) || !bars[b2.ID].Start.Equal(at(1, 11, 0)) || !bars[a1.ID].Start.Equal(at(0, 8, 0)) {
		t.Fatalf("after commit a1 %v, b1 %v, b2 %v", bars[a1.ID].Start, bars[b1.ID].Start, bars[b2.ID].Start)
	}
	if got := outboxCount(t, admin, schedevents.OrderDatesChanged); got != datesBefore+2 {
		t.Fatalf("orderdates.changed events = %d, want %d (A and B)", got, datesBefore+2)
	}
}

// BR-23.10: a pegging chain deeper than 5 levels is cut, the move reports it
// and the run records SCHED-0007; the sixth level is not touched.
func TestMoveJob_PropagationStopsAtFiveLevels(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	orders := make([]string, 7)
	jobs := make([]store.Operation, 7)
	for i := range orders {
		orders[i] = ids.New()
		r := mustResource(t, ctx, s, "R-"+string(rune('A'+i)), true)
		jobs[i] = mustOperation(t, ctx, s, orders[i], 10, 60, r.ID)
		if i > 0 {
			if _, _, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: orders[i-1], DependentOrderID: orders[i]}, store.Idem{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	out, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: jobs[0].ID, Start: at(0, 14, 0), HorizonDays: 60, Propagate: true}, store.Idem{Key: "deep", Fingerprint: "fp"})
	if err != nil || out.Conflict != nil || !out.Truncated || len(out.Dependents) != 5 {
		t.Fatalf("outcome = %+v err=%v, want 5 dependents and truncated", out, err)
	}
	if !slices.ContainsFunc(out.Run.Messages, func(m string) bool { return strings.HasPrefix(m, domain.CodePropagationTruncated) }) {
		t.Fatalf("run messages %v, want SCHED-0007", out.Run.Messages)
	}
	bars := barsOf(t, ctx, s)
	if !bars[jobs[5].ID].Start.Equal(at(1, 11, 0)) || !bars[jobs[6].ID].Start.Equal(at(0, 8, 0)) {
		t.Fatalf("level 5 at %v (want next day 11:00), level 6 at %v (want untouched 08:00)", bars[jobs[5].ID].Start, bars[jobs[6].ID].Start)
	}
}

// FR-23.17: a recurring block (every Monday 08:00-12:00) is stored, listed
// and expanded by runs like a weekly one, and the shapes stay exclusive.
func TestCapacityBlock_RecurringWindowSteersARun(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "PAINT-1", true)
	rec := &store.BlockRecurrence{Rule: "FREQ=WEEKLY;BYDAY=MO", StartDate: "2026-12-28", StartMinute: 8 * 60, EndMinute: 12 * 60}
	b, created, err := s.CreateCapacityBlock(ctx, store.CapacityBlock{ResourceID: r.ID, Code: "MON-WHITE", Recurrence: rec, Priority: 10, Active: true,
		Condition: []domain.Predicate{{Attribute: "color", Op: "=", Value: "white"}}}, store.Idem{})
	if err != nil || !created || b.Recurrence == nil || *b.Recurrence != *rec || b.Windows != nil || b.StartAt != nil {
		t.Fatalf("block = %+v created=%v err=%v", b, created, err)
	}
	if listed, err := s.ListCapacityBlocks(ctx, r.ID); err != nil || len(listed) != 1 || listed[0].Recurrence == nil {
		t.Fatalf("listed = %+v err=%v", listed, err)
	}
	start, end := at(0, 6, 0), at(0, 7, 0)
	if _, _, err := s.CreateCapacityBlock(ctx, store.CapacityBlock{ResourceID: r.ID, Code: "BOTH", Recurrence: rec, StartAt: &start, EndAt: &end,
		Condition: []domain.Predicate{}, Active: true}, store.Idem{}); err == nil {
		t.Fatal("a block with a recurrence and a dated window must be refused by the table")
	}
	paint := func(color string, due int) store.Operation {
		dueAt := at(0, due, 0)
		op, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 120, Priority: 100, DueAt: &dueAt,
			Attributes: map[string]string{"color": color}, ResourceIDs: []string{r.ID}}, store.Idem{})
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	red, white := paint("red", 9), paint("white", 10)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	bars := barsOf(t, ctx, s)
	if !bars[white.ID].Start.Equal(at(0, 8, 0)) || !bars[red.ID].Start.Equal(at(0, 12, 0)) {
		t.Fatalf("white %v, red %v; want white at 08:00 in the recurring block and red at 12:00 after it", bars[white.ID].Start, bars[red.ID].Start)
	}
}
