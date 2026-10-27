package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/domain"
	"github.com/BenjaminSRussell/passage/services/scheduling/internal/store"
)

// Block utilization through the database: the white job fills half of the
// Monday white block, the red job placed after the block is not counted, an
// inactive block is left out, and a recurring block on a Berlin resource is
// expanded in local time.
func TestBlockUtilization_CountsMatchedMinutesPerWindow(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	r := mustResource(t, ctx, s, "PAINT-1", true)
	white := []domain.Predicate{{Attribute: "color", Op: "=", Value: "white"}}
	for _, b := range []store.CapacityBlock{
		{ResourceID: r.ID, Code: "MON-WHITE", Windows: []domain.Shift{{Weekday: 1, StartMinute: 8 * 60, EndMinute: 12 * 60}}, Priority: 10, Active: true, Condition: white},
		{ResourceID: r.ID, Code: "OFF", Windows: []domain.Shift{{Weekday: 2, StartMinute: 8 * 60, EndMinute: 12 * 60}}, Priority: 5, Active: false, Condition: white},
	} {
		if _, _, err := s.CreateCapacityBlock(ctx, b, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, color := range []string{"red", "white"} {
		due := at(0, 10, 0)
		if _, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 120, Priority: 100, DueAt: &due,
			Attributes: map[string]string{"color": color}, ResourceIDs: []string{r.ID}}, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}

	got, err := s.BlockUtilization(ctx, r.ID, at(0, 0, 0), at(2, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("windows = %+v, want only the Monday window of the active block", got)
	}
	w := got[0]
	if w.Code != "MON-WHITE" || !w.Start.Equal(at(0, 8, 0)) || !w.End.Equal(at(0, 12, 0)) ||
		w.BlockMinutes != 240 || w.MatchedMinutes != 120 || w.UnusedMinutes != 120 || len(w.Condition) != 1 {
		t.Fatalf("window = %+v, want MON-WHITE 08:00-12:00 holding 240 min, 120 used by the white job", w)
	}
	// Clipping: from 09:00 the window holds 180 minutes, 60 of them used.
	if got, err := s.BlockUtilization(ctx, r.ID, at(0, 9, 0), at(1, 0, 0)); err != nil || len(got) != 1 ||
		!got[0].Start.Equal(at(0, 9, 0)) || got[0].BlockMinutes != 180 || got[0].MatchedMinutes != 60 {
		t.Fatalf("clipped = %+v err=%v", got, err)
	}

	berlin, _, err := s.CreateResource(ctx, store.Resource{Code: "PAINT-B", Name: "Berlin", Finite: true, TimeZone: "Europe/Berlin", Shifts: weekdays(8*60, 16*60)}, store.Idem{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateCapacityBlock(ctx, store.CapacityBlock{ResourceID: berlin.ID, Code: "EARLY", Priority: 1, Active: true, Condition: white,
		Recurrence: &store.BlockRecurrence{Rule: "FREQ=DAILY;COUNT=2", StartDate: "2027-01-05", StartMinute: 8 * 60, EndMinute: 9 * 60}}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	got, err = s.BlockUtilization(ctx, berlin.ID, at(0, 0, 0), at(7, 0, 0))
	if err != nil || len(got) != 2 {
		t.Fatalf("recurring windows = %+v err=%v, want two", got, err)
	}
	if !got[0].Start.Equal(at(1, 7, 0)) || !got[1].Start.Equal(at(2, 7, 0)) || got[0].BlockMinutes != 60 || got[1].UnusedMinutes != 60 {
		t.Fatalf("recurring windows = %+v, want 08:00-09:00 Berlin (07:00 UTC) on Tuesday and Wednesday", got)
	}

	if _, err := s.BlockUtilization(tenantCtx(2), r.ID, at(0, 0, 0), at(1, 0, 0)); problemCode(err) != store.CodeResourceNotFound {
		t.Fatalf("tenant 2 reads tenant 1 blocks: err=%v", err)
	}
	if out, err := s.BlockUtilization(ctx, berlin.ID, at(14, 0, 0), at(15, 0, 0)); err != nil || out == nil || len(out) != 0 {
		t.Fatalf("range without windows = %#v err=%v, want an empty list", out, err)
	}
}
