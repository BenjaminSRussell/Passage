package domain

import "testing"

func TestCanApplyHold(t *testing.T) {
	for holdType, want := range map[string]bool{
		HoldFraud: true, HoldManual: true, HoldPriceReview: true,
		HoldCredit: false, "": false, "manual": false, "COMPLIANCE": false,
	} {
		if got := CanApplyHold(holdType); got != want {
			t.Errorf("CanApplyHold(%q) = %v, want %v", holdType, got, want)
		}
	}
}
