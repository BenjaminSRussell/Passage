package domain

import (
	"errors"
	"testing"

	"pgregory.net/rapid"
)

func TestTransitionCheck_Table(t *testing.T) {
	cases := []struct {
		name     string
		from, to CheckStatus
		code     string // empty when allowed
	}{
		{name: "print a created check", from: CheckCreated, to: CheckPrinted},
		{name: "send a printed check", from: CheckPrinted, to: CheckSent},
		{name: "clear a sent check", from: CheckSent, to: CheckCleared},
		{name: "void a printed check", from: CheckPrinted, to: CheckVoided},
		{name: "void a sent check before clearing", from: CheckSent, to: CheckVoided},
		{name: "stop a sent check", from: CheckSent, to: CheckStopped},
		{name: "void after cleared is refused", from: CheckCleared, to: CheckVoided, code: CodeCheckCleared},
		{name: "print a voided check is refused", from: CheckVoided, to: CheckPrinted, code: CodeCheckState},
		{name: "clear a created check is refused", from: CheckCreated, to: CheckCleared, code: CodeCheckState},
		{name: "stop a created check is refused", from: CheckCreated, to: CheckStopped, code: CodeCheckState},
		{name: "stop a printed check still in hand is refused", from: CheckPrinted, to: CheckStopped, code: CodeCheckState},
		{name: "void a stopped check", from: CheckStopped, to: CheckVoided},
		{name: "clear a stopped check is refused", from: CheckStopped, to: CheckCleared, code: CodeCheckState},
		{name: "clear a cleared check again is refused", from: CheckCleared, to: CheckCleared, code: CodeCheckState},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := TransitionCheck(tc.from, tc.to)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			var de *Error
			if !errors.As(err, &de) || de.Code != tc.code {
				t.Fatalf("error = %v, want code %s", err, tc.code)
			}
		})
	}
}

func TestNeedsPositivePayVoid(t *testing.T) {
	for _, s := range AllCheckStatuses {
		want := s == CheckSent || s == CheckStopped
		if got := NeedsPositivePayVoid(s); got != want {
			t.Fatalf("NeedsPositivePayVoid(%s) = %v, want %v", s, got, want)
		}
	}
}

// Property: a voided check never leaves its state through a successful
// transition, and a cleared check is never voided (BR-36.5).
func TestCheckLifecycle_TerminalStatesHold(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		state := CheckCreated
		for i := 0; i < 20; i++ {
			to := rapid.SampledFrom(AllCheckStatuses).Draw(t, "to")
			if err := TransitionCheck(state, to); err == nil {
				if state == CheckVoided {
					t.Fatalf("left voided state to %s", to)
				}
				if state == CheckCleared && to == CheckVoided {
					t.Fatalf("voided a cleared check")
				}
				state = to
			}
		}
	})
}

func TestValidVoidReason(t *testing.T) {
	cases := map[string]bool{"": false, "   ": false, "lost in post": true, "x": true}
	for in, want := range cases {
		if got := ValidVoidReason(in); got != want {
			t.Fatalf("ValidVoidReason(%q) = %v, want %v", in, got, want)
		}
	}
}
