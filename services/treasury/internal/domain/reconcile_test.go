package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestReconciliationState_TransitionsFollowBR363(t *testing.T) {
	states := []ReconciliationState{ReconOpen, ReconInProgress, ReconClosed, ReconReopened}
	allowed := map[[2]ReconciliationState]bool{
		{ReconOpen, ReconInProgress}:   true,
		{ReconInProgress, ReconClosed}: true,
		{ReconClosed, ReconReopened}:   true,
		{ReconReopened, ReconClosed}:   true,
	}
	for _, from := range states {
		for _, to := range states {
			if got := from.CanTransition(to); got != allowed[[2]ReconciliationState{from, to}] {
				t.Errorf("%s -> %s = %v, want %v", from, to, got, !got)
			}
		}
	}
	if ReconciliationState("unknown").CanTransition(ReconClosed) {
		t.Error("an unknown state must not transition")
	}
}

func TestCheckClose_AC364(t *testing.T) {
	d := decimal.RequireFromString
	if err := CheckClose(ReconInProgress, d("1000.00"), d("900.00"), d("100.00")); err != nil {
		t.Fatalf("zero difference should close: %v", err)
	}
	if err := CheckClose(ReconReopened, d("-50.00"), d("-80.00"), d("30.00")); err != nil {
		t.Fatalf("a reopened reconciliation closes again when it agrees: %v", err)
	}
	// AC-36.4: a difference of 0.01 is refused and the difference is shown.
	err := CheckClose(ReconInProgress, d("1000.01"), d("900.00"), d("100.00"))
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeReconcileDifference || de.Detail != "reconciliation cannot close: difference 0.01" {
		t.Fatalf("0.01 difference: got %v", err)
	}
	// A sub-cent difference is not rounded away.
	err = CheckClose(ReconInProgress, d("1000.001"), d("1000"), d("0"))
	if !errors.As(err, &de) || de.Detail != "reconciliation cannot close: difference 0.001" {
		t.Fatalf("0.001 difference: got %v", err)
	}
	for _, s := range []ReconciliationState{ReconOpen, ReconClosed} {
		if got := errCode(CheckClose(s, d("1"), d("1"), d("0"))); got != CodeReconcileState {
			t.Fatalf("close from %s: code %q, want %s", s, got, CodeReconcileState)
		}
	}
}
