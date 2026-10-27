package domain

import (
	"testing"
	"time"
)

func TestPrinterState_MapsLastJobStatus(t *testing.T) {
	cases := map[string]string{
		"":         StateIdle,
		JobQueued:  StateBusy,
		JobPrinted: StateReady,
		JobFailed:  StateError,
		"UNKNOWN":  StateIdle,
	}
	for in, want := range cases {
		if got := PrinterState(in); got != want {
			t.Errorf("PrinterState(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestRetentionCutoff_SubtractsWholeDaysInUTC(t *testing.T) {
	now := time.Date(2026, 10, 21, 1, 0, 0, 0, time.FixedZone("UTC+5", 5*3600))
	got := RetentionCutoff(now, DefaultRetentionDays)
	want := time.Date(2026, 9, 20, 20, 0, 0, 0, time.UTC)
	if !got.Equal(want) || got.Location() != time.UTC {
		t.Fatalf("cutoff = %s, want %s", got, want)
	}
}
