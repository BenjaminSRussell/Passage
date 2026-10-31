package domain

import (
	"errors"
	"testing"
)

func TestDefaultPolicy(t *testing.T) {
	p := DefaultPolicy("le")
	if !p.ChangeManagementDefault || p.POFourEyes || p.AllowInvoiceBeforeReceipt || p.Version != 0 {
		t.Fatalf("unexpected defaults %+v", p)
	}
}

func TestCheckFourEyes(t *testing.T) {
	on := Policy{POFourEyes: true}
	for _, tc := range []struct {
		name     string
		p        Policy
		actor    string
		involved []string
		wantErr  bool
	}{
		{"off allows the creator", Policy{}, "u1", []string{"u1", "u1", "u1"}, false},
		{"off allows an unnamed caller", Policy{}, "", []string{"u1", "", ""}, false},
		{"on blocks the creator", on, "u1", []string{"u1", "", ""}, true},
		{"on blocks the submitter", on, "u2", []string{"u1", "u2", ""}, true},
		{"on blocks the last editor", on, "u3", []string{"u1", "u2", "u3"}, true},
		{"on allows another user", on, "u4", []string{"u1", "u2", "u3"}, false},
		{"unrecorded users block no one", on, "u4", []string{"", "", ""}, false},
		{"on refuses an unnamed caller", on, "", []string{"", "", ""}, true},
	} {
		err := CheckFourEyes(tc.p, tc.actor, tc.involved...)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v", tc.name, err)
		}
		var de *Error
		if err != nil && (!errors.As(err, &de) || de.Code != CodeFourEyes) {
			t.Errorf("%s: wrong code %v", tc.name, err)
		}
	}
}
