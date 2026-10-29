package domain

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// hrStages is the BR-45.9 example: INTAKE -> INVESTIGATION -> RESOLUTION, with an
// "Interview" activity required to leave INVESTIGATION.
var hrStages = []Stage{
	{Code: "INTAKE", Sequence: 1},
	{Code: "INVESTIGATION", Sequence: 2, RequiredActivities: []string{"Interview"}},
	{Code: "RESOLUTION", Sequence: 3},
}

func codeOf(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func TestCheckStage(t *testing.T) {
	tests := []struct {
		name   string
		from   string
		to     string
		done   map[string]bool
		reason string
		want   string // expected problem code, empty for allowed
	}{
		{"forward with required activity open is rejected", "INVESTIGATION", "RESOLUTION", map[string]bool{}, "", CodeStageGate},
		{"forward after activity DONE is allowed", "INVESTIGATION", "RESOLUTION", map[string]bool{"Interview": true}, "", ""},
		{"forward from stage without requirements", "INTAKE", "INVESTIGATION", nil, "", ""},
		{"skipping a stage is invalid", "INTAKE", "RESOLUTION", map[string]bool{"Interview": true}, "", CodeStageInvalid},
		{"back one stage needs a reason", "RESOLUTION", "INVESTIGATION", nil, "", CodeStageInvalid},
		{"back one stage with reason is allowed", "RESOLUTION", "INVESTIGATION", nil, "new evidence", ""},
		{"same stage is invalid", "INTAKE", "INTAKE", nil, "", CodeStageInvalid},
		{"unknown target is invalid", "INTAKE", "CLOSEOUT", nil, "", CodeStageInvalid},
		{"unknown source is invalid", "NOPE", "INTAKE", nil, "", CodeStageInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckStage(hrStages, tc.from, tc.to, tc.done, tc.reason)
			if got := codeOf(err); got != tc.want {
				t.Fatalf("code %q, want %q (err %v)", got, tc.want, err)
			}
		})
	}
}

func TestCheckStage_GateNamesMissingActivities(t *testing.T) {
	stages := []Stage{{Code: "A", Sequence: 1, RequiredActivities: []string{"Interview", "Consent"}}, {Code: "B", Sequence: 2}}
	err := CheckStage(stages, "A", "B", map[string]bool{"Consent": true}, "")
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeStageGate {
		t.Fatalf("want %s, got %v", CodeStageGate, err)
	}
	if want := "required activities not DONE: Interview"; !strings.Contains(de.Detail, want) {
		t.Fatalf("detail %q lacks %q", de.Detail, want)
	}
}

// Property: a case can only move to an adjacent stage. Any other target is rejected
// with CASE-STG-002, whatever the activities say.
func TestCheckStage_OnlyAdjacentMovesAllowed(t *testing.T) {
	stages := []Stage{{Code: "S1", Sequence: 1}, {Code: "S2", Sequence: 2}, {Code: "S3", Sequence: 3}, {Code: "S4", Sequence: 4}}
	rapid.Check(t, func(t *rapid.T) {
		from := rapid.IntRange(0, 3).Draw(t, "from")
		to := rapid.IntRange(0, 3).Draw(t, "to")
		reason := rapid.SampledFrom([]string{"", "reason"}).Draw(t, "reason")
		err := CheckStage(stages, stages[from].Code, stages[to].Code, map[string]bool{}, reason)
		switch d := to - from; {
		case d == 0:
			if codeOf(err) != CodeStageInvalid {
				t.Fatalf("same stage: %v", err)
			}
		case d == 1:
			if codeOf(err) != "" && codeOf(err) != CodeStageGate {
				t.Fatalf("forward: %v", err)
			}
		case d == -1:
			if reason == "" && codeOf(err) != CodeStageInvalid {
				t.Fatalf("back without reason: %v", err)
			}
			if reason != "" && err != nil {
				t.Fatalf("back with reason: %v", err)
			}
		default:
			if codeOf(err) != CodeStageInvalid {
				t.Fatalf("jump %d: %v", d, err)
			}
		}
	})
}

