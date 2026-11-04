package domain

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestMergeResolvesToFurthestState(t *testing.T) {
	cases := []struct {
		from    WOStatus
		kind    ActionKind
		outcome string
	}{
		{WODispatched, ActTravelStart, OutcomeApplied},
		{WODispatched, ActWorkStart, OutcomeApplied}, // an offline device skipped the travel mark
		{WOTraveling, ActTravelStart, OutcomeSuperseded},
		{WOInProgress, ActTravelStart, OutcomeSuperseded},
		{WOInProgress, ActWorkStart, OutcomeSuperseded},
		{WOInProgress, ActHold, OutcomeApplied},
		{WOInProgress, ActComplete, OutcomeApplied},
		{WOTraveling, ActHold, OutcomeRejected},
		{WOOnHold, ActResume, OutcomeApplied},
		{WOOnHold, ActComplete, OutcomeRejected},
		{WOOnHold, ActTravelStart, OutcomeSuperseded},
		{WOInProgress, ActResume, OutcomeSuperseded},
		{WOTraveling, ActResume, OutcomeRejected},
		{WOCompleted, ActWorkStart, OutcomeSuperseded},
		{WOCompleted, ActComplete, OutcomeSuperseded},
		{WOUnscheduled, ActWorkStart, OutcomeRejected},
		{WOScheduled, ActTravelStart, OutcomeRejected},
		{WOPosted, ActComplete, OutcomeRejected},
		{WOCanceled, ActWorkStart, OutcomeRejected},
	}
	for _, c := range cases {
		if got, code, _ := Merge(c.from, c.kind); got != c.outcome || (got == OutcomeRejected) != (code != "") {
			t.Errorf("Merge(%s, %s) = %s %q, want %s", c.from, c.kind, got, code, c.outcome)
		}
	}
}

func TestWorkMinutes(t *testing.T) {
	at := func(m int) time.Time {
		return time.Date(2026, 3, 2, 8, 0, 0, 0, time.UTC).Add(time.Duration(m) * time.Minute)
	}
	got := WorkMinutes([]Mark{{ActComplete, at(100)}, {ActWorkStart, at(10)}, {ActHold, at(40)}, {ActResume, at(60)}, {ActWorkStart, at(61)}})
	if got != 30+40 {
		t.Fatalf("work minutes = %d, want 70", got)
	}
	if WorkMinutes([]Mark{{ActWorkStart, at(0)}}) != 0 || WorkMinutes(nil) != 0 {
		t.Fatal("an open or empty span counts nothing")
	}
}

func TestPartsOutstanding(t *testing.T) {
	lines := []WorkLine{{ID: "a", Kind: KindPart, Qty: decimal.NewFromInt(2)}, {ID: "b", Kind: KindPart, Qty: decimal.NewFromInt(1)}, {ID: "c", Kind: KindLabor, Qty: decimal.NewFromInt(1)}}
	got := PartsOutstanding(lines, map[string]decimal.Decimal{"a": decimal.NewFromInt(1), "b": decimal.NewFromInt(1)})
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("outstanding = %v, want [a]", got)
	}
}
