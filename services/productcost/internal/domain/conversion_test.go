package domain

import (
	"testing"
	"time"
)

var allBlockers = []string{BlockNegativeOnHand, BlockInTransit, BlockStartedOrders}

func cleanPosition() ConversionPosition {
	return ConversionPosition{ItemID: "IT-1", SiteID: "S-1", OnHand: d("500"), StandardUnit: d("10.00"), ActualValue: d("4925.00")}
}

func input(pos ...ConversionPosition) ConversionInput {
	return ConversionInput{ConversionDate: "2026-11-01", CloseThrough: "2026-10-31", Blockers: allBlockers, Positions: pos}
}

func hasCode(diags []Diagnostic, code, severity string) bool {
	for _, dg := range diags {
		if dg.Code == code && dg.Severity == severity {
			return true
		}
	}
	return false
}

func TestConversion_RevaluationPreviewMatchesAC10(t *testing.T) {
	rep, err := ValidateConversion(input(cleanPosition()))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Blocked {
		t.Fatalf("clean conversion blocked: %+v", rep.Diagnostics)
	}
	if got := rep.Revaluation.StringFixed(2); got != "75.00" {
		t.Errorf("revaluation = %s, want 75.00", got)
	}
	want := []JournalLine{
		{Account: AccountInventory, Debit: d("75.00")},
		{Account: AccountCostChangeVar, Credit: d("75.00")},
	}
	if len(rep.Journal) != 2 {
		t.Fatalf("journal = %+v, want %+v", rep.Journal, want)
	}
	for i := range want {
		if rep.Journal[i].Account != want[i].Account ||
			!rep.Journal[i].Debit.Equal(want[i].Debit) || !rep.Journal[i].Credit.Equal(want[i].Credit) {
			t.Errorf("journal[%d] = %+v, want %+v", i, rep.Journal[i], want[i])
		}
	}
}

func TestConversion_CloseDateMustBeDayBeforeConversion(t *testing.T) {
	for _, closeThrough := range []string{"2026-10-30", "2026-11-01", ""} {
		in := input(cleanPosition())
		in.CloseThrough = closeThrough
		rep, err := ValidateConversion(in)
		if err != nil {
			t.Fatal(err)
		}
		// The close is a later checklist step, so validation only warns.
		if rep.CloseComplete || rep.Blocked || !hasCode(rep.Diagnostics, CodeConversionClose, "warning") {
			t.Errorf("close %q: want CST-0731 warning and closeComplete false, got %+v", closeThrough, rep)
		}
	}
	rep, _ := ValidateConversion(input(cleanPosition()))
	if !rep.CloseComplete || hasCode(rep.Diagnostics, CodeConversionClose, "warning") {
		t.Errorf("close through 2026-10-31 for 2026-11-01 is complete: %+v", rep)
	}
}

// TestConversion_CloseStepAndActivationNeedTheClose checks BR-39.11: the
// checklist validates and previews before the close, but the close step and
// activation refuse a missing close with CST-0731.
func TestConversion_CloseStepAndActivationNeedTheClose(t *testing.T) {
	at := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	in := input(cleanPosition())
	in.CloseThrough = ""
	open, _ := ValidateConversion(in)
	run := ConversionRun{}
	var err error
	for _, step := range []string{StepSelect, StepRoll, StepValidate, StepPreview} {
		if run, err = Advance(run, step, "alice", at, open); err != nil {
			t.Fatalf("step %s before the close: %v", step, err)
		}
	}
	if run.Status != ConversionValidated {
		t.Errorf("status = %s, want Validated", run.Status)
	}
	if _, err := Advance(run, StepClose, "alice", at, open); codeOf(err) != CodeConversionClose {
		t.Fatalf("close without the inventory close: err %v, want %s", err, CodeConversionClose)
	}
	closed, _ := ValidateConversion(input(cleanPosition()))
	if run, err = Advance(run, StepClose, "alice", at, closed); err != nil || run.Status != ConversionClosed {
		t.Fatalf("close: %v, status %s", err, run.Status)
	}
	if _, err := Advance(run, StepActivate, "alice", at, open); codeOf(err) != CodeConversionClose {
		t.Errorf("activate without the close: err %v, want %s", err, CodeConversionClose)
	}
	bad := cleanPosition()
	bad.OnHand = d("-1")
	blocked, _ := ValidateConversion(input(bad))
	if _, err := Advance(run, StepActivate, "alice", at, blocked); codeOf(err) != CodeChecklistBlocked {
		t.Errorf("activate with a blocker: err %v, want %s", err, CodeChecklistBlocked)
	}
}

func TestConversion_ForgedChecklistIsRejected(t *testing.T) {
	at := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	ok, _ := ValidateConversion(input(cleanPosition()))
	cases := map[string]ConversionRun{
		"skips roll":     {Checklist: []ChecklistStep{{Step: StepSelect, Status: StepDone, User: "a", At: at}, {Step: StepValidate, Status: StepDone, User: "a", At: at}}},
		"no user":        {Checklist: []ChecklistStep{{Step: StepSelect, Status: StepDone, At: at}}},
		"unknown status": {Checklist: []ChecklistStep{{Step: StepSelect, Status: "Skipped", User: "a", At: at}}},
	}
	for name, run := range cases {
		if CheckChecklist(run) < 0 {
			t.Errorf("%s: checklist accepted", name)
		}
		if _, err := Advance(run, StepRoll, "a", at, ok); err == nil {
			t.Errorf("%s: advance accepted a forged checklist", name)
		}
	}
	// A status the checklist does not support is replaced by the derived one.
	run, err := Advance(ConversionRun{Status: ConversionActivated}, StepSelect, "a", at, ok)
	if err != nil || run.Status != ConversionDraft {
		t.Errorf("status = %s (%v), want Draft derived from the checklist", run.Status, err)
	}
}

