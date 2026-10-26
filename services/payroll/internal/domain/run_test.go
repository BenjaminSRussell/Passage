package domain

import (
	"errors"
	"testing"
)

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from, to RunStatus
		want     bool
	}{
		{RunDraft, RunCalculated, true},
		{RunCalculated, RunDraft, true},
		{RunCalculated, RunReviewed, true},
		{RunReviewed, RunFinalized, true},
		{RunFinalized, RunPosted, true},
		{RunPosted, RunPaid, true},
		{RunReviewed, RunCanceled, true},
		{RunDraft, RunCanceled, true},
		{RunFinalized, RunDraft, false},
		{RunFinalized, RunCanceled, false},
		{RunPaid, RunDraft, false},
		{RunDraft, RunFinalized, false},
	}
	for _, tc := range tests {
		if got := CanTransition(tc.from, tc.to); got != tc.want {
			t.Errorf("%s -> %s = %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
	var de *Error
	if err := CheckTransition(RunPaid, RunDraft); !errors.As(err, &de) || de.Code != CodeRunTransition {
		t.Fatalf("CheckTransition error = %v", err)
	}
}

func TestSegregationOfDuties(t *testing.T) {
	if err := CheckFinalize("u1", "u1"); err == nil {
		t.Fatal("calculator must not finalize (PAY-RUN-004)")
	}
	if err := CheckFinalize("u1", "u2"); err != nil {
		t.Fatalf("different finalizer refused: %v", err)
	}
	if err := CheckPublish("author", "author"); err == nil {
		t.Fatal("author must not publish own rule set (PAY-RULE-002)")
	}
	if err := CheckPublish("author", "approver"); err != nil {
		t.Fatalf("second approver refused: %v", err)
	}
}

func TestRetroDeltaSumsEarningDeltasAcrossPeriods(t *testing.T) {
	period := func(regular, tax string) []Line {
		return []Line{
			{Code: "REGULAR", Kind: KindEarning, Amount: dec(regular), RuleVersion: "US-FED-2025.1"},
			{Code: "FED_WH", Kind: KindTax, Amount: dec(tax), RuleVersion: "US-FED-2025.1"},
		}
	}
	// AC-43.4: a rate increase effective two periods back raises regular pay
	// from 4,000.00 to 4,200.00 in each of them.
	got := RetroDelta(
		RetroPeriod{Prior: period("4000", "504.66"), Recalculated: period("4200", "548.66")},
		RetroPeriod{Prior: period("4000", "504.66"), Recalculated: period("4200", "548.66")},
	)
	if len(got) != 1 || got[0].Code != "RETRO_REGULAR" || got[0].Kind != KindEarning || !got[0].Amount.Equal(dec("400")) {
		t.Fatalf("retro lines = %+v; want one RETRO_REGULAR of 400 and no tax deltas", got)
	}
	if !RetroSum(got).Equal(dec("400")) {
		t.Fatalf("retro sum = %s", RetroSum(got))
	}

	// The current run calculates the deltas as earnings: taxes recompute on
	// the new gross under the current-period method (BR-43.4).
	in := Input{RunType: RunRegular, Periods: 26, W4: W4{FilingStatus: FilingSingle}, State: "CO",
		Earnings: append([]Earning{{Code: "REGULAR", Amount: dec("4200")}}, RetroEarnings(got)...),
		PreTax:   []Deduction{{Code: "401K", Amount: dec("240")}}}
	st, err := Calculate(US2025(), in)
	if err != nil {
		t.Fatal(err)
	}
	// Fed wages 4,360 × 26 − 8,600 = 104,760 → 5,578.50 + 22% × 49,885 = 16,553.20 / 26.
	if !st.Gross.Equal(dec("4600")) || !lineAmounts(st)["FED_WH"].Equal(dec("636.66")) || !lineAmounts(st)["RETRO_REGULAR"].Equal(dec("400")) {
		t.Fatalf("gross %s FED_WH %s RETRO_REGULAR %s", st.Gross, lineAmounts(st)["FED_WH"], lineAmounts(st)["RETRO_REGULAR"])
	}
}

func TestRetroDeltaCodesThatDisappearOrNetToZero(t *testing.T) {
	prior := []Line{
		{Code: "REGULAR", Kind: KindEarning, Amount: dec("4000")},
		{Code: "SHIFT_DIFF", Kind: KindEarning, Amount: dec("150")},
	}
	recalc := []Line{{Code: "REGULAR", Kind: KindEarning, Amount: dec("4000")}}
	got := RetroDelta(RetroPeriod{Prior: prior, Recalculated: recalc})
	if len(got) != 1 || got[0].Code != "RETRO_SHIFT_DIFF" || !got[0].Amount.Equal(dec("-150")) {
		t.Fatalf("got %+v; want RETRO_SHIFT_DIFF -150 only", got)
	}
	// An increase in one period and an equal decrease in another nets to nothing.
	up := RetroPeriod{Prior: prior[:1], Recalculated: []Line{{Code: "REGULAR", Kind: KindEarning, Amount: dec("4100")}}}
	down := RetroPeriod{Prior: prior[:1], Recalculated: []Line{{Code: "REGULAR", Kind: KindEarning, Amount: dec("3900")}}}
	if got := RetroDelta(up, down); len(got) != 0 {
		t.Fatalf("offsetting deltas produced %+v", got)
	}
}
