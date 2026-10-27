package domain

import (
	"errors"
	"testing"
	"time"

	"pgregory.net/rapid"
)

var offlineBase = time.Date(2026, 10, 21, 8, 0, 0, 0, time.UTC)

func TestOrderReplay_BR25_7(t *testing.T) {
	in := []ReplayAction{
		{ID: "c", ClientAt: offlineBase.Add(2 * time.Minute)},
		{ID: "a", ClientAt: offlineBase},
		{ID: "b", ClientAt: offlineBase},
		{ID: "a", ClientAt: offlineBase.Add(9 * time.Minute)}, // duplicate client UUID: no-op
	}
	got := OrderReplay(in)
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %d actions, want %d", len(got), len(want))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Fatalf("position %d = %s, want %s", i, got[i].ID, id)
		}
	}
	if !got[0].ClientAt.Equal(offlineBase) {
		t.Fatal("duplicate replaced the first occurrence")
	}
	if len(in) != 4 || in[0].ID != "c" {
		t.Fatal("OrderReplay modified its input")
	}
}

func TestOrderReplay_SortedPermutationProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(0, 60).Draw(t, "n")
		in := make([]ReplayAction, n)
		for i := range in {
			in[i] = ReplayAction{
				ID:       rapid.StringMatching(`[a-f0-9]{4}`).Draw(t, "id"),
				ClientAt: offlineBase.Add(time.Duration(rapid.IntRange(0, 600).Draw(t, "sec")) * time.Second),
			}
		}
		got := OrderReplay(in)
		ids := map[string]bool{}
		for i, a := range got {
			if ids[a.ID] {
				t.Fatalf("duplicate %s survived", a.ID)
			}
			ids[a.ID] = true
			if i > 0 && got[i-1].ClientAt.After(a.ClientAt) {
				t.Fatal("replay not in client time order")
			}
		}
		distinct := map[string]bool{}
		for _, a := range in {
			distinct[a.ID] = true
		}
		if len(got) != len(distinct) {
			t.Fatalf("kept %d, want %d distinct", len(got), len(distinct))
		}
	})
}

func TestFinishedConflicts_BR25_7(t *testing.T) {
	actions := []ReplayAction{
		{ID: "s1", Kind: "job-start", JobID: "J-1"},
		{ID: "s2", Kind: "job-start", JobID: "J-2"},
		{ID: "f1", Kind: "job-stop", JobID: "J-1"},
		{ID: "s3", Kind: "job-start", JobID: ""},
	}
	flags := FinishedConflicts(actions, map[string]bool{"J-1": true})
	if len(flags) != 1 || flags["s1"] != ConflictJobFinished {
		t.Fatalf("flags = %v, want only s1 flagged", flags)
	}
}

func TestClockSkew_BR25_7(t *testing.T) {
	tests := []struct {
		name       string
		offset     time.Duration
		exceeded   bool
		correction time.Duration
	}{
		{"within tolerance is kept", 4 * time.Minute, false, 0},
		{"exactly five minutes is kept", 5 * time.Minute, false, 0},
		{"ahead of server by more than five", -6 * time.Minute, true, -6 * time.Minute},
		{"behind server by more than five", 7 * time.Minute, true, 7 * time.Minute},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := SkewExceeded(tc.offset); got != tc.exceeded {
				t.Fatalf("SkewExceeded = %v, want %v", got, tc.exceeded)
			}
			got := CorrectedClientTime(offlineBase, tc.offset)
			if want := offlineBase.Add(tc.correction); !got.Equal(want) {
				t.Fatalf("corrected = %s, want %s", got, want)
			}
		})
	}
	if off := ClockOffset(offlineBase, offlineBase.Add(time.Minute)); off != time.Minute {
		t.Fatalf("ClockOffset = %s, want 1m", off)
	}
}

func TestCheckQueueCapacity_SF0010(t *testing.T) {
	if err := CheckQueueCapacity(39, 40); err != nil {
		t.Fatalf("40th action refused: %v", err)
	}
	err := CheckQueueCapacity(40, 40)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeQueueFull {
		t.Fatalf("err = %v, want %s", err, CodeQueueFull)
	}
	if err := CheckQueueCapacity(4999, 0); err != nil {
		t.Fatalf("zero max should use the default limit: %v", err)
	}
}
