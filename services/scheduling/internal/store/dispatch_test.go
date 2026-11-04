package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

func TestDispatchRows_ListsPrimaryAndSecondaryBookingsInTheWindow(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	m, tool := mustResource(t, ctx, s, "M1", true), mustResource(t, ctx, s, "TOOL", true)
	a := secOperation(t, ctx, s, 10, 120, m.ID, tool.ID)
	b := secOperation(t, ctx, s, 20, 90, m.ID)
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	rows, truncated, err := s.DispatchRows(ctx, m.ID, monday, at(7, 0, 0))
	if err != nil || truncated || len(rows) != 2 {
		t.Fatalf("rows = %+v truncated=%v err=%v", rows, truncated, err)
	}
	lines, total := domain.DispatchLines(rows, monday, at(7, 0, 0))
	if total != 210 || lines[0].Sequence != 1 || lines[0].Role != "primary" || !lines[0].Finite {
		t.Fatalf("lines = %+v total %d, want both jobs, 210 minutes", lines, total)
	}
	seen := map[string]bool{lines[0].JobID: true, lines[1].JobID: true}
	if !seen[a.ID] || !seen[b.ID] || !lines[1].Start.Equal(lines[0].End) && lines[1].Start.Before(lines[0].End) {
		t.Fatalf("lines = %+v, want jobs %s and %s back to back", lines, a.ID, b.ID)
	}
	if _, _, err := s.FirmJob(ctx, a.ID, true, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	sec, _, err := s.DispatchRows(ctx, tool.ID, monday, at(7, 0, 0))
	if err != nil || len(sec) != 1 || sec[0].Role != "secondary" || sec[0].JobID != a.ID {
		t.Fatalf("tool rows = %+v err=%v, want the secondary booking of operation 10", sec, err)
	}
	// The secondary line shows the order and operation of the job and the primary's firmed flag.
	if sec[0].OrderID != a.OrderID || sec[0].OperNumber != 10 || !sec[0].Firmed || sec[0].SetupGroup != "" || sec[0].ChangeoverMins != 0 {
		t.Fatalf("secondary row = %+v, want order %s, operation 10, firmed, no setup", sec[0], a.OrderID)
	}
	// A window after the plan holds nothing.
	if none, _, err := s.DispatchRows(ctx, m.ID, at(30, 0, 0), at(31, 0, 0)); err != nil || len(none) != 0 {
		t.Fatalf("late window = %+v err=%v", none, err)
	}
}

func TestDispatchRows_UnknownResourceAndOtherTenant(t *testing.T) {
	s, _, _ := setup(t)
	one, two := tenantCtx(1), tenantCtx(2)
	r := mustResource(t, one, s, "M1", true)
	secOperation(t, one, s, 10, 60, r.ID)
	if _, _, err := s.RunSchedule(one, siteRun(monday), store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.DispatchRows(two, r.ID, monday, at(7, 0, 0)); problemCode(err) != store.CodeResourceNotFound {
		t.Fatalf("tenant 2 reads tenant 1 resource: err=%v", err)
	}
	if rows, _, err := s.DispatchRows(one, r.ID, monday, at(7, 0, 0)); err != nil || len(rows) != 1 {
		t.Fatalf("own rows = %+v err=%v", rows, err)
	}
}
