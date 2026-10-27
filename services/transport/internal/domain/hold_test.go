package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func openHold(statuses ...string) ScreeningHold {
	h := ScreeningHold{ID: "h1", LoadID: "l1", Status: HoldOpen, Version: 1}
	for i, s := range statuses {
		h.Parties = append(h.Parties, PartyScreening{Role: "SHIP_TO", Name: "Party " + string(rune('A'+i)), Status: s})
	}
	return h
}

func holdCode(t *testing.T, err error) string {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want a domain error", err)
	}
	return de.Code
}

func TestScreeningHold_ReleaseRecordsUserReasonAndTime(t *testing.T) {
	h := openHold(ScreenPotentialMatch, ScreenClear)
	at := time.Date(2026, 3, 2, 9, 30, 0, 0, time.FixedZone("CET", 3600))
	got, changed, err := h.Release("officer-7", "  name differs in date of birth  ", at)
	if err != nil || !changed {
		t.Fatalf("release = %v, changed %v", err, changed)
	}
	if got.Status != HoldReleased || got.ReleasedBy != "officer-7" || got.ReleaseReason != "name differs in date of birth" {
		t.Fatalf("released hold = %+v", got)
	}
	if got.ReleasedAt == nil || !got.ReleasedAt.Equal(at) || got.ReleasedAt.Location() != time.UTC {
		t.Fatalf("released at %v, want %v in UTC", got.ReleasedAt, at)
	}
	if got.Version != 2 {
		t.Fatalf("version = %d, want 2", got.Version)
	}
	if h.Status != HoldOpen {
		t.Fatal("Release must not change the receiver")
	}
}

func TestScreeningHold_PendingIsReleasableBlockedIsNot(t *testing.T) {
	if _, _, err := openHold(ScreenPending).Release("u", "provider back, cleared by hand", time.Now()); err != nil {
		t.Fatalf("SCREENING_PENDING release = %v", err)
	}
	_, _, err := openHold(ScreenPotentialMatch, ScreenBlocked).Release("u", "looks fine", time.Now())
	if code := holdCode(t, err); code != CodeScreeningHold {
		t.Fatalf("BLOCKED release code = %s, want %s", code, CodeScreeningHold)
	}
}

func TestScreeningHold_SecondRelease(t *testing.T) {
	at := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	released, _, err := openHold(ScreenPotentialMatch).Release("u1", "cleared", at)
	if err != nil {
		t.Fatal(err)
	}
	again, changed, err := released.Release("u1", " cleared ", at.Add(time.Hour))
	if err != nil || changed || again.Version != released.Version || !again.ReleasedAt.Equal(at) {
		t.Fatalf("same release again = %+v, changed %v, err %v; want it unchanged", again, changed, err)
	}
	if _, _, err := released.Release("u2", "cleared", at); holdCode(t, err) != CodeHoldState {
		t.Fatalf("another user's release = %v, want %s", err, CodeHoldState)
	}
	if _, _, err := released.Release("u1", "other reason", at); holdCode(t, err) != CodeHoldState {
		t.Fatalf("another reason = %v, want %s", err, CodeHoldState)
	}
}

func TestValidateHoldRelease(t *testing.T) {
	cases := []struct {
		name   string
		reason string
		bad    bool
	}{
		{"empty", "", true},
		{"blank", "   ", true},
		{"ok", "reviewed against the passport copy", false},
		{"at limit", strings.Repeat("x", MaxReleaseReasonLength), false},
		{"over limit", strings.Repeat("x", MaxReleaseReasonLength+1), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if errs := ValidateHoldRelease(tc.reason); (len(errs) > 0) != tc.bad {
				t.Fatalf("errors = %v, bad = %v", errs, tc.bad)
			}
		})
	}
}
