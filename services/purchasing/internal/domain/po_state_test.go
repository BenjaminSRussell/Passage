package domain

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestNextApproval_TableDriven(t *testing.T) {
	tests := []struct {
		name    string
		current string
		action  string
		want    string
		code    string
	}{
		{"submit draft", POStatusDraft, POActionSubmit, POStatusInReview, ""},
		{"approve in review", POStatusInReview, POActionApprove, POStatusApproved, ""},
		{"confirm approved", POStatusApproved, POActionConfirm, POStatusConfirmed, ""},
		{"reject in review", POStatusInReview, POActionReject, POStatusRejected, ""},
		{"resubmit rejected", POStatusRejected, POActionSubmit, POStatusInReview, ""},
		{"submit confirmed is refused", POStatusConfirmed, POActionSubmit, "", CodePOState},
		{"confirm draft is PRC-PO-008", POStatusDraft, POActionConfirm, "", CodeConfirmPrecondition},
		{"approve draft is refused", POStatusDraft, POActionApprove, "", CodePOState},
		{"reject approved is refused", POStatusApproved, POActionReject, "", CodePOState},
		{"confirm in review is PRC-PO-008", POStatusInReview, POActionConfirm, "", CodeConfirmPrecondition},
		{"confirm confirmed is PRC-PO-008", POStatusConfirmed, POActionConfirm, "", CodeConfirmPrecondition},
		{"cancelled cannot be submitted", POStatusCancelled, POActionSubmit, "", CodePOState},
		{"unknown action", POStatusDraft, "explode", "", CodePOState},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NextApproval(tc.current, tc.action)
			if tc.code != "" {
				assertPOCode(t, err, tc.code)
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
		})
	}
}

func TestCanCancel_TableDriven(t *testing.T) {
	zero := decimal.Zero
	one := decimal.NewFromInt(1)
	tests := []struct {
		name     string
		approval string
		received decimal.Decimal
		invoiced decimal.Decimal
		code     string
	}{
		{"draft with nothing received", POStatusDraft, zero, zero, ""},
		{"confirmed with nothing received", POStatusConfirmed, zero, zero, ""},
		{"rejected with nothing received", POStatusRejected, zero, zero, ""},
		{"confirmed with receipt", POStatusConfirmed, one, zero, CodePOState},
		{"confirmed with invoice", POStatusConfirmed, zero, one, CodePOState},
		{"finalized", POStatusFinalized, zero, zero, CodePOState},
		{"already cancelled", POStatusCancelled, zero, zero, CodePOState},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CanCancel(tc.approval, tc.received, tc.invoiced)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			assertPOCode(t, err, tc.code)
		})
	}
}

func TestCheckReduction_TableDriven(t *testing.T) {
	d := decimal.NewFromInt
	tests := []struct {
		name     string
		received int64
		invoiced int64
		newQty   int64
		allow    bool
		code     string
	}{
		{"no receipts, any qty", 0, 0, 1, false, ""},
		{"qty equal to received", 50, 0, 50, false, ""},
		{"qty below received", 50, 0, 49, false, CodeReceivedFloor},
		{"removed line with receipt", 5, 0, 0, false, CodeReceivedFloor},
		{"below invoiced without flag", 0, 30, 20, false, CodeReceivedFloor},
		{"below invoiced with flag", 0, 30, 20, true, ""},
		{"below invoiced but above received, no flag", 10, 30, 20, false, CodeReceivedFloor},
		{"below received even with flag", 50, 0, 40, true, CodeReceivedFloor},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckReduction(1, d(tc.received), d(tc.invoiced), d(tc.newQty), tc.allow)
			if tc.code == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			assertPOCode(t, err, tc.code)
		})
	}
}

func assertPOCode(t *testing.T, err error, code string) {
	t.Helper()
	var de *Error
	if !errors.As(err, &de) {
		t.Fatalf("want domain error %s, got %v", code, err)
	}
	if de.Code != code {
		t.Fatalf("code %s, want %s", de.Code, code)
	}
}
