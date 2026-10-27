package domain

import (
	"errors"
	"slices"
	"testing"

	"github.com/shopspring/decimal"
	"pgregory.net/rapid"
)

func code(err error) string {
	var de *Error
	if errors.As(err, &de) {
		return de.Code
	}
	return ""
}

func done(reported, scrap string, units int) Completion {
	return Completion{ReportedQty: decimal.RequireFromString(reported), ScrapQty: decimal.RequireFromString(scrap), HandlingUnits: units}
}

func TestStartJob(t *testing.T) {
	tests := []struct {
		name    string
		job     State
		card    State
		picking bool
		want    []State
		code    string
	}{
		{"first job of a planned card walks it through prepared", Planned, Planned, false, []State{Prepared, InProgress}, ""},
		{"first job of a prepared card", Prepared, Prepared, true, []State{InProgress}, ""},
		{"second job of a running card", Planned, InProgress, false, []State{}, ""},
		{"picking activity waits for a prepared card", Planned, Planned, true, nil, CodeMove},
		{"not-planned job cannot start", NotPlanned, NotPlanned, false, nil, CodeMove},
		{"running job cannot start again", InProgress, InProgress, false, nil, CodeMove},
		{"completed job cannot restart", Completed, InProgress, false, nil, CodeMove},
		{"completed card takes no new work", Planned, Completed, false, nil, CodeMove},
		{"emptied card takes no new work", Planned, Emptied, false, nil, CodeMove},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := StartJob(tc.job, tc.card, tc.picking)
			if code(err) != tc.code {
				t.Fatalf("err = %v, want code %q", err, tc.code)
			}
			if tc.code == "" && !slices.Equal(got, tc.want) {
				t.Fatalf("card steps = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCompleteJob(t *testing.T) {
	ok := done("20", "0", 1)
	tests := []struct {
		name   string
		job    State
		card   State
		others []State
		in     Completion
		want   []State
		code   string
	}{
		{"planned job dragged to completed (AC-24.7)", Planned, InProgress, nil, ok, nil, CodeMove},
		{"prepared job cannot skip in progress", Prepared, InProgress, nil, ok, nil, CodeMove},
		{"completed job cannot complete twice", Completed, InProgress, nil, ok, nil, CodeMove},
		{"card not in progress", InProgress, Prepared, nil, ok, nil, CodeMove},
		{"negative scrap", InProgress, InProgress, nil, done("20", "-1", 1), nil, CodeInvalid},
		{"nothing reported", InProgress, InProgress, nil, done("0", "0", 1), nil, CodeInvalid},
		{"no handling unit", InProgress, InProgress, nil, done("20", "0", 0), nil, CodeInvalid},
		{"only scrap is a valid report", InProgress, InProgress, nil, done("0", "2", 1), []State{Completed}, ""},
		{"an open sibling keeps the card running", InProgress, InProgress, []State{Completed, Planned}, ok, []State{}, ""},
		{"the last job completes the card", InProgress, InProgress, []State{Completed, Completed}, ok, []State{Completed}, ""},
		{"a single-job card completes", InProgress, InProgress, nil, ok, []State{Completed}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := CompleteJob(tc.job, tc.card, tc.others, tc.in)
			if code(err) != tc.code {
				t.Fatalf("err = %v, want code %q", err, tc.code)
			}
			if tc.code == "" && !slices.Equal(got, tc.want) {
				t.Fatalf("card steps = %v, want %v", got, tc.want)
			}
		})
	}
}

// Whatever the job and card states, the card steps a job returns are legal
// one-step BR-24.6 moves from the card's state, and they stop at In progress
// (start) or Completed (complete). Completion happens exactly when every other
// job is completed.
func TestJobsKeepTheCardStateMachine(t *testing.T) {
	all := states()
	jobStates := []State{NotPlanned, Planned, Prepared, InProgress, Completed}
	rapid.Check(t, func(t *rapid.T) {
		card := rapid.SampledFrom(all).Draw(t, "card")
		job := rapid.SampledFrom(jobStates).Draw(t, "job")
		legal := func(steps []State, end State) {
			from := card
			for _, s := range steps {
				if err := CheckMove(from, s); err != nil {
					t.Fatalf("card step %s → %s: %v", from, s, err)
				}
				from = s
			}
			if len(steps) > 0 && from != end {
				t.Fatalf("card ends at %s, want %s", from, end)
			}
		}
		if steps, err := StartJob(job, card, rapid.Bool().Draw(t, "picking")); err == nil {
			legal(steps, InProgress)
			if len(steps) == 0 && card != InProgress {
				t.Fatalf("start left the card %s", card)
			}
		}
		others := rapid.SliceOfN(rapid.SampledFrom(jobStates), 0, 4).Draw(t, "others")
		steps, err := CompleteJob(job, card, others, done("1", "0", 1))
		if err != nil {
			return
		}
		legal(steps, Completed)
		allDone := !slices.ContainsFunc(others, func(s State) bool { return s != Completed })
		if allDone != (len(steps) == 1) {
			t.Fatalf("others %v gave card steps %v", others, steps)
		}
	})
}

func TestJobStatesAndJobDrivenMoves(t *testing.T) {
	if ValidJobState(Emptied) || !ValidJobState(NotPlanned) || ValidJobState("done") {
		t.Fatal("job states are the card states without emptied")
	}
	for _, s := range states() {
		if JobDriven(s) != (s == InProgress || s == Completed) {
			t.Fatalf("JobDriven(%s)", s)
		}
	}
}
