package store_test

import (
	"testing"

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
