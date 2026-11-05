package domain

import (
	"errors"
	"testing"
)

func TestNextLoadStatus_ReleaseTenderAndOutcomes(t *testing.T) {
	cases := []struct {
		from, move, want string
	}{
		{LoadPlanned, MoveRelease, LoadTendered},
		{LoadRated, MoveRelease, LoadTendered},
		{LoadPlanned, MoveTender, LoadTendered},
		{LoadRated, MoveTender, LoadTendered},
		// The workflow starts the tender of a released load.
		{LoadTendered, MoveTender, LoadTendered},
		{LoadTendered, MoveTenderAccepted, LoadAccepted},
		// A failed tender puts the load back where it can be released again.
		{LoadTendered, MoveTenderFailed, LoadPlanned},
	}
	for _, c := range cases {
		got, err := NextLoadStatus(c.from, c.move)
		if err != nil || got != c.want {
			t.Errorf("%s on %s = %q, %v; want %q", c.move, c.from, got, err, c.want)
		}
	}
}

func TestNextLoadStatus_RefusesOtherStatuses(t *testing.T) {
	allowed := map[string][]string{
		MoveRelease:        {LoadPlanned, LoadRated},
		MoveTender:         {LoadPlanned, LoadRated, LoadTendered},
		MoveTenderAccepted: {LoadTendered},
		MoveTenderFailed:   {LoadTendered},
	}
	for move, ok := range allowed {
		for _, from := range LoadStatuses {
			if contains(ok, from) {
				continue
			}
			_, err := NextLoadStatus(from, move)
			var de *Error
			if !errors.As(err, &de) || de.Code != CodeLoadState {
				t.Errorf("%s on %s = %v, want %s", move, from, err, CodeLoadState)
			}
		}
	}
	if _, err := NextLoadStatus(LoadPlanned, "dispatch"); err == nil {
		t.Fatal("an unknown move must be refused")
	}
}

func TestLoadAfterTender_FollowsTheOutcomeOnlyFromTendered(t *testing.T) {
	cases := []struct {
		load, tender, want string
		moved              bool
	}{
		{LoadTendered, TenderPending, LoadTendered, false},
		{LoadTendered, TenderAccepted, LoadAccepted, true},
		{LoadTendered, TenderFailed, LoadPlanned, true},
		// A load that is not TENDERED is left as it is, and the outcome stands.
		{LoadPlanned, TenderAccepted, LoadPlanned, false},
		{LoadAccepted, TenderAccepted, LoadAccepted, false},
		{LoadCancelled, TenderFailed, LoadCancelled, false},
	}
	for _, c := range cases {
		got, moved := LoadAfterTender(c.load, c.tender)
		if got != c.want || moved != c.moved {
			t.Errorf("load %s, tender %s = %q, %v; want %q, %v", c.load, c.tender, got, moved, c.want, c.moved)
		}
	}
}
