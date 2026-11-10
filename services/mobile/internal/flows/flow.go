// Package flows holds the server-driven flow model: step graphs, publish-time
// validation (FR-32.2, BR-32.9), and the step runner that decides what one
// submitted scan or value does. It is pure; calls to the warehouse work API
// are returned to the caller as CallRequests, never made here.
package flows

import (
	"fmt"
	"regexp"
	"slices"
	"sort"

	"github.com/BenjaminSRussell/passage/services/mobile/internal/gs1"
)

const CodeGraph = "MOB-0113"

const (
	StepScan    = "scan"
	StepInput   = "input"
	StepSelect  = "select"
	StepConfirm = "confirm"
	StepDisplay = "display"
	StepBranch  = "branch"
	StepCall    = "call"
)

var stepTypes = []string{StepScan, StepInput, StepSelect, StepConfirm, StepDisplay, StepBranch, StepCall}

const (
	OpAssign    = "assign"
	OpConfirm   = "confirm"
	OpException = "exception"
	OpReceive   = "receive"
	OpGetWork   = "get-work"
)

var ops = []string{OpAssign, OpConfirm, OpException, OpReceive, OpGetWork}

const (
	MaxSteps  = 100
	MaxPrompt = 200
)

var (
	idFormat   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	codeFormat = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,39}$`)
)

// Call names a warehouse work API operation. Params maps an operation
// parameter to the state value that supplies it; a value starting with "="
// is a literal.
type Call struct {
	Op     string            `json:"op"`
	Params map[string]string `json:"params,omitempty"`
}

type Step struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Prompt       string             `json:"prompt"`
	ExpectedScan []gs1.ScanClass    `json:"expectedScan,omitempty"`
	Next         map[string]*string `json:"next"`
	Call         *Call              `json:"call,omitempty"`
}

// Draft is a flow definition: what the designer saves and a device runs.
type Draft struct {
	Code        string   `json:"code"`
	EntryStepID string   `json:"entryStepId"`
	Steps       []Step   `json:"steps"`
	Warehouses  []string `json:"warehouses,omitempty"`
}

type Issue struct {
	Code    string `json:"code"`
	StepID  string `json:"stepId,omitempty"`
	Message string `json:"message"`
}

func (d Draft) Step(id string) (Step, bool) {
	for _, s := range d.Steps {
		if s.ID == id {
			return s, true
		}
	}
	return Step{}, false
}

// ValidShape checks what must hold before a draft is stored; graph rules are
// checked at publish.
func ValidShape(d Draft) map[string]string {
	fields := map[string]string{}
	if !codeFormat.MatchString(d.Code) {
		fields["code"] = "must be upper snake case, 2 to 40 characters"
	}
	if len(d.Steps) > MaxSteps {
		fields["steps"] = fmt.Sprintf("at most %d steps", MaxSteps)
	}
	for _, s := range d.Steps {
		if len(s.Prompt) > MaxPrompt {
			fields["steps."+s.ID+".prompt"] = fmt.Sprintf("at most %d characters", MaxPrompt)
		}
	}
	return fields
}

// Validate checks a draft before publish and returns every issue found; an
// empty list means the flow may be published.
func Validate(d Draft) []Issue {
	var issues []Issue
	add := func(stepID, format string, args ...any) {
		issues = append(issues, Issue{Code: CodeGraph, StepID: stepID, Message: fmt.Sprintf(format, args...)})
	}
	if len(d.Steps) == 0 {
		add("", "flow has no steps")
		return issues
	}
	byID := map[string]Step{}
	for _, s := range d.Steps {
		if !idFormat.MatchString(s.ID) {
			add(s.ID, "step id %s must be lower snake case", s.ID)
		}
		if _, dup := byID[s.ID]; dup {
			add(s.ID, "step id %s is duplicated", s.ID)
		}
		byID[s.ID] = s
		if !slices.Contains(stepTypes, s.Type) {
			add(s.ID, "step %s has unknown type %s", s.ID, s.Type)
		}
		if s.Type == StepScan {
			if len(s.ExpectedScan) == 0 {
				add(s.ID, "scan step %s must declare an expected scan class", s.ID)
			}
			for _, c := range s.ExpectedScan {
				if !gs1.ValidClass(c) {
					add(s.ID, "scan step %s has unknown scan class %s", s.ID, c)
				}
			}
		}
		if s.Type == StepBranch && len(s.Next) < 2 {
			add(s.ID, "branch step %s needs at least two outcomes", s.ID)
		}
		if s.Type == StepCall && (s.Call == nil || !slices.Contains(ops, s.Call.Op)) {
			add(s.ID, "call step %s must name one of %v", s.ID, ops)
		}
	}
	if _, ok := byID[d.EntryStepID]; !ok {
		add("", "entry step %s does not exist", d.EntryStepID)
		return issues
	}
	for _, s := range d.Steps {
		for _, outcome := range sortedKeys(s.Next) {
			if t := s.Next[outcome]; t != nil {
				if _, ok := byID[*t]; !ok {
					add(s.ID, "step %s outcome %s points to missing step %s", s.ID, outcome, *t)
				}
			}
		}
	}
	reachable := map[string]bool{}
	stack := []string{d.EntryStepID}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if reachable[id] {
			continue
		}
		reachable[id] = true
		for _, t := range byID[id].Next {
			if t != nil {
				if _, ok := byID[*t]; ok {
					stack = append(stack, *t)
				}
			}
		}
	}
	for _, s := range d.Steps {
		if !reachable[s.ID] {
			add(s.ID, "step %s is unreachable from the entry step", s.ID)
		}
	}
	reverse := map[string][]string{}
	var queue []string
	for _, s := range d.Steps {
		terminal := len(s.Next) == 0
		for _, t := range s.Next {
			if t == nil {
				terminal = true
			} else if _, ok := byID[*t]; ok {
				reverse[*t] = append(reverse[*t], s.ID)
			}
		}
		if terminal {
			queue = append(queue, s.ID)
		}
	}
	finishing := map[string]bool{}
	for len(queue) > 0 {
		id := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if finishing[id] {
			continue
		}
		finishing[id] = true
		queue = append(queue, reverse[id]...)
	}
	for _, s := range d.Steps {
		if reachable[s.ID] && !finishing[s.ID] {
			add(s.ID, "step %s cannot reach the end of the flow", s.ID)
		}
	}
	return issues
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
