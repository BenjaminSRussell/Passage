package domain

import (
	"fmt"
	"regexp"
)

// CodeSequenceStep is raised when a maintenance sequence has invalid steps or
// scope (FR-44.15); a service code, the PRD names none.
const CodeSequenceStep = "EAM-SEQ-001"

// MaxSequenceMultiple bounds a step's multiple, and MaxSequenceHierarchy its
// hierarchy (a smallint in the PRD data model).
const (
	MaxSequenceMultiple  = 1000
	MaxSequenceHierarchy = 32767
)

// SequenceStep is one job type of a maintenance sequence (BR-44.10). A step is due
// on every occurrence whose number is a multiple of Multiple; the highest Hierarchy
// among the due steps is the one performed, and the steps it Includes are not
// repeated.
type SequenceStep struct {
	Code      string   `json:"code"`
	JobType   string   `json:"jobType"`
	Multiple  int      `json:"multiple"`
	Hierarchy int      `json:"hierarchy"`
	Includes  []string `json:"includes"`
}

var (
	stepCodeRe = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	jobTypeRe  = regexp.MustCompile(`^[A-Z0-9_]{2,20}$`)
)

// ValidateSequenceSteps checks the structure of a sequence: 1 to 50 steps with
// unique codes, job type codes, multiples of 1 to 1000, unique hierarchy values
// of 1 or more, and includes that name other steps of lower hierarchy (so the
// selected step is unambiguous and a step never suppresses a bigger one).
func ValidateSequenceSteps(steps []SequenceStep) error {
	if len(steps) == 0 || len(steps) > 50 {
		return GateError{Code: CodeSequenceStep, Detail: "a sequence needs 1 to 50 steps"}
	}
	byCode := make(map[string]SequenceStep, len(steps))
	hier := make(map[int]string, len(steps))
	for _, s := range steps {
		switch {
		case !stepCodeRe.MatchString(s.Code):
			return GateError{Code: CodeSequenceStep, Detail: fmt.Sprintf("step code %q is invalid", s.Code)}
		case !jobTypeRe.MatchString(s.JobType):
			return GateError{Code: CodeSequenceStep, Detail: fmt.Sprintf("step %s needs a job type code like OVERHAUL", s.Code)}
		case s.Multiple < 1 || s.Multiple > MaxSequenceMultiple:
			return GateError{Code: CodeSequenceStep, Detail: fmt.Sprintf("step %s multiple must be 1 to %d", s.Code, MaxSequenceMultiple)}
		case s.Hierarchy < 1 || s.Hierarchy > MaxSequenceHierarchy:
			return GateError{Code: CodeSequenceStep, Detail: fmt.Sprintf("step %s hierarchy must be 1 to %d", s.Code, MaxSequenceHierarchy)}
		}
		if _, dup := byCode[s.Code]; dup {
			return GateError{Code: CodeSequenceStep, Detail: fmt.Sprintf("duplicate step code %s", s.Code)}
		}
		if other, dup := hier[s.Hierarchy]; dup {
			return GateError{Code: CodeSequenceStep,
				Detail: fmt.Sprintf("steps %s and %s share hierarchy %d", other, s.Code, s.Hierarchy)}
		}
		byCode[s.Code] = s
		hier[s.Hierarchy] = s.Code
	}
	for _, s := range steps {
		seen := make(map[string]bool, len(s.Includes))
		for _, inc := range s.Includes {
			if seen[inc] {
				return GateError{Code: CodeSequenceStep, Detail: fmt.Sprintf("step %s includes %s twice", s.Code, inc)}
			}
			seen[inc] = true
			target, ok := byCode[inc]
			switch {
			case !ok:
				return GateError{Code: CodeSequenceStep, Detail: fmt.Sprintf("step %s includes unknown step %s", s.Code, inc)}
			case inc == s.Code:
				return GateError{Code: CodeSequenceStep, Detail: fmt.Sprintf("step %s includes itself", s.Code)}
			case target.Hierarchy >= s.Hierarchy:
				return GateError{Code: CodeSequenceStep,
					Detail: fmt.Sprintf("step %s may only include lower-hierarchy steps, not %s", s.Code, inc)}
			}
		}
	}
	return nil
}

// Occurrence is the outcome of generating one sequence occurrence: the step codes
// to perform, selected step first, then the extra due steps not covered by it.
type Occurrence struct {
	Number int      `json:"occurrenceNumber"`
	Steps  []string `json:"stepCodes"`
	Jobs   []string `json:"jobTypes"`
}

// SelectSteps implements BR-44.10 for occurrence number k (1-based). It assumes the
// steps were validated by ValidateSequenceSteps. The result is empty when no step
// is due on that occurrence.
func SelectSteps(steps []SequenceStep, k int) Occurrence {
	occ := Occurrence{Number: k, Steps: []string{}, Jobs: []string{}}
	if k < 1 {
		return occ
	}
	var due []SequenceStep
	for _, s := range steps {
		if k%s.Multiple == 0 {
			due = append(due, s)
		}
	}
	if len(due) == 0 {
		return occ
	}
	top := due[0]
	for _, s := range due[1:] {
		if s.Hierarchy > top.Hierarchy {
			top = s
		}
	}
	suppressed := make(map[string]bool, len(top.Includes)+1)
	suppressed[top.Code] = true
	for _, inc := range top.Includes {
		suppressed[inc] = true
	}
	occ.Steps = append(occ.Steps, top.Code)
	occ.Jobs = append(occ.Jobs, top.JobType)
	for _, s := range due {
		if !suppressed[s.Code] {
			occ.Steps = append(occ.Steps, s.Code)
			occ.Jobs = append(occ.Jobs, s.JobType)
		}
	}
	return occ
}

// SimulateSteps returns the next n occurrences after last (BR-44.10), without
// changing anything: the "simulate 12 months" view of the sequence editor.
func SimulateSteps(steps []SequenceStep, last, n int) []Occurrence {
	out := make([]Occurrence, 0, n)
	for k := last + 1; k <= last+n; k++ {
		out = append(out, SelectSteps(steps, k))
	}
	return out
}
