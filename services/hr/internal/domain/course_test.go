package domain

import (
	"errors"
	"testing"

	"pgregory.net/rapid"
)

func TestSeat(t *testing.T) {
	tests := []struct {
		name                                  string
		confirmed, waitlisted, capacity, wcap int
		want                                  string
		wantOK                                bool
	}{
		{"first seat is confirmed", 0, 0, 10, 2, RegConfirmed, true},
		{"last free seat is confirmed", 9, 0, 10, 2, RegConfirmed, true},
		{"full session goes to the waitlist", 10, 0, 10, 2, RegWaitlisted, true},
		{"last waitlist place", 10, 1, 10, 2, RegWaitlisted, true},
		{"waitlist full is refused", 10, 2, 10, 2, "", false},
		{"no waitlist at all is refused when full", 3, 0, 3, 0, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Seat(tc.confirmed, tc.waitlisted, tc.capacity, tc.wcap)
			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("Seat = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestSeatingFollowsRegistrationOrder is AC-42.9 as a property: n workers
// registering one after another into capacity c and waitlist w get
// min(n, c) CONFIRMED, then min(n-c, w) WAITLISTED, and every later one is
// refused; the counts never exceed capacity or waitlist.
func TestSeatingFollowsRegistrationOrder(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		capacity := rapid.IntRange(1, 40).Draw(t, "capacity")
		wcap := rapid.IntRange(0, MaxWaitlistCapacity).Draw(t, "waitlistCapacity")
		n := rapid.IntRange(0, 200).Draw(t, "registrations")
		confirmed, waitlisted, refused := 0, 0, 0
		for i := 0; i < n; i++ {
			status, ok := Seat(confirmed, waitlisted, capacity, wcap)
			switch {
			case !ok:
				refused++
			case status == RegConfirmed:
				if waitlisted > 0 {
					t.Fatalf("registration %d confirmed while %d are waitlisted", i, waitlisted)
				}
				confirmed++
			case status == RegWaitlisted:
				waitlisted++
			}
		}
		if confirmed != min(n, capacity) || waitlisted != min(max(n-capacity, 0), wcap) || refused != n-confirmed-waitlisted {
			t.Fatalf("n=%d c=%d w=%d: confirmed=%d waitlisted=%d refused=%d", n, capacity, wcap, confirmed, waitlisted, refused)
		}
	})
}

func TestValidateOutcome(t *testing.T) {
	tests := []struct {
		status, result string
		score          bool
		wantErr        bool
	}{
		{RegAttended, ResultPass, false, false},
		{RegAttended, ResultFail, true, false},
		{RegAttended, "", false, false},
		{RegNoShow, "", false, false},
		{RegNoShow, ResultPass, false, true},
		{RegNoShow, "", true, true},
		{RegAttended, "", true, true},
		{RegAttended, "MAYBE", false, true},
		{RegConfirmed, "", false, true},
		{RegCanceled, "", false, true},
	}
	for _, tc := range tests {
		err := ValidateOutcome(tc.status, tc.result, tc.score)
		var de *Error
		if tc.wantErr != (err != nil) || (err != nil && (!errors.As(err, &de) || de.Code != CodeCourseOutcome)) {
			t.Errorf("ValidateOutcome(%s, %q, score=%v) = %v, want error %v", tc.status, tc.result, tc.score, err, tc.wantErr)
		}
	}
}

func TestCompletionStep(t *testing.T) {
	confirmed := Recorded{Status: RegConfirmed}
	attended := Recorded{Status: RegAttended}
	passed := Recorded{Status: RegAttended, Result: ResultPass, Score: "92.50"}
	noShow := Recorded{Status: RegNoShow}
	tests := []struct {
		name         string
		stored, next Recorded
		noop, err    bool
	}{
		{"confirmed takes a pass", confirmed, passed, false, false},
		{"confirmed takes a no-show", confirmed, noShow, false, false},
		{"same pass again is a no-op", passed, passed, true, false},
		{"same no-show again is a no-op", noShow, noShow, true, false},
		{"attendance first, result later", attended, passed, false, false},
		{"a pass cannot become a fail", passed, Recorded{Status: RegAttended, Result: ResultFail}, false, true},
		{"a pass cannot lose its result", passed, attended, false, true},
		{"a no-show cannot become attended", noShow, passed, false, true},
		{"attended cannot become a no-show", attended, noShow, false, true},
		{"waitlisted cannot be completed", Recorded{Status: RegWaitlisted}, passed, false, true},
		{"requested cannot be completed", Recorded{Status: RegRequested}, attended, false, true},
		{"canceled cannot be completed", Recorded{Status: RegCanceled}, noShow, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			noop, err := CompletionStep(tc.stored, tc.next)
			if noop != tc.noop || (err != nil) != tc.err {
				t.Fatalf("CompletionStep = (%v, %v), want noop %v err %v", noop, err, tc.noop, tc.err)
			}
			var de *Error
			if err != nil && (!errors.As(err, &de) || de.Code != CodeCourseState) {
				t.Fatalf("error code = %v, want %s", err, CodeCourseState)
			}
		})
	}
}

func TestCheckCancel(t *testing.T) {
	for status, want := range map[string]struct{ noop, err bool }{
		RegRequested: {false, false}, RegConfirmed: {false, false}, RegWaitlisted: {false, false},
		RegCanceled: {true, false}, RegAttended: {false, true}, RegNoShow: {false, true},
	} {
		noop, err := CheckCancel(status)
		if noop != want.noop || (err != nil) != want.err {
			t.Errorf("CheckCancel(%s) = (%v, %v), want %+v", status, noop, err, want)
		}
	}
}

// TestAwardRatingNeverLowers is BR-42.9: rating = max(existing, course rating).
func TestAwardRatingNeverLowers(t *testing.T) {
	tests := []struct{ existing, awarded, want int }{
		{0, 3, 3},
		{2, 3, 3},
		{4, 3, 4},
		{3, 3, 3},
	}
	for _, tc := range tests {
		if got := AwardRating(tc.existing, tc.awarded); got != tc.want {
			t.Errorf("AwardRating(%d, %d) = %d, want %d", tc.existing, tc.awarded, got, tc.want)
		}
	}
}

func TestCheckNotSelfApproval(t *testing.T) {
	me := "user-1"
	other := "user-2"
	if err := CheckNotSelfApproval(me, &me); err == nil {
		t.Error("approving one's own registration was allowed")
	}
	if err := CheckNotSelfApproval(me, &other); err != nil {
		t.Errorf("approving another worker's registration: %v", err)
	}
	if err := CheckNotSelfApproval(me, nil); err != nil {
		t.Errorf("a worker without a sign-in user: %v", err)
	}
}
