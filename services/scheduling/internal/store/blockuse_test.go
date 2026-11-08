package store_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/httpx"
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

// The cross-resource report totals every window of each active block, skips
// resources without active blocks, rounds the share half up and refuses an
// unknown resource.
func TestBlockReport_TotalsBlocksAcrossResources(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	white := []domain.Predicate{{Attribute: "color", Op: "=", Value: "white"}}
	paint := mustResource(t, ctx, s, "PAINT-1", true)
	mustResource(t, ctx, s, "PLAIN-1", true) // no blocks: left out
	pack := mustResource(t, ctx, s, "PACK-1", true)
	for _, b := range []store.CapacityBlock{
		{ResourceID: paint.ID, Code: "MON-WHITE", Windows: []domain.Shift{{Weekday: 1, StartMinute: 8 * 60, EndMinute: 12 * 60}}, Priority: 10, Active: true, Condition: white},
		{ResourceID: paint.ID, Code: "OFF", Windows: []domain.Shift{{Weekday: 2, StartMinute: 8 * 60, EndMinute: 12 * 60}}, Priority: 5, Active: false, Condition: white},
		{ResourceID: pack.ID, Code: "MON-PACK", Windows: []domain.Shift{{Weekday: 1, StartMinute: 8 * 60, EndMinute: 10 * 60}}, Priority: 1, Active: true, Condition: white},
	} {
		if _, _, err := s.CreateCapacityBlock(ctx, b, store.Idem{}); err != nil {
			t.Fatal(err)
		}
	}
	due := at(0, 10, 0)
	if _, _, err := s.CreateOperation(ctx, store.Operation{OrderID: ids.New(), OperNumber: 10, Minutes: 120, Priority: 100, DueAt: &due,
		Attributes: map[string]string{"color": "white"}, ResourceIDs: []string{paint.ID}}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.RunSchedule(ctx, siteRun(at(0, 6, 0)), store.Idem{}); err != nil {
		t.Fatal(err)
	}

	got, err := s.BlockReport(ctx, "", at(0, 0, 0), at(2, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 || got.Items[0].ResourceCode != "PACK-1" || got.Items[1].ResourceCode != "PAINT-1" {
		t.Fatalf("items = %+v, want PACK-1 then PAINT-1 only", got.Items)
	}
	pk, pt := got.Items[0], got.Items[1]
	if pk.BlockMinutes != 120 || pk.MatchedMinutes != 0 || pk.UtilizationPct == nil || *pk.UtilizationPct != 0 || pk.Windows != 1 {
		t.Fatalf("pack = %+v", pk)
	}
	if pt.Code != "MON-WHITE" || pt.BlockMinutes != 240 || pt.MatchedMinutes != 120 || pt.UnusedMinutes != 120 || pt.UtilizationPct == nil || *pt.UtilizationPct != 50 {
		t.Fatalf("paint = %+v", pt)
	}
	if got.BlockMinutes != 360 || got.MatchedMinutes != 120 || got.UnusedMinutes != 240 || got.UtilizationPct == nil || *got.UtilizationPct != 33 || got.Truncated {
		t.Fatalf("totals = %+v", got)
	}
	one, err := s.BlockReport(ctx, paint.ID, at(0, 0, 0), at(2, 0, 0))
	if err != nil || len(one.Items) != 1 || one.Items[0].ResourceCode != "PAINT-1" {
		t.Fatalf("one resource = %+v err=%v", one, err)
	}
	// A range with no window of any block lists nothing and has no share.
	empty, err := s.BlockReport(ctx, "", at(1, 12, 0), at(1, 13, 0))
	if err != nil || len(empty.Items) != 0 || empty.UtilizationPct != nil || empty.Items == nil {
		t.Fatalf("empty = %+v err=%v", empty, err)
	}
	if _, err := s.BlockReport(ctx, ids.New(), at(0, 0, 0), at(1, 0, 0)); err == nil {
		t.Fatal("unknown resource must be refused")
	}
}

// The report is per tenant: another tenant sees no rows and gets SCHED-0102
// for a resource of this one. A named resource without blocks is an empty
// report, not a missing resource.
func TestBlockReport_IsolatesTenantsAndListsANamedResourceWithoutBlocks(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	paint := mustResource(t, ctx, s, "PAINT-1", true)
	plain := mustResource(t, ctx, s, "PLAIN-1", true)
	if _, _, err := s.CreateCapacityBlock(ctx, store.CapacityBlock{ResourceID: paint.ID, Code: "MON", Windows: []domain.Shift{{Weekday: 1, StartMinute: 8 * 60, EndMinute: 12 * 60}},
		Priority: 1, Active: true, Condition: []domain.Predicate{{Attribute: "color", Op: "=", Value: "white"}}}, store.Idem{}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.BlockReport(ctx, plain.ID, at(0, 0, 0), at(1, 0, 0)); err != nil || len(got.Items) != 0 || got.UtilizationPct != nil {
		t.Fatalf("named resource without blocks = %+v err=%v", got, err)
	}
	other := tenantCtx(2)
	if got, err := s.BlockReport(other, "", at(0, 0, 0), at(1, 0, 0)); err != nil || len(got.Items) != 0 || got.BlockMinutes != 0 {
		t.Fatalf("other tenant = %+v err=%v, want no rows", got, err)
	}
	var p *httpx.Problem
	if _, err := s.BlockReport(other, paint.ID, at(0, 0, 0), at(1, 0, 0)); !errors.As(err, &p) || p.Code != store.CodeResourceNotFound {
		t.Fatalf("other tenant naming the resource: err=%v, want %s", err, store.CodeResourceNotFound)
	}
}

// More resources with active blocks than the report reads: the first
// MaxBlockReportResources by code are listed and Truncated is set; exactly
// that many is not truncated.
func TestBlockReport_TruncatesAfterTheResourceLimit(t *testing.T) {
	s, _, _ := setup(t)
	ctx := tenantCtx(1)
	cond := []domain.Predicate{{Attribute: "color", Op: "=", Value: "white"}}
	for i := 0; i <= store.MaxBlockReportResources; i++ {
		r := mustResource(t, ctx, s, fmt.Sprintf("R-%03d", i), true)
		if _, _, err := s.CreateCapacityBlock(ctx, store.CapacityBlock{ResourceID: r.ID, Code: "MON", Windows: []domain.Shift{{Weekday: 1, StartMinute: 8 * 60, EndMinute: 9 * 60}},
			Priority: 1, Active: true, Condition: cond}, store.Idem{}); err != nil {
			t.Fatal(err)
		}
		if i == store.MaxBlockReportResources-1 {
			got, err := s.BlockReport(ctx, "", at(0, 0, 0), at(1, 0, 0))
			if err != nil || got.Truncated || len(got.Items) != store.MaxBlockReportResources {
				t.Fatalf("at the limit: %d items truncated=%v err=%v", len(got.Items), got.Truncated, err)
			}
		}
	}
	got, err := s.BlockReport(ctx, "", at(0, 0, 0), at(1, 0, 0))
	if err != nil || !got.Truncated || len(got.Items) != store.MaxBlockReportResources {
		t.Fatalf("over the limit: %d items truncated=%v err=%v", len(got.Items), got.Truncated, err)
	}
	if last := got.Items[len(got.Items)-1].ResourceCode; last != fmt.Sprintf("R-%03d", store.MaxBlockReportResources-1) {
		t.Fatalf("last listed resource = %s, want the first %d by code", last, store.MaxBlockReportResources)
	}
	if got.BlockMinutes != 60*store.MaxBlockReportResources {
		t.Fatalf("totals = %d minutes, want the listed rows only", got.BlockMinutes)
	}
}
