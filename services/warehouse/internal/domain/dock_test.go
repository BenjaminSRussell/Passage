package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func at(h, m int) time.Time { return time.Date(2026, 10, 27, h, m, 0, 0, time.UTC) }

func appt(door string, h1, m1, h2, m2 int) Appointment {
	return Appointment{DoorID: door, DoorCode: "D1", Direction: Inbound, Reference: "ASN-1", Start: at(h1, m1), End: at(h2, m2)}
}

// AC-31.8: with 09:00–09:45 booked on D1, 09:30–10:00 is refused with
// WMS-0108; a touching window or another door is accepted.
func TestCheckDoorSlot_AC31_8(t *testing.T) {
	booked := []Appointment{appt("door-1", 9, 0, 9, 45), appt("door-2", 9, 30, 10, 0)}
	err := CheckDoorSlot(booked, appt("door-1", 9, 30, 10, 0))
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeDoorOverlap {
		t.Fatalf("overlap: %v", err)
	}
	if !strings.Contains(de.Detail, "Door D1 is already booked 2026-10-27 09:00–2026-10-27 09:45 UTC") || !strings.Contains(de.Detail, "DOOR_SLOT_OVERLAP") {
		t.Fatalf("detail = %q", de.Detail)
	}
	for _, ok := range []Appointment{appt("door-1", 9, 45, 10, 0), appt("door-1", 8, 0, 9, 0), appt("door-3", 9, 30, 10, 0)} {
		if err := CheckDoorSlot(booked, ok); err != nil {
			t.Errorf("%v–%v on %s: %v", ok.Start, ok.End, ok.DoorID, err)
		}
	}
	// A window that contains the booked one overlaps too.
	if err := CheckDoorSlot(booked, appt("door-1", 8, 45, 10, 0)); err == nil {
		t.Fatal("enclosing window accepted")
	}
}

func TestOnSlotGrid(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+30*60)
	cases := []struct {
		t    time.Time
		want bool
	}{
		{at(9, 0), true},
		{at(9, 45), true},
		{at(9, 10), false},
		{at(9, 0).Add(time.Second), false},
		{time.Date(2026, 10, 27, 14, 45, 0, 0, ist), true},
	}
	for _, tc := range cases {
		if got := OnSlotGrid(tc.t); got != tc.want {
			t.Errorf("OnSlotGrid(%v) = %v", tc.t, got)
		}
	}
}