func TestConversion_RoundsPerPositionInTheCurrency(t *testing.T) {
	// 3 × 3.3333 = 9.9999 against 9.00 actual: 1.00 in USD, 1 in JPY, 0.9999 → 1.000 in KWD.
	pos := ConversionPosition{ItemID: "X", SiteID: "S", OnHand: d("3"), StandardUnit: d("3.3333"), ActualValue: d("9")}
	for currency, want := range map[string]string{"USD": "1", "JPY": "1", "KWD": "1"} {
		in := input(pos, pos)
		in.Currency = currency
		rep, err := ValidateConversion(in)
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Lines) != 2 || !rep.Lines[0].Amount.Equal(d(want)) {
			t.Errorf("%s lines = %+v, want each %s", currency, rep.Lines, want)
		}
		sum := rep.Lines[0].Amount.Add(rep.Lines[1].Amount)
		if !rep.Revaluation.Equal(sum) || !rep.Journal[0].Debit.Equal(sum) {
			t.Errorf("%s: revaluation %s and journal %+v must equal the rows %s", currency, rep.Revaluation, rep.Journal, sum)
		}
	}
}

func TestConversion_NilBlockersMeansAllBlock(t *testing.T) {
	neg := cleanPosition()
	neg.OnHand = d("-3")
	in := input(neg)
	in.Blockers = nil
	rep, _ := ValidateConversion(in)
	if !rep.Blocked {
		t.Errorf("default blockers did not block negative on-hand: %+v", rep.Diagnostics)
	}
}

func TestConversion_BadDatesAreRejected(t *testing.T) {
	in := input(cleanPosition())
	in.ConversionDate = "01/11/2026"
	if _, err := ValidateConversion(in); err == nil {
		t.Error("bad conversion date accepted")
	}
	in = input(cleanPosition())
	in.CloseThrough = "yesterday"
	if _, err := ValidateConversion(in); err == nil {
		t.Error("bad close date accepted")
	}
}

func TestConversion_BlockerMatrix(t *testing.T) {
	neg := cleanPosition()
	neg.OnHand = d("-3")
	transit := cleanPosition()
	transit.InTransit = 2
	started := cleanPosition()
	started.StartedOrders = 1
	cases := []struct {
		name     string
		pos      ConversionPosition
		blockers []string
		code     string
		severity string
		blocked  bool
	}{
		{"negative on-hand blocks when enabled", neg, allBlockers, CodeNegativeOnHand, "error", true},
		{"negative on-hand warns when disabled", neg, []string{}, CodeNegativeOnHand, "warning", false},
		{"in-transit blocks when enabled", transit, allBlockers, CodeConversionInTrans, "error", true},
		{"in-transit warns when disabled", transit, []string{}, CodeConversionInTrans, "warning", false},
		{"started orders block when enabled", started, allBlockers, CodeStartedOrders, "error", true},
		{"started orders settle at new standard when allowed", started, []string{}, CodeStartedOrders, "warning", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := input(tc.pos)
			in.Blockers = tc.blockers
			rep, err := ValidateConversion(in)
			if err != nil {
				t.Fatal(err)
			}
			if !hasCode(rep.Diagnostics, tc.code, tc.severity) {
				t.Errorf("diagnostics %+v, want %s %s", rep.Diagnostics, tc.code, tc.severity)
			}
			if rep.Blocked != tc.blocked {
				t.Errorf("blocked = %v, want %v", rep.Blocked, tc.blocked)
			}
		})
	}
}

func TestConversion_ChecklistRunsInOrderAndRecordsUsers(t *testing.T) {
	at := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	ok, _ := ValidateConversion(input(cleanPosition()))
	run := ConversionRun{Status: ConversionDraft}

	if NextStep(run) != StepSelect {
		t.Fatalf("first step = %s", NextStep(run))
	}
	if _, err := Advance(run, StepValidate, "alice", at, ok); err == nil {
		t.Fatal("validate ran before select")
	}
	var err error
	for _, step := range ConversionSteps {
		run, err = Advance(run, step, "alice", at, ok)
		if err != nil {
			t.Fatalf("step %s: %v", step, err)
		}
	}
	if NextStep(run) != "" {
		t.Errorf("checklist not complete, next = %s", NextStep(run))
	}
	if run.Status != ConversionActivated {
		t.Errorf("status = %s, want Activated", run.Status)
	}
	if len(run.Checklist) != len(ConversionSteps) || run.Checklist[2].User != "alice" || !run.Checklist[2].At.Equal(at) {
		t.Errorf("checklist does not record users and timestamps: %+v", run.Checklist)
	}
	if _, err := Advance(run, StepActivate, "alice", at, ok); err == nil {
		t.Error("completed checklist accepted another activate")
	}
}

func TestConversion_BlockedReportStopsAtValidate(t *testing.T) {
	bad := cleanPosition()
	bad.OnHand = d("-1")
	rep, _ := ValidateConversion(input(bad))
	run := ConversionRun{Status: ConversionDraft, Checklist: []ChecklistStep{
		{Step: StepSelect, Status: StepDone, User: "bob", At: time.Now()},
		{Step: StepRoll, Status: StepDone, User: "bob", At: time.Now()},
	}}
	_, err := Advance(run, StepValidate, "bob", time.Now(), rep)
	if codeOf(err) != CodeChecklistBlocked {
		t.Fatalf("err %v, want %s", err, CodeChecklistBlocked)
	}
}

func TestConversion_UserIsRequired(t *testing.T) {
	ok, _ := ValidateConversion(input(cleanPosition()))
	if _, err := Advance(ConversionRun{}, StepSelect, "", time.Now(), ok); err == nil {
		t.Error("step without a user accepted")
	}
}
