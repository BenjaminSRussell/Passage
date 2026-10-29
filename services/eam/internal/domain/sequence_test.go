package domain

import (
	"errors"
	"reflect"
	"testing"

	"pgregory.net/rapid"
)

// abcSteps is the BR-44.10 example: A every occurrence, B every 4th (includes A),
// C every 12th (includes A and B).
func abcSteps() []SequenceStep {
	return []SequenceStep{
		{Code: "A", JobType: "LUBE", Multiple: 1, Hierarchy: 1, Includes: []string{}},
		{Code: "B", JobType: "INSPECT", Multiple: 4, Hierarchy: 2, Includes: []string{"A"}},
		{Code: "C", JobType: "OVERHAUL", Multiple: 12, Hierarchy: 3, Includes: []string{"A", "B"}},
	}
}

// TestSelectSteps_AC44_10 covers AC-44.10: occurrences 1-12 yield the job sequence
// A, A, A, B, A, A, A, B, A, A, A, C.
func TestSelectSteps_AC44_10(t *testing.T) {
	want := []string{"A", "A", "A", "B", "A", "A", "A", "B", "A", "A", "A", "C"}
	var got []string
	for k := 1; k <= 12; k++ {
		occ := SelectSteps(abcSteps(), k)
		if len(occ.Steps) != 1 {
			t.Fatalf("occurrence %d: steps %v, want exactly one", k, occ.Steps)
		}
		got = append(got, occ.Steps[0])
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestSelectSteps_Table covers selection edge cases: no step due, an extra step
// that is not included by the selected one, and invalid occurrence numbers.
func TestSelectSteps_Table(t *testing.T) {
	steps := []SequenceStep{
		{Code: "S", JobType: "SAFETY", Multiple: 2, Hierarchy: 1, Includes: []string{}},
		{Code: "T", JobType: "TEST", Multiple: 3, Hierarchy: 2, Includes: []string{}},
	}
	cases := []struct {
		name string
		k    int
		want []string
	}{
		{"nothing due", 1, []string{}},
		{"S only", 2, []string{"S"}},
		{"T only", 3, []string{"T"}},
		{"both due, T on top, S not included, both kept", 6, []string{"T", "S"}},
		{"zero is not an occurrence", 0, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SelectSteps(steps, tc.k).Steps
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("k=%d got %v, want %v", tc.k, got, tc.want)
			}
		})
	}
}

// TestValidateSequenceSteps covers the structural rules.
func TestValidateSequenceSteps(t *testing.T) {
	bad := func(mut func([]SequenceStep) []SequenceStep) []SequenceStep {
		return mut(abcSteps())
	}
	cases := []struct {
		name  string
		steps []SequenceStep
		ok    bool
	}{
		{"valid", abcSteps(), true},
		{"empty", []SequenceStep{}, false},
		{"zero multiple", bad(func(s []SequenceStep) []SequenceStep { s[0].Multiple = 0; return s }), false},
		{"duplicate code", bad(func(s []SequenceStep) []SequenceStep { s[1].Code = "A"; return s }), false},
		{"shared hierarchy", bad(func(s []SequenceStep) []SequenceStep { s[1].Hierarchy = 3; return s }), false},
		{"unknown include", bad(func(s []SequenceStep) []SequenceStep { s[1].Includes = []string{"Z"}; return s }), false},
		{"include higher step", bad(func(s []SequenceStep) []SequenceStep { s[0].Includes = []string{"C"}; return s }), false},
		{"self include", bad(func(s []SequenceStep) []SequenceStep { s[0].Includes = []string{"A"}; return s }), false},
		{"bad code", bad(func(s []SequenceStep) []SequenceStep { s[0].Code = "a b"; return s }), false},
		{"lowercase job type", bad(func(s []SequenceStep) []SequenceStep { s[0].JobType = "lube"; return s }), false},
		{"zero hierarchy", bad(func(s []SequenceStep) []SequenceStep { s[0].Hierarchy = 0; return s }), false},
		{"multiple above 1000", bad(func(s []SequenceStep) []SequenceStep { s[2].Multiple = 1001; return s }), false},
		{"include twice", bad(func(s []SequenceStep) []SequenceStep { s[2].Includes = []string{"A", "A"}; return s }), false},
		{"nil includes are fine", bad(func(s []SequenceStep) []SequenceStep { s[0].Includes = nil; return s }), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSequenceSteps(tc.steps)
			if tc.ok && err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if !tc.ok {
				var ge GateError
				if !errors.As(err, &ge) || ge.Code != CodeSequenceStep {
					t.Fatalf("err = %v, want %s", err, CodeSequenceStep)
				}
			}
		})
	}
}

// TestSelectSteps_Property checks, for any valid chain of steps and any occurrence
// number, that the selected step is due, that no job is repeated, and that every
// due step is either performed or suppressed by the selected step (BR-44.10).
func TestSelectSteps_Property(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		n := rapid.IntRange(1, 5).Draw(rt, "n")
		steps := make([]SequenceStep, 0, n)
		for i := 0; i < n; i++ {
			s := SequenceStep{
				Code:      string(rune('A' + i)),
				JobType:   "JOB" + string(rune('A'+i)),
				Multiple:  rapid.IntRange(1, 12).Draw(rt, "multiple"),
				Hierarchy: i + 1,
				Includes:  []string{},
			}
			for j := 0; j < i; j++ {
				if rapid.Bool().Draw(rt, "include") {
					s.Includes = append(s.Includes, steps[j].Code)
				}
			}
			steps = append(steps, s)
		}
		if err := ValidateSequenceSteps(steps); err != nil {
			rt.Fatalf("generated chain invalid: %v", err)
		}
		k := rapid.IntRange(1, 200).Draw(rt, "k")
		occ := SelectSteps(steps, k)

		seen := map[string]bool{}
		for _, c := range occ.Steps {
			if seen[c] {
				rt.Fatalf("step %s repeated in occurrence %d", c, k)
			}
			seen[c] = true
		}
		if len(occ.Steps) > 0 {
			top := occ.Steps[0]
			var topStep SequenceStep
			for _, s := range steps {
				if s.Code == top {
					topStep = s
				}
			}
			if k%topStep.Multiple != 0 {
				rt.Fatalf("top step %s not due at %d", top, k)
			}
			for _, s := range steps {
				due := k%s.Multiple == 0
				if due && !seen[s.Code] {
					covered := false
					for _, inc := range topStep.Includes {
						covered = covered || inc == s.Code
					}
					if !covered {
						rt.Fatalf("due step %s neither performed nor suppressed", s.Code)
					}
				}
			}
		}
	})
}

// TestSimulateSteps covers the editor simulation: it continues after the last
// generated occurrence (k = last + 1, BR-44.10) and changes nothing.
func TestSimulateSteps(t *testing.T) {
	steps := abcSteps()
	got := SimulateSteps(steps, 10, 3)
	if len(got) != 3 || got[0].Number != 11 || got[1].Steps[0] != "C" || got[2].Number != 13 || got[2].Steps[0] != "A" {
		t.Fatalf("simulation after 10 = %+v", got)
	}
	if !reflect.DeepEqual(got[1].Jobs, []string{"OVERHAUL"}) {
		t.Fatalf("occurrence 12 jobs = %v, want only OVERHAUL", got[1].Jobs)
	}
	if !reflect.DeepEqual(steps, abcSteps()) {
		t.Fatal("simulation changed the steps")
	}
}
