package flows

import (
	"errors"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/mobile/internal/gs1"
)

func TestCoreFlowsAreValid(t *testing.T) {
	core := CoreFlows()
	if len(core) != 8 {
		t.Fatalf("core flows = %d, want the seven of FR-32.4 plus RECEIVE", len(core))
	}
	for _, f := range core {
		if issues := Validate(f); len(issues) > 0 {
			t.Errorf("%s: %+v", f.Code, issues)
		}
		if shape := ValidShape(f); len(shape) > 0 {
			t.Errorf("%s shape: %v", f.Code, shape)
		}
	}
}

func TestValidateRejectsBrokenGraphs(t *testing.T) {
	cases := []struct {
		name string
		d    Draft
		want string
	}{
		{"empty", Draft{Code: "X1"}, "no steps"},
		{"missing entry", Draft{Code: "X1", EntryStepID: "nope", Steps: []Step{{ID: "a", Type: StepConfirm, Next: map[string]*string{}}}}, "entry step nope"},
		{"unreachable", Draft{Code: "X1", EntryStepID: "a", Steps: []Step{
			{ID: "a", Type: StepConfirm, Next: map[string]*string{}},
			{ID: "b", Type: StepConfirm, Next: map[string]*string{}},
		}}, "step b is unreachable"},
		{"never terminates", Draft{Code: "X1", EntryStepID: "a", Steps: []Step{
			{ID: "a", Type: StepConfirm, Next: map[string]*string{"default": to("b")}},
			{ID: "b", Type: StepConfirm, Next: map[string]*string{"default": to("a")}},
		}}, "cannot reach the end"},
		{"dangling", Draft{Code: "X1", EntryStepID: "a", Steps: []Step{{ID: "a", Type: StepConfirm, Next: map[string]*string{"default": to("zz")}}}}, "missing step zz"},
		{"scan without class", Draft{Code: "X1", EntryStepID: "a", Steps: []Step{{ID: "a", Type: StepScan, Next: map[string]*string{}}}}, "expected scan class"},
		{"branch with one outcome", Draft{Code: "X1", EntryStepID: "a", Steps: []Step{{ID: "a", Type: StepBranch, Next: map[string]*string{"x": nil}}}}, "at least two outcomes"},
		{"call without op", Draft{Code: "X1", EntryStepID: "a", Steps: []Step{{ID: "a", Type: StepCall, Next: map[string]*string{}}}}, "must name one of"},
		{"bad id", Draft{Code: "X1", EntryStepID: "A", Steps: []Step{{ID: "A", Type: StepConfirm, Next: map[string]*string{}}}}, "lower snake case"},
	}
	for _, c := range cases {
		issues := Validate(c.d)
		found := false
		for _, i := range issues {
			found = found || (i.Code == CodeGraph && strings.Contains(i.Message, c.want))
		}
		if !found {
			t.Errorf("%s: want an issue containing %q, got %+v", c.name, c.want, issues)
		}
	}
}

func pick(t *testing.T) Draft {
	t.Helper()
	for _, f := range CoreFlows() {
		if f.Code == "PICK" {
			return f
		}
	}
	t.Fatal("no PICK flow")
	return Draft{}
}

func TestPickRunsToConfirmCall(t *testing.T) {
	d := pick(t)
	st := Start("PICK", 1, d, map[string]string{"work_id": "w1", "line_no": "2"})
	steps := []struct {
		id string
		in Input
	}{
		{"scan_location", Input{Value: "A-01-02"}},
		{"scan_item", Input{Value: "]C10109501101530003"}},
		{"enter_qty", Input{Value: "5"}},
		{"qty_check", Input{Outcome: "full"}},
		{"confirm_pick", Input{}},
	}
	for _, s := range steps {
		r, err := Submit(d, st, s.id, s.in, gs1.ClassifyOptions{})
		if err != nil || r.Verdict != nil {
			t.Fatalf("%s: err=%v verdict=%+v", s.id, err, r.Verdict)
		}
		st = r.State
	}
	if st.StepID != "call_confirm" || st.Values["scan_item"] != "09501101530003" || st.Values["scan_item.01"] != "09501101530003" {
		t.Fatalf("state before call: %+v", st)
	}
	r, err := Submit(d, st, "call_confirm", Input{}, gs1.ClassifyOptions{})
	if err != nil || r.Call == nil || r.Call.Op != OpConfirm {
		t.Fatalf("call request: %+v %v", r, err)
	}
	want := map[string]string{"workId": "w1", "lineNo": "2", "qty": "5"}
	for k, v := range want {
		if r.Call.Params[k] != v {
			t.Errorf("param %s = %q, want %q", k, r.Call.Params[k], v)
		}
	}
	done, err := Resolve(d, st, map[string]string{"work_version": "4"})
	if err != nil || !done.Done || done.Values["work_version"] != "4" {
		t.Fatalf("resolve: %+v %v", done, err)
	}
}

