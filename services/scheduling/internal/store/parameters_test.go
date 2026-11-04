package store_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func TestParameters_DefaultsThenVersionedChanges(t *testing.T) {
	s, _, admin := setup(t)
	ctx := tenantCtx(1)

	p, err := s.GetParameters(ctx)
	if err != nil || p.Version != 1 || p.UpdatedAt != nil || p.DefaultDirection != "forward" || !p.DefaultFinite || p.DefaultMode != "jobs" ||
		p.DragSnapMinutes != 5 || p.PropagationOrders != 500 || p.PropagationLevels != 5 {
		t.Fatalf("defaults = %+v err=%v", p, err)
	}

	dir, finite, snap := "backward", false, 15
	upd, replayed, err := s.UpdateParameters(ctx, 1, store.ParametersPatch{DefaultDirection: &dir, DefaultFinite: &finite, DragSnapMinutes: &snap}, idem("p1"))
	if err != nil || replayed || upd.Version != 2 || upd.UpdatedAt == nil || upd.DefaultDirection != "backward" || upd.DefaultFinite || upd.DragSnapMinutes != 15 || upd.PropagationLevels != 5 {
		t.Fatalf("first change = %+v replayed=%v err=%v", upd, replayed, err)
	}
	if got, _ := s.GetParameters(ctx); got.Version != 2 || got.DefaultDirection != "backward" {
		t.Fatalf("read back = %+v", got)
	}

	// Stale version.
	levels := 3
	if _, _, err := s.UpdateParameters(ctx, 1, store.ParametersPatch{PropagationLevels: &levels}, idem("p2")); problemCode(err) != store.CodeParametersVersion {
		t.Fatalf("stale: %v, want SCHED-0140", err)
	}
	// Same key replays the first answer even though the version moved on.
	again, replayed, err := s.UpdateParameters(ctx, 1, store.ParametersPatch{DefaultDirection: &dir, DefaultFinite: &finite, DragSnapMinutes: &snap}, idem("p1"))
	if err != nil || !replayed || again.Version != 2 {
		t.Fatalf("replay = %+v replayed=%v err=%v", again, replayed, err)
	}
	// Same key, different body.
	if _, _, err := s.UpdateParameters(ctx, 2, store.ParametersPatch{PropagationLevels: &levels}, store.Idem{Key: "p1", Fingerprint: "other"}); problemCode(err) != store.CodeIdempotencyReuse {
		t.Fatalf("reuse: %v", err)
	}
	next, _, err := s.UpdateParameters(ctx, 2, store.ParametersPatch{PropagationLevels: &levels}, idem("p3"))
	if err != nil || next.Version != 3 || next.PropagationLevels != 3 || next.DefaultDirection != "backward" {
		t.Fatalf("second change = %+v err=%v", next, err)
	}
	if got := outboxCount(t, admin, "dev.passage.scheduling.parameters.updated.v1"); got != 2 {
		t.Fatalf("parameters.updated events = %d, want 2 (the replay and the stale change publish nothing)", got)
	}

	// Another tenant still has the defaults.
	if other, err := s.GetParameters(tenantCtx(2)); err != nil || other.Version != 1 || other.DefaultDirection != "forward" {
		t.Fatalf("tenant 2 = %+v err=%v", other, err)
	}
}

func TestParameters_InvalidValueIsRefusedBeforeItIsStored(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	bad := 61
	if _, _, err := s.UpdateParameters(ctx, 1, store.ParametersPatch{DragSnapMinutes: &bad}, idem("b1")); problemCode(err) != "COM-0001" {
		t.Fatalf("invalid snap: %v", err)
	}
	if p, _ := s.GetParameters(ctx); p.Version != 1 || p.DragSnapMinutes != 5 {
		t.Fatalf("a refused change stored %+v", p)
	}
}

// BR-23.10: the move walks as far as the tenant's parameters say.
func TestMoveJob_PropagationLimitFollowsTheParameters(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	orders := make([]string, 4)
	jobs := make([]store.Operation, 4)
	for i := range orders {
		orders[i] = ids.New()
		r := mustResource(t, ctx, s, "R-"+string(rune('A'+i)), true)
		jobs[i] = mustOperation(t, ctx, s, orders[i], 10, 60, r.ID)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(orders); i++ {
		if _, _, err := s.CreatePeg(ctx, store.Peg{ParentOrderID: orders[i-1], DependentOrderID: orders[i]}, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	levels := 2
	if _, _, err := s.UpdateParameters(ctx, 1, store.ParametersPatch{PropagationLevels: &levels}, idem("lv")); err != nil {
		t.Fatal(err)
	}
	out, _, err := s.MoveJob(ctx, store.MoveRequest{JobID: jobs[0].ID, Start: at(0, 14, 0), HorizonDays: 60, Propagate: true}, idem("mv"))
	if err != nil || out.Conflict != nil || !out.Truncated || len(out.Dependents) != 2 {
		t.Fatalf("outcome = %+v err=%v, want 2 dependents and truncated at the 2 levels the parameters allow", out, err)
	}
	if !slices.ContainsFunc(out.Run.Messages, func(m string) bool {
		return strings.HasPrefix(m, domain.CodePropagationTruncated) && strings.Contains(m, "2 orders / 2 levels")
	}) {
		t.Fatalf("run messages %v, want SCHED-0007 naming the 2 levels", out.Run.Messages)
	}
}
