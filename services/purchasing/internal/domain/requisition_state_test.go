package domain_test

import (
	"errors"
	"testing"

	"pgregory.net/rapid"

	"github.com/BenjaminSRussell/passage/services/purchasing/internal/domain"
)

func TestRequisitionTransition(t *testing.T) {
	tests := []struct {
		name         string
		status       string
		action       string
		steps, chain int
		want         string
		wantSteps    int
		wantRefusal  bool
	}{
		{"first of three levels", domain.ReqInReview, domain.ReqActionApprove, 0, 3, domain.ReqInReview, 1, false},
		{"last level approves", domain.ReqInReview, domain.ReqActionApprove, 2, 3, domain.ReqApproved, 3, false},
		{"single level approves", domain.ReqInReview, domain.ReqActionApprove, 0, 1, domain.ReqApproved, 1, false},
		{"nothing left to approve", domain.ReqInReview, domain.ReqActionApprove, 1, 1, "", 0, true},
		{"approve a draft", domain.ReqDraft, domain.ReqActionApprove, 0, 0, "", 0, true},
		{"approve an approved one", domain.ReqApproved, domain.ReqActionApprove, 1, 1, "", 0, true},
		{"reject keeps the levels done", domain.ReqInReview, domain.ReqActionReject, 1, 3, domain.ReqRejected, 1, false},
		{"reject an approved one", domain.ReqApproved, domain.ReqActionReject, 1, 1, "", 0, true},
		{"reopen resets the chain", domain.ReqRejected, domain.ReqActionReopen, 1, 3, domain.ReqDraft, 0, false},
		{"reopen a draft", domain.ReqDraft, domain.ReqActionReopen, 0, 0, "", 0, true},
		{"cancel a draft", domain.ReqDraft, domain.ReqActionCancel, 0, 0, domain.ReqCancelled, 0, false},
		{"cancel a rejected one", domain.ReqRejected, domain.ReqActionCancel, 1, 3, domain.ReqCancelled, 1, false},
		{"cancel an approved one", domain.ReqApproved, domain.ReqActionCancel, 3, 3, domain.ReqCancelled, 3, false},
		{"cancel one in review", domain.ReqInReview, domain.ReqActionCancel, 0, 1, "", 0, true},
		{"cancel a cancelled one", domain.ReqCancelled, domain.ReqActionCancel, 0, 0, "", 0, true},
		{"withdraw before any level approved", domain.ReqInReview, domain.ReqActionWithdraw, 0, 2, domain.ReqDraft, 0, false},
		{"withdraw after a level approved", domain.ReqInReview, domain.ReqActionWithdraw, 1, 2, "", 0, true},
		{"withdraw a draft", domain.ReqDraft, domain.ReqActionWithdraw, 0, 0, "", 0, true},
		{"withdraw an approved one", domain.ReqApproved, domain.ReqActionWithdraw, 1, 1, "", 0, true},
		{"unknown action", domain.ReqDraft, "close", 0, 0, "", 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, steps, err := domain.RequisitionTransition(tc.status, tc.action, tc.steps, tc.chain)
			if tc.wantRefusal {
				if !errors.Is(err, domain.ErrRequisitionState) {
					t.Fatalf("err = %v, want ErrRequisitionState", err)
				}
				return
			}
			if err != nil || got != tc.want || steps != tc.wantSteps {
				t.Fatalf("got %s/%d/%v, want %s/%d", got, steps, err, tc.want, tc.wantSteps)
			}
		})
	}
}

// Approving every level of any chain, one call per level, ends APPROVED after exactly len(chain) calls and never earlier.
func TestRequisitionTransition_ApprovalWalkProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		n := rapid.IntRange(1, 6).Draw(t, "chain")
		status, steps := domain.ReqInReview, 0
		for i := 1; i <= n; i++ {
			var err error
			status, steps, err = domain.RequisitionTransition(status, domain.ReqActionApprove, steps, n)
			if err != nil {
				t.Fatalf("level %d of %d: %v", i, n, err)
			}
			if (status == domain.ReqApproved) != (i == n) {
				t.Fatalf("level %d of %d left status %s", i, n, status)
			}
		}
		if _, _, err := domain.RequisitionTransition(status, domain.ReqActionApprove, steps, n); !errors.Is(err, domain.ErrRequisitionState) {
			t.Fatalf("a further approval must be refused, got %v", err)
		}
	})
}

func TestNextApprover(t *testing.T) {
	chain := []string{domain.RoleManager, domain.RoleCFO}
	for steps, want := range map[int]string{0: domain.RoleManager, 1: domain.RoleCFO, 2: "", -1: ""} {
		if got := domain.NextApprover(chain, steps); got != want {
			t.Errorf("steps %d: %q, want %q", steps, got, want)
		}
	}
}
