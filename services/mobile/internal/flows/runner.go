package flows

import (
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/BenjaminSRussell/passage/services/mobile/internal/gs1"
)

var (
	ErrWrongStep    = errors.New("flows: the submitted step is not the current step")
	ErrNoOutcome    = errors.New("flows: the outcome is not one of the step's outcomes")
	ErrValueNeeded  = errors.New("flows: the step needs a value")
	ErrFlowFinished = errors.New("flows: the flow already finished")
	ErrValueTooLong = errors.New("flows: the value is too long")
)

const MaxValueLength = 512

// State is where a device is in a flow. Version pins the flow version the run
// started on, so a publish never changes a run in progress (BR-32.9).
type State struct {
	FlowCode string            `json:"flowCode"`
	Version  int               `json:"version"`
	StepID   string            `json:"stepId"`
	Values   map[string]string `json:"values"`
	Done     bool              `json:"done"`
}

// Start begins a run at the entry step with seed values.
func Start(code string, version int, d Draft, seed map[string]string) State {
	values := maps.Clone(seed)
	if values == nil {
		values = map[string]string{}
	}
	return State{FlowCode: code, Version: version, StepID: d.EntryStepID, Values: values}
}

type Input struct {
	Value   string
	Outcome string
}

// CallRequest is a warehouse operation the current call step asks for, with
// its parameters resolved from the run's values.
type CallRequest struct {
	Op     string
	Params map[string]string
}

type Result struct {
	State   State
	Verdict *gs1.Verdict
	Call    *CallRequest
}

// Submit applies one input to the step with the given id. A scan that fails
// its check leaves the state unchanged and returns the verdict. A call step is
// not advanced here: it returns the request for the caller to make, then Resolve.
func Submit(d Draft, st State, stepID string, in Input, opts gs1.ClassifyOptions) (Result, error) {
	if st.Done {
		return Result{}, ErrFlowFinished
	}
	if stepID != st.StepID {
		return Result{}, fmt.Errorf("%w: expected %s, got %s", ErrWrongStep, st.StepID, stepID)
	}
	step, ok := d.Step(st.StepID)
	if !ok {
		return Result{}, ErrWrongStep
	}
	if len(in.Value) > MaxValueLength || len(in.Outcome) > MaxValueLength {
		return Result{}, ErrValueTooLong
	}
	next := clone(st)
	switch step.Type {
	case StepScan:
		c := gs1.ClassifyFor(in.Value, step.ExpectedScan, opts)
		verdict := firstOK(step.ExpectedScan, c)
		if verdict != nil {
			return Result{State: st, Verdict: verdict}, nil
		}
		record(next.Values, step.ID, c)
	case StepInput, StepSelect:
		if strings.TrimSpace(in.Value) == "" {
			return Result{}, ErrValueNeeded
		}
		next.Values[step.ID] = strings.TrimSpace(in.Value)
	case StepConfirm, StepDisplay:
		next.Values[step.ID] = "ok"
	case StepBranch:
		outcome := in.Outcome
		if outcome == "" {
			outcome = in.Value
		}
		if _, ok := step.Next[outcome]; !ok {
			return Result{}, ErrNoOutcome
		}
		next.Values[step.ID] = outcome
		return Result{State: advance(step, next, outcome)}, nil
	case StepCall:
		return Result{State: st, Call: &CallRequest{Op: step.Call.Op, Params: resolve(step.Call.Params, st.Values)}}, nil
	}
	return Result{State: advance(step, next, "default")}, nil
}

// Resolve completes a call step with the values the call produced.
func Resolve(d Draft, st State, produced map[string]string) (State, error) {
	step, ok := d.Step(st.StepID)
	if !ok || step.Type != StepCall {
		return st, ErrWrongStep
	}
	next := clone(st)
	for k, v := range produced {
		next.Values[k] = v
	}
	next.Values[step.ID] = "ok"
	return advance(step, next, "default"), nil
}

func advance(step Step, st State, outcome string) State {
	target, ok := step.Next[outcome]
	if !ok && len(step.Next) == 1 {
		for _, t := range step.Next {
			target, ok = t, true
		}
	}
	if !ok || target == nil {
		st.Done = true
		return st
	}
	st.StepID = *target
	return st
}

func firstOK(expected []gs1.ScanClass, c gs1.Classified) *gs1.Verdict {
	var first *gs1.Verdict
	for _, class := range expected {
		v := gs1.Check(class, c)
		if v == nil {
			return nil
		}
		if first == nil {
			first = v
		}
	}
	if first == nil {
		return &gs1.Verdict{Code: gs1.CodeNoMatch, Message: "Nothing is expected here."}
	}
	if len(expected) > 1 && first.Code == gs1.CodeNoMatch {
		labels := make([]string, len(expected))
		for i, e := range expected {
			labels[i] = string(e)
		}
		first.Message = "Expected " + strings.Join(labels, " or ") + ", scanned " + gs1.Describe(c) + "."
	}
	return first
}

func record(values map[string]string, stepID string, c gs1.Classified) {
	if c.GS1 == nil {
		values[stepID] = c.Value
		return
	}
	for _, ai := range c.GS1.Order {
		values[stepID+"."+ai] = c.GS1.Fields[ai]
	}
	switch {
	case c.GS1.Fields["00"] != "":
		values[stepID] = c.GS1.Fields["00"]
	case c.GS1.Fields["01"] != "":
		values[stepID] = gs1.ToGTIN14(c.GS1.Fields["01"])
	case c.GS1.Fields["10"] != "":
		values[stepID] = c.GS1.Fields["10"]
	case len(c.GS1.Order) > 0:
		values[stepID] = c.GS1.Fields[c.GS1.Order[0]]
	}
}

func resolve(params map[string]string, values map[string]string) map[string]string {
	out := make(map[string]string, len(params))
	for name, key := range params {
		if lit, ok := strings.CutPrefix(key, "="); ok {
			out[name] = lit
		} else {
			out[name] = values[key]
		}
	}
	return out
}

func clone(st State) State {
	st.Values = maps.Clone(st.Values)
	if st.Values == nil {
		st.Values = map[string]string{}
	}
	return st
}