func TestProcessProblems(t *testing.T) {
	tests := []struct {
		name   string
		stages []Stage
		ok     bool
	}{
		{"valid three stages", hrStages, true},
		{"empty", nil, false},
		{"sequence gap", []Stage{{Code: "A", Sequence: 1}, {Code: "B", Sequence: 3}}, false},
		{"duplicate code", []Stage{{Code: "A", Sequence: 1}, {Code: "A", Sequence: 2}}, false},
		{"bad code", []Stage{{Code: "lower", Sequence: 1}}, false},
		{"blank required activity", []Stage{{Code: "AA", Sequence: 1, RequiredActivities: []string{"  "}}}, false},
		{"too many stages", manyStages(21), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(ProcessProblems(tc.stages)) == 0; got != tc.ok {
				t.Fatalf("ok=%v, problems %v", got, ProcessProblems(tc.stages))
			}
		})
	}
}

func manyStages(n int) []Stage {
	out := make([]Stage, n)
	for i := range out {
		out[i] = Stage{Code: fmt.Sprintf("S%02d", i+1), Sequence: i + 1}
	}
	return out
}

func TestCheckTransition(t *testing.T) {
	codes := []string{"FIXED", "DUPLICATE"}
	tests := []struct {
		name   string
		status string
		to     string
		code   string
		codes  []string
		want   string
	}{
		{"open to resolved with code", GenericOpen, GenericResolved, "FIXED", codes, ""},
		{"open to resolved without code", GenericOpen, GenericResolved, "", codes, CodeResolutionRequired},
		{"resolved code outside category list", GenericOpen, GenericResolved, "OTHER", codes, CodeResolutionUnknown},
		{"any code when category lists none", GenericOpen, GenericResolved, "OTHER", nil, ""},
		{"open to canceled needs no code", GenericOpen, GenericCanceled, "", codes, ""},
		{"resolved to closed", GenericResolved, GenericClosed, "", codes, ""},
		{"closed is final", GenericClosed, GenericOpen, "", codes, CodeCaseState},
		{"canceled is final", GenericCanceled, GenericResolved, "FIXED", codes, CodeCaseState},
		{"open straight to closed is invalid", GenericOpen, GenericClosed, "", codes, CodeCaseState},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := codeOf(CheckTransition(tc.status, tc.to, tc.code, tc.codes)); got != tc.want {
				t.Fatalf("code %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRequiresCustomerAndRouting(t *testing.T) {
	tests := []struct {
		category string
		customer bool
		routed   bool
	}{
		{CategoryService, true, true},
		{CategoryCustomerService, true, true},
		{CategoryCollections, true, false},
		{CategoryHR, false, false},
		{CategoryProductChange, false, false},
		{CategoryInternal, false, false},
	}
	for _, tc := range tests {
		if RequiresCustomer(tc.category) != tc.customer || ServiceRouted(tc.category) != tc.routed {
			t.Errorf("%s: customer=%v routed=%v", tc.category, RequiresCustomer(tc.category), ServiceRouted(tc.category))
		}
	}
}

func TestCanAccess(t *testing.T) {
	tests := []struct {
		name   string
		duty   string
		holds  bool
		owner  bool
		allows bool
	}{
		{"open category anyone", "", false, false, true},
		{"restricted by duty holder", "CaseHR", true, false, true},
		{"restricted owner without duty", "CaseHR", false, true, true},
		{"restricted outsider", "CaseHR", false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanAccess(tc.duty, tc.holds, tc.owner); got != tc.allows {
				t.Fatalf("got %v, want %v", got, tc.allows)
			}
		})
	}
}

// BR-45.9 reopen: within the category window the case itself reopens; after it,
// or with a window of 0, a new case is linked by reopened_from.
func TestReopenInPlace(t *testing.T) {
	resolved := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		status  string
		at      *time.Time
		now     time.Time
		window  int
		inPlace bool
		code    string
	}{
		{"resolved inside the window", GenericResolved, &resolved, resolved.AddDate(0, 0, 13), 14, true, ""},
		{"closed on the last day of the window", GenericClosed, &resolved, resolved.AddDate(0, 0, 14), 14, true, ""},
		{"closed after the window", GenericClosed, &resolved, resolved.AddDate(0, 0, 14).Add(time.Second), 14, false, ""},
		{"window 0 always links a new case", GenericResolved, &resolved, resolved.Add(time.Minute), 0, false, ""},
		{"open case cannot be reopened", GenericOpen, nil, resolved, 14, false, CodeCaseState},
		{"canceled case cannot be reopened", GenericCanceled, nil, resolved, 14, false, CodeCaseState},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReopenInPlace(tc.status, tc.at, tc.now, tc.window)
			if codeOf(err) != tc.code || got != tc.inPlace {
				t.Fatalf("inPlace=%v err=%v, want %v %q", got, err, tc.inPlace, tc.code)
			}
		})
	}
}
