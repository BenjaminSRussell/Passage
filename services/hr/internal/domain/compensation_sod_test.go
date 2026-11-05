package domain

import (
	"errors"
	"testing"
)

func TestCheckCycleApprover(t *testing.T) {
	cases := []struct {
		name string
		in   CycleApproval
		ok   bool
	}{
		{"independent approver", CycleApproval{Approver: "u-3", Creator: "u-1", Submitters: []string{"u-2"}}, true},
		{"legacy cycle without a recorded creator", CycleApproval{Approver: "u-3"}, true},
		{"anonymous", CycleApproval{Approver: "", Creator: "u-1"}, false},
		{"creator", CycleApproval{Approver: "u-1", Creator: "u-1", Submitters: []string{"u-2"}}, false},
		{"submitter", CycleApproval{Approver: "u-2", Creator: "u-1", Submitters: []string{"u-4", "u-2"}}, false},
		{"worker or manager on the cycle", CycleApproval{Approver: "u-3", Creator: "u-1", OnCycle: true}, false},
	}
	for _, c := range cases {
		err := CheckCycleApprover(c.in)
		if c.ok != (err == nil) {
			t.Errorf("%s: err = %v, want ok %v", c.name, err, c.ok)
		}
		var de *Error
		if err != nil && (!errors.As(err, &de) || de.Code != CodeCompSoD) {
			t.Errorf("%s: err = %v, want %s", c.name, err, CodeCompSoD)
		}
	}
}
