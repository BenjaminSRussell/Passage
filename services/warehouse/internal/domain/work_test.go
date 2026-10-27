package domain

import (
	"slices"
	"testing"
)

// TestWorkTransitions_Table covers BR-31.3: every legal edge is accepted and
// every other edge between the seven statuses is refused with WMS-0110.
func TestWorkTransitions_Table(t *testing.T) {
	all := []WorkStatus{WorkOpen, WorkAssigned, WorkInProgress, WorkCompleted,
		WorkPartiallyComplete, WorkCancelled, WorkBlockedNoLocation}
	legalEdges := [][2]WorkStatus{
		{WorkOpen, WorkAssigned},
		{WorkOpen, WorkCancelled},
		{WorkAssigned, WorkInProgress},
		{WorkAssigned, WorkOpen},
		{WorkAssigned, WorkCancelled},
		{WorkInProgress, WorkCompleted},
		{WorkInProgress, WorkPartiallyComplete},
		{WorkBlockedNoLocation, WorkOpen},
		{WorkBlockedNoLocation, WorkCancelled},
	}
	for _, from := range all {
		for _, to := range all {
			err := CheckTransition(from, to, 0)
			want := slices.Contains(legalEdges, [2]WorkStatus{from, to})
			if want && err != nil {
				t.Errorf("%s -> %s: unexpected error %v", from, to, err)
			}
			if !want && err == nil {
				t.Errorf("%s -> %s: illegal transition accepted", from, to)
			}
		}
	}
}

// TestWorkTransitions_CancelAfterConfirmationRefused checks that a work with a
// confirmed line cannot be cancelled, while the other legal edges still pass.
func TestWorkTransitions_CancelAfterConfirmationRefused(t *testing.T) {
	cases := []struct {
		name      string
		from, to  WorkStatus
		confirmed int
		wantErr   bool
	}{
		{"open cancel untouched", WorkOpen, WorkCancelled, 0, false},
		{"assigned cancel after one confirm", WorkAssigned, WorkCancelled, 1, true},
		{"open assign after confirm allowed", WorkOpen, WorkAssigned, 3, false},
		{"in progress complete after confirms allowed", WorkInProgress, WorkCompleted, 4, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := CheckTransition(c.from, c.to, c.confirmed)
			if (err != nil) != c.wantErr {
				t.Fatalf("CheckTransition(%s,%s,%d) err = %v, wantErr %v", c.from, c.to, c.confirmed, err, c.wantErr)
			}
			if err != nil {
				de, ok := err.(*Error)
				if !ok || de.Code != CodeIllegalTransition {
					t.Fatalf("want *Error with code %s, got %#v", CodeIllegalTransition, err)
				}
			}
		})
	}
}