func TestShortPickTakesExceptionPath(t *testing.T) {
	d := pick(t)
	st := Start("PICK", 1, d, map[string]string{"work_id": "w1", "line_no": "1"})
	st.StepID = "qty_check"
	r, err := Submit(d, st, "qty_check", Input{Outcome: "short"}, gs1.ClassifyOptions{})
	if err != nil || r.State.StepID != "short_reason" {
		t.Fatalf("branch: %+v %v", r.State, err)
	}
	st = r.State
	st.Values["enter_qty"] = "3"
	r, _ = Submit(d, st, "short_reason", Input{Value: "DAMAGED"}, gs1.ClassifyOptions{})
	r, _ = Submit(d, r.State, "call_short", Input{}, gs1.ClassifyOptions{})
	if r.Call == nil || r.Call.Op != OpException || r.Call.Params["action"] != "SHORT_PICK" || r.Call.Params["reason"] != "DAMAGED" || r.Call.Params["qty"] != "3" {
		t.Fatalf("exception call: %+v", r.Call)
	}
}

func TestScanMismatchDoesNotAdvance(t *testing.T) {
	d := pick(t)
	st := Start("PICK", 1, d, nil)
	r, err := Submit(d, st, "scan_location", Input{Value: "]C10109501101530003"}, gs1.ClassifyOptions{})
	if err != nil || r.Verdict == nil || r.Verdict.Code != gs1.CodeNoMatch || r.State.StepID != "scan_location" {
		t.Fatalf("mismatch must stay on the step with MOB-0101: %+v %v", r, err)
	}
	if r.Verdict.Message != "Expected location, scanned GTIN." {
		t.Fatalf("message: %q", r.Verdict.Message)
	}
	r, _ = Submit(d, st, "scan_location", Input{Value: "]C10109501101530004"}, gs1.ClassifyOptions{})
	if r.Verdict == nil || r.Verdict.Code != gs1.CodeCheckDigit {
		t.Fatalf("check digit failure must be reported: %+v", r.Verdict)
	}
}

func TestSubmitGuards(t *testing.T) {
	d := pick(t)
	st := Start("PICK", 1, d, nil)
	if _, err := Submit(d, st, "enter_qty", Input{Value: "1"}, gs1.ClassifyOptions{}); !errors.Is(err, ErrWrongStep) {
		t.Fatalf("wrong step: %v", err)
	}
	st.StepID = "enter_qty"
	if _, err := Submit(d, st, "enter_qty", Input{Value: "  "}, gs1.ClassifyOptions{}); !errors.Is(err, ErrValueNeeded) {
		t.Fatalf("blank value: %v", err)
	}
	st.StepID = "qty_check"
	if _, err := Submit(d, st, "qty_check", Input{Outcome: "maybe"}, gs1.ClassifyOptions{}); !errors.Is(err, ErrNoOutcome) {
		t.Fatalf("unknown outcome: %v", err)
	}
	st.Done = true
	if _, err := Submit(d, st, "qty_check", Input{Outcome: "full"}, gs1.ClassifyOptions{}); !errors.Is(err, ErrFlowFinished) {
		t.Fatalf("finished: %v", err)
	}
}

func TestMultiClassScanAcceptsEither(t *testing.T) {
	d := Draft{Code: "X1", EntryStepID: "s", Steps: []Step{{ID: "s", Type: StepScan, ExpectedScan: []gs1.ScanClass{gs1.ClassLP, gs1.ClassLocation}, Next: map[string]*string{"default": nil}}}}
	st := Start("X1", 1, d, nil)
	if r, _ := Submit(d, st, "s", Input{Value: "A-01"}, gs1.ClassifyOptions{}); !r.State.Done {
		t.Fatalf("location must satisfy LP or LOCATION: %+v", r)
	}
	r, _ := Submit(d, st, "s", Input{Value: "]C10109501101530003"}, gs1.ClassifyOptions{})
	if r.Verdict == nil || r.Verdict.Message != "Expected LP or LOCATION, scanned GTIN." {
		t.Fatalf("verdict: %+v", r.Verdict)
	}
}
