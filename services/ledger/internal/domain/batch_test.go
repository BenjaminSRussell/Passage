package domain

import (
	"errors"
	"testing"
)

var allBatchStatuses = []string{BatchDraft, BatchValidated, BatchInApproval, BatchApproved, BatchRejected,
	BatchPosting, BatchPosted, BatchCancelled}

func TestCheckTransition_BR11_10(t *testing.T) {
	tests := []struct {
		name     string
		from, to string
		allowed  bool
	}{
		{"draft to validated", BatchDraft, BatchValidated, true},
		{"draft to cancelled", BatchDraft, BatchCancelled, true},
		{"validated to in approval", BatchValidated, BatchInApproval, true},
		{"validated to posting without workflow", BatchValidated, BatchPosting, true},
		{"validated to cancelled", BatchValidated, BatchCancelled, true},
		{"in approval to approved", BatchInApproval, BatchApproved, true},
		{"in approval to rejected", BatchInApproval, BatchRejected, true},
		{"approved to posting", BatchApproved, BatchPosting, true},
		{"posting to posted", BatchPosting, BatchPosted, true},
		{"posting failure back to approved", BatchPosting, BatchApproved, true},
		{"rejected to cancelled", BatchRejected, BatchCancelled, true},
		{"draft skips validation into approval", BatchDraft, BatchInApproval, false},
		{"draft cannot post directly", BatchDraft, BatchPosting, false},
		{"draft cannot be approved", BatchDraft, BatchApproved, false},
		{"in approval cannot post", BatchInApproval, BatchPosting, false},
		{"in approval cannot be cancelled", BatchInApproval, BatchCancelled, false},
		{"approved cannot be cancelled", BatchApproved, BatchCancelled, false},
		{"posted is terminal", BatchPosted, BatchCancelled, false},
		{"posted cannot repost", BatchPosted, BatchPosting, false},
		{"cancelled is terminal", BatchCancelled, BatchDraft, false},
		{"unknown status has no exits", "parked", BatchValidated, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckTransition(tt.from, tt.to)
			if tt.allowed && err != nil {
				t.Fatalf("want allowed, got %v", err)
			}
			if !tt.allowed {
				var se *StateError
				if !errors.Is(err, ErrBatchState) || !errors.As(err, &se) || se.From != tt.from || se.To != tt.to {
					t.Fatalf("want a StateError %s -> %s, got %v", tt.from, tt.to, err)
				}
			}
		})
	}
}

func TestTerminalStatesHaveNoExits(t *testing.T) {
	for _, from := range []string{BatchPosted, BatchCancelled} {
		for _, to := range allBatchStatuses {
			if CanTransition(from, to) {
				t.Errorf("terminal %s allows %s", from, to)
			}
		}
	}
}

func TestBatchEditable(t *testing.T) {
	want := map[string]bool{BatchDraft: true, BatchValidated: true, BatchApproved: true, BatchRejected: true,
		BatchInApproval: false, BatchPosting: false, BatchPosted: false, BatchCancelled: false}
	for _, status := range allBatchStatuses {
		if got := BatchEditable(status); got != want[status] {
			t.Errorf("BatchEditable(%s) = %v, want %v", status, got, want[status])
		}
	}
}

func TestCheckSubmit(t *testing.T) {
	tests := []struct {
		status   string
		approval bool
		ok       bool
	}{
		{BatchDraft, false, true},
		{BatchDraft, true, true},
		// A validated batch is re-submitted only when its journal name gained a workflow.
		{BatchValidated, true, true},
		{BatchValidated, false, false},
		{BatchInApproval, true, false},
		{BatchApproved, true, false},
		{BatchRejected, true, false},
		{BatchPosted, false, false},
	}
	for _, tt := range tests {
		err := CheckSubmit(tt.status, tt.approval)
		if (err == nil) != tt.ok {
			t.Errorf("CheckSubmit(%s, approval=%v) = %v, want ok=%v", tt.status, tt.approval, err, tt.ok)
		}
		if err != nil && !errors.Is(err, ErrBatchState) {
			t.Errorf("CheckSubmit(%s) error %v is not ErrBatchState", tt.status, err)
		}
	}
	if SubmitTarget(true) != BatchInApproval || SubmitTarget(false) != BatchValidated {
		t.Fatal("submit target must follow the journal name's workflow flag")
	}
}

func TestCheckPost(t *testing.T) {
	tests := []struct {
		status   string
		approval bool
		want     error
	}{
		{BatchApproved, true, nil},
		{BatchApproved, false, nil},
		{BatchValidated, false, nil},
		{BatchValidated, true, ErrApprovalRequired},
		{BatchDraft, false, ErrBatchState},
		{BatchInApproval, true, ErrBatchState},
		{BatchRejected, true, ErrBatchState},
		{BatchPosted, false, ErrBatchState},
		{BatchCancelled, false, ErrBatchState},
	}
	for _, tt := range tests {
		err := CheckPost(tt.status, tt.approval)
		if (tt.want == nil) != (err == nil) || (tt.want != nil && !errors.Is(err, tt.want)) {
			t.Errorf("CheckPost(%s, approval=%v) = %v, want %v", tt.status, tt.approval, err, tt.want)
		}
	}
}

func TestCheckApproval(t *testing.T) {
	tests := []struct {
		name            string
		submitter, appr string
		selfAllowed     bool
		wantErr         bool
	}{
		{"different approver", "u1", "u2", false, false},
		{"self approval refused", "u1", "u1", false, true},
		{"self approval allowed by journal name", "u1", "u1", true, false},
		{"unknown submitter does not block", "", "u1", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CheckApproval(tt.submitter, tt.appr, tt.selfAllowed)
			if (err != nil) != tt.wantErr || (err != nil && !errors.Is(err, ErrSelfApproval)) {
				t.Fatalf("CheckApproval err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
