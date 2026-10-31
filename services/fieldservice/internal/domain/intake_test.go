package domain

import "testing"

func TestCaseToken_NormalizesToStoredNumber(t *testing.T) {
	cases := []struct {
		subject string
		want    string
		ok      bool
	}{
		{"Re: [CS-1042] pump noise", "CS-001042", true},
		{"[CS-001042] pump noise", "CS-001042", true},
		{"fwd: [cs-7] lower case", "CS-000007", true},
		{"[CS-1042] then [CS-9]", "CS-001042", true},
		{"pump noise CS-1042", "", false},
		{"[CS-] empty", "", false},
		{"[CS-0] zero", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		got, ok := CaseToken(tc.subject)
		if got != tc.want || ok != tc.ok {
			t.Errorf("CaseToken(%q) = %q, %v; want %q, %v", tc.subject, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAutomated_AutoReplyHeaders(t *testing.T) {
	cases := []struct {
		auto  string
		xAuto bool
		want  bool
	}{
		{"", false, false},
		{"no", false, false},
		{" No ", false, false},
		{"auto-replied", false, true},
		{"auto-generated", false, true},
		{"", true, true},
	}
	for _, tc := range cases {
		if got := Automated(tc.auto, tc.xAuto); got != tc.want {
			t.Errorf("Automated(%q, %v) = %v, want %v", tc.auto, tc.xAuto, got, tc.want)
		}
	}
}

// BR-45.13 and AC-45.14.
func TestThread_BR45_13(t *testing.T) {
	open := &ThreadCase{ID: "a", Number: "CS-001042", Status: GenericOpen}
	resolved := &ThreadCase{ID: "r", Number: "CS-001045", Status: GenericResolved}
	closed := &ThreadCase{ID: "c", Number: "CS-001043", Status: GenericClosed}
	canceled := &ThreadCase{ID: "x", Number: "CS-001044", Status: GenericCanceled}
	cases := []struct {
		name         string
		token, reply *ThreadCase
		automated    bool
		route        string
		target       *ThreadCase
		acknowledge  bool
	}{
		{"AC-45.14 token of an open case attaches without acknowledgment", open, nil, false, RouteAttached, open, false},
		{"reply chain of an open case attaches", nil, open, false, RouteAttached, open, false},
		{"a resolved case is not closed and takes the message", resolved, nil, false, RouteAttached, resolved, false},
		{"token of a closed case opens a linked case", closed, nil, false, RouteNewCase, closed, true},
		{"reply to a canceled case opens a linked case", nil, canceled, false, RouteNewCase, canceled, true},
		{"token wins over the reply chain", closed, open, false, RouteNewCase, closed, true},
		{"AC-45.14 no token from an unknown sender opens a case", nil, nil, false, RouteNewCase, nil, true},
		{"auto-replies open a case without acknowledgment", nil, nil, true, RouteNewCase, nil, false},
		{"auto-replies to an open case attach", open, nil, true, RouteAttached, open, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Thread(tc.token, tc.reply, tc.automated)
			if got.Route != tc.route || got.Case != tc.target || got.Acknowledge != tc.acknowledge {
				t.Fatalf("Thread = %+v, want route %s case %v ack %v", got, tc.route, tc.target, tc.acknowledge)
			}
		})
	}
}
