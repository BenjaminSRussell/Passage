package domain_test

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

func TestApproveReopen_RequiresTwoDistinctApprovers(t *testing.T) {
	req := domain.ReopenRequest{RequestedBy: "alice", Required: 2, Reason: "audit adjustment after close"}
	if _, _, err := domain.ApproveReopen(req, "alice"); code(err) != domain.CodeInvalidTransition {
		t.Fatalf("requester must not approve: %v", err)
	}
	req, done, err := domain.ApproveReopen(req, "bob")
	if err != nil || done || len(req.Approvals) != 1 {
		t.Fatalf("first approval: done=%v err=%v approvals=%d", done, err, len(req.Approvals))
	}
	if _, _, err := domain.ApproveReopen(req, "bob"); code(err) != domain.CodeInvalidTransition {
		t.Fatalf("duplicate approver: %v", err)
	}
	req, done, err = domain.ApproveReopen(req, "cara")
	if err != nil || !done || len(req.Approvals) != 2 {
		t.Fatalf("second approval: done=%v err=%v approvals=%d", done, err, len(req.Approvals))
	}
}

func TestApproveReopen_RejectsShortReasonAndEmptyActor(t *testing.T) {
	if err := domain.ValidateReopenReason("too-short"); code(err) != domain.CodeValidation {
		t.Fatalf("reason under 10 chars: %v", err)
	}
	if err := domain.ValidateReopenReason("long enough reason"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := domain.ApproveReopen(domain.ReopenRequest{RequestedBy: "alice", Required: 2, Reason: "audit adjustment after close"}, ""); code(err) != domain.CodeValidation {
		t.Fatalf("empty actor: %v", err)
	}
}
