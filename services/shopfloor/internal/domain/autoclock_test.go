package domain

import (
	"testing"
	"time"
)

func TestPlanAutoClockOut_OnlyShiftsOlderThanThreshold(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	open := []OpenShift{
		{WorkerID: "a", RegistrationID: "ra", ClockedInAt: now.Add(-17 * time.Hour)},
		{WorkerID: "b", RegistrationID: "rb", ClockedInAt: now.Add(-16 * time.Hour)},
		{WorkerID: "c", RegistrationID: "rc", ClockedInAt: now.Add(-15 * time.Hour)},
		{WorkerID: "d", RegistrationID: "rd", ClockedInAt: now.Add(time.Hour)},
	}
	got := PlanAutoClockOut(open, now, 16)
	if len(got) != 2 || got[0].WorkerID != "a" || got[1].WorkerID != "b" {
		t.Fatalf("plan = %+v", got)
	}
	// The clock-out is the clock-in plus the threshold, not the time the sweep ran.
	if want := open[0].ClockedInAt.Add(16 * time.Hour); !got[0].ClockOutAt.Equal(want) {
		t.Fatalf("clock-out at %v, want %v", got[0].ClockOutAt, want)
	}
}

func TestCheckAutoClockOutHours(t *testing.T) {
	for _, h := range []int{4, 16, 24} {
		if err := CheckAutoClockOutHours(h); err != nil {
			t.Fatalf("%d: %v", h, err)
		}
	}
	for _, h := range []int{-1, 0, 3, 25} {
		if err := CheckAutoClockOutHours(h); err == nil {
			t.Fatalf("%d accepted", h)
		}
	}
}
