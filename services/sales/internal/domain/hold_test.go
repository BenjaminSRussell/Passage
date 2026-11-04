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

func TestCanRejectHold(t *testing.T) {
	tests := []struct {
		holdType, holdStatus, orderStatus string
		want                              bool
	}{
		{HoldCredit, HoldActive, OrderOpen, true},
		{HoldCredit, HoldActive, OrderDraft, true},
		{HoldCredit, HoldActive, OrderConfirmed, false},
		{HoldCredit, HoldReleased, OrderOpen, false},
		{HoldCredit, HoldRejected, OrderOpen, false},
		{HoldManual, HoldActive, OrderOpen, false},
		{HoldFraud, HoldActive, OrderOpen, false},
	}
	for _, tc := range tests {
		if got := CanRejectHold(tc.holdType, tc.holdStatus, tc.orderStatus); got != tc.want {
			t.Errorf("%+v: got %v", tc, got)
		}
	}
}
