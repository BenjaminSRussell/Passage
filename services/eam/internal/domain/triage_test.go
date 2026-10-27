package domain

import "testing"

func TestTriageTarget_FR44_6(t *testing.T) {
	tests := []struct {
		from, action, want string
		err                bool
	}{
		{"NEW", TriageTriage, "TRIAGED", false},
		{"DEFERRED", TriageTriage, "TRIAGED", false},
		{"NEW", TriageReject, "REJECTED", false},
		{"DEFERRED", TriageReject, "REJECTED", false},
		{"REJECTED", TriageReject, "REJECTED", false},
		{"NEW", TriageDefer, "DEFERRED", false},
		{"TRIAGED", TriageDefer, "DEFERRED", false},
		{"REJECTED", TriageTriage, "", true},
		{"CONVERTED", TriageDefer, "", true},
		{"CONVERTED", TriageReject, "", true},
		{"NEW", "MERGE", "", true},
	}
	for _, tc := range tests {
		got, err := TriageTarget(tc.from, tc.action)
		if (err != nil) != tc.err {
			t.Fatalf("%s %s: err = %v, want error %v", tc.from, tc.action, err, tc.err)
		}
		if got != tc.want {
			t.Fatalf("%s %s: got %q, want %q", tc.from, tc.action, got, tc.want)
		}
	}
}
