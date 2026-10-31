package domain

import (
	"errors"
	"testing"
)

func TestValidateTracking(t *testing.T) {
	ok := []TrackingEntry{
		{BatchNo: "L-2026-01", LocationCode: "A-01", Qty: d("5")},
		{BatchNo: "L-2026-01", LocationCode: "A-02", Qty: d("2.5")},
		{SerialNo: "SN-1", Qty: d("1")},
		{BatchNo: "L-2026-02", SerialNo: "SN-2", Qty: d("1")},
	}
	if err := ValidateTracking(ok); err != nil {
		t.Fatalf("valid entries refused: %v", err)
	}
	bad := map[string][]TrackingEntry{
		"no dimension":     {{Qty: d("1")}},
		"zero qty":         {{BatchNo: "B1", Qty: d("0")}},
		"serial two units": {{SerialNo: "S1", Qty: d("2")}},
		"duplicate serial": {{SerialNo: "S1", Qty: d("1")}, {SerialNo: "S1", Qty: d("1")}},
		"duplicate batch":  {{BatchNo: "B1", LocationCode: "A", Qty: d("1")}, {BatchNo: "B1", LocationCode: "A", Qty: d("1")}},
		"bad code":         {{BatchNo: "B 1", Qty: d("1")}},
		"bad location":     {{BatchNo: "B1", LocationCode: "a b", Qty: d("1")}},
	}
	for name, e := range bad {
		if err := ValidateTracking(e); !errors.Is(err, ErrTrackingEntry) {
			t.Errorf("%s: want ErrTrackingEntry, got %v", name, err)
		}
	}
}

func TestTrackedQuantityRules(t *testing.T) {
	e := []TrackingEntry{{BatchNo: "B1", Qty: d("4")}, {BatchNo: "B2", Qty: d("6")}}
	if got := TrackedQty(e); !got.Equal(d("10")) {
		t.Fatalf("tracked = %s", got)
	}
	if err := CheckTrackedWithinPicked(d("10"), d("10")); err != nil {
		t.Fatal(err)
	}
	if err := CheckTrackedWithinPicked(d("11"), d("10")); !errors.Is(err, ErrTrackingQty) {
		t.Fatalf("want ErrTrackingQty, got %v", err)
	}
	if err := CheckTrackedForPost(true, d("10"), d("9")); !errors.Is(err, ErrTrackingRequired) {
		t.Fatalf("short tracking must fail: %v", err)
	}
	if err := CheckTrackedForPost(true, d("10"), d("10")); err != nil {
		t.Fatal(err)
	}
	if err := CheckTrackedForPost(false, d("10"), d("0")); err != nil {
		t.Fatalf("untracked line must pass: %v", err)
	}
}
