package domain

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// TestProjectStops_AC347 is AC-34.7 with BR-34.9: a D1 with no earlier AF sets the
// stop DELIVERED and backfills the pickup as inferred.
func TestProjectStops_AC347(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	stops := ProjectStops([]TrackingEvent{
		{LoadID: "load-1", StopNo: 2, Code: "D1", OccurredAt: t0.Add(3 * time.Hour), Source: "EDI214", Ref: "214-9"},
	})
	if len(stops) != 1 {
		t.Fatalf("stops = %d, want 1", len(stops))
	}
	s := stops[0]
	if s.StopNo != 2 || s.Status != StopDelivered || s.DeliveredAt == nil || !s.DeliveredAt.Equal(t0.Add(3*time.Hour)) {
		t.Fatalf("stop = %+v, want stop 2 DELIVERED at 11:00", s)
	}
	if !s.PickupInferred || s.PickedUpAt == nil || !s.PickedUpAt.Equal(*s.DeliveredAt) {
		t.Fatalf("pickup not inferred from the delivery: %+v", s)
	}
}

// TestProjectStops_AppliesByOccurredAt: a pickup that occurred before the delivery
// but was ingested after it is a real pickup, not an inferred one.
func TestProjectStops_AppliesByOccurredAt(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	s := ProjectStops([]TrackingEvent{
		{StopNo: 1, Code: "D1", OccurredAt: t0.Add(2 * time.Hour), Source: "API", Ref: "a"},
		{StopNo: 1, Code: "AF", OccurredAt: t0, Source: "EDI214", Ref: "b"},
		{StopNo: 1, Code: "X3", OccurredAt: t0.Add(-30 * time.Minute), Source: "EDI214", Ref: "c"},
	})[0]
	if s.PickupInferred || s.PickedUpAt == nil || !s.PickedUpAt.Equal(t0) || s.Status != StopDelivered {
		t.Fatalf("stop = %+v, want the real pickup at 08:00 and DELIVERED", s)
	}
	if s.ArrivedAt == nil || !s.ArrivedAt.Equal(t0.Add(-30*time.Minute)) {
		t.Fatalf("arrivedAt = %v, want 07:30", s.ArrivedAt)
	}
}

// TestProjectStops_StatusNeverGoesBack: an arrival reported after the pickup
// keeps the stop PICKED_UP, and stops are returned in stop order.
func TestProjectStops_StatusNeverGoesBack(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	stops := ProjectStops([]TrackingEvent{
		{StopNo: 3, Code: "X1", OccurredAt: t0, Source: "API", Ref: "s3"},
		{StopNo: 1, Code: "AF", OccurredAt: t0, Source: "API", Ref: "a"},
		{StopNo: 1, Code: "X3", OccurredAt: t0.Add(time.Hour), Source: "API", Ref: "b"},
	})
	if len(stops) != 2 || stops[0].StopNo != 1 || stops[1].StopNo != 3 {
		t.Fatalf("stops = %+v, want stops 1 and 3 in order", stops)
	}
	if stops[0].Status != StopPickedUp || stops[1].Status != StopArrived {
		t.Fatalf("statuses = %s, %s", stops[0].Status, stops[1].Status)
	}
}

func TestProjectStops_SkipsUnknownCodes(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	if stops := ProjectStops([]TrackingEvent{{StopNo: 1, Code: "ZZ", OccurredAt: t0, Source: "API", Ref: "x"}}); len(stops) != 0 {
		t.Fatalf("unknown code produced stops: %+v", stops)
	}
	if IsTrackingCode("ZZ") || !IsTrackingCode("D1") {
		t.Fatal("IsTrackingCode disagrees with the supported codes")
	}
}

func TestSameReport(t *testing.T) {
	t0 := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	a := TrackingEvent{ID: "x", LoadID: "l", StopNo: 1, Code: "AF", OccurredAt: t0, Source: "API", Ref: "r", ReceivedAt: t0}
	b := a
	b.ID, b.ReceivedAt = "y", t0.Add(time.Hour)
	if !a.SameReport(b) {
		t.Fatal("ids and receipt times are not part of the report")
	}
	b.Code = "D1"
	if a.SameReport(b) {
		t.Fatal("a different code is a different report")
	}
}

// TestProjectStops_PropertyOrderIndependent checks BR-34.9: the projection
// depends on occurred_at, not on the order events were ingested.
func TestProjectStops_PropertyOrderIndependent(t *testing.T) {
	base := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 12).Draw(t, "events")
		events := make([]TrackingEvent, n)
		for i := range events {
			events[i] = TrackingEvent{
				StopNo:     rapid.IntRange(1, 3).Draw(t, fmt.Sprintf("stop%d", i)),
				Code:       rapid.SampledFrom(TrackingCodes).Draw(t, fmt.Sprintf("code%d", i)),
				OccurredAt: base.Add(time.Duration(rapid.IntRange(0, 120).Draw(t, fmt.Sprintf("minute%d", i))) * time.Minute),
				Source:     "EDI214",
				Ref:        fmt.Sprintf("ref-%02d", i),
			}
		}
		perm := rapid.Permutation(events).Draw(t, "ingest order")
		forward, shuffled := ProjectStops(events), ProjectStops(perm)
		if !reflect.DeepEqual(forward, shuffled) {
			t.Fatalf("ingest order changed the projection:\n%v\n%v", forward, shuffled)
		}
		for _, s := range forward {
			if s.PickupInferred && (s.PickedUpAt == nil || s.DeliveredAt == nil) {
				t.Fatalf("inferred pickup without a delivery: %+v", s)
			}
			if (s.Status == StopDelivered) != (s.DeliveredAt != nil) {
				t.Fatalf("status %s disagrees with deliveredAt %v", s.Status, s.DeliveredAt)
			}
		}
	})
}
