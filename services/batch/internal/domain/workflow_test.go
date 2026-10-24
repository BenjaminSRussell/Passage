package domain

import (
	"testing"
	"time"
)

func TestCompletionPolicies(t *testing.T) {
	tests := []struct {
		policy              string
		pct, approved, rejected, total int
		done, rejectedStep  bool
	}{
		{PolicySingle, 0, 1, 0, 3, true, false},
		{PolicySingle, 0, 0, 1, 3, true, true},
		{PolicyAll, 0, 3, 0, 3, true, false},
		{PolicyAll, 0, 2, 0, 3, false, false},
		{PolicyAll, 0, 1, 1, 3, true, true},
		{PolicyMajority, 0, 2, 0, 3, true, false},
		{PolicyMajority, 0, 2, 0, 4, false, false},
		{PolicyMajority, 0, 3, 0, 4, true, false},
		{PolicyPercentage, 60, 3, 0, 5, true, false},
		{PolicyPercentage, 60, 2, 0, 5, false, false},
	}
	for _, tc := range tests {
		done, rejected := Completion(tc.policy, tc.pct, tc.approved, tc.rejected, tc.total)
		if done != tc.done || rejected != tc.rejectedStep {
			t.Fatalf("%s a=%d r=%d t=%d => %v/%v, want %v/%v", tc.policy, tc.approved, tc.rejected, tc.total, done, rejected, tc.done, tc.rejectedStep)
		}
	}
}

func TestRecallAfterApprovalRejected(t *testing.T) {
	inst := Instance{SubmittedBy: "u1", Status: InstanceInReview}
	err := CanRecall(inst, []WorkItem{{Status: ItemApproved}}, "u1")
	if err == nil || err.(*Error).Code != CodeRecallNotAllowed {
		t.Fatalf("err = %v", err)
	}
}

func TestRecallBeforeApproval(t *testing.T) {
	inst := Instance{SubmittedBy: "u1", Status: InstanceSubmitted}
	if err := CanRecall(inst, []WorkItem{{Status: ItemPending}}, "u1"); err != nil {
		t.Fatal(err)
	}
}

func TestSelfApproveBlocked(t *testing.T) {
	_, _, err := ApplyAction(
		Instance{SubmittedBy: "u1", Status: InstanceInReview},
		WorkItem{AssigneeID: "u1", Status: ItemPending},
		Definition{},
		Action{Kind: "approve", Actor: "u1"},
	)
	if err == nil || err.(*Error).Code != CodeSelfApprove {
		t.Fatalf("err = %v", err)
	}
}

func TestDelegateChainLimit(t *testing.T) {
	_, _, err := ApplyAction(
		Instance{SubmittedBy: "u1", Status: InstanceInReview},
		WorkItem{AssigneeID: "u2", Status: ItemPending, DelegationDepth: 3},
		Definition{DelegationMax: 3},
		Action{Kind: "delegate", Actor: "u2", Delegate: "u3"},
	)
	if err == nil || err.(*Error).Code != CodeDelegationLimit {
		t.Fatalf("err = %v", err)
	}
}

func TestThreeLevelChainDelegationAndSLA(t *testing.T) {
	def := Definition{
		AdminUserID: "admin",
		Steps: []Step{
			{Index: 1, AssigneeType: AssigneeRole, Assignee: "manager", SLA: 4 * time.Hour, EscalationAction: EscalateDelegate, EscalationTarget: "admin"},
			{Index: 2, AssigneeType: AssigneeUser, Assignee: "bob", SLA: 4 * time.Hour, EscalationAction: EscalateDelegate, EscalationTarget: "admin"},
			{Index: 3, AssigneeType: AssigneeExpression, Assignee: "record.controller", SLA: 4 * time.Hour, EscalationAction: EscalateDelegate, EscalationTarget: "admin"},
		},
	}
	inst := Instance{SubmittedBy: "req", Status: InstanceSubmitted, CurrentStep: 1}
	items := []WorkItem{{ID: "w1", Step: 1, AssigneeID: "alice", Status: ItemPending}}

	inst, items[0], err := ApplyAction(inst, items[0], def, Action{Kind: "approve", Actor: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	items = append(items, WorkItem{ID: "w2", Step: 2, AssigneeID: "bob", Status: ItemPending})
	inst, items[1], err = ApplyAction(inst, items[1], def, Action{Kind: "delegate", Actor: "bob", Delegate: "dana"})
	if err != nil {
		t.Fatal(err)
	}
	items = append(items, WorkItem{ID: "w3", Step: 2, AssigneeID: "dana", Status: ItemPending, OriginalAssigneeID: "bob", DelegationDepth: 1})
	inst, items[2], err = ApplyAction(inst, items[2], def, Action{Kind: "approve", Actor: "dana"})
	if err != nil {
		t.Fatal(err)
	}
	items = append(items, WorkItem{ID: "w4", Step: 3, AssigneeID: "carol", Status: ItemPending})
	inst, items[3], target := Escalate(inst, items[3], def.Steps[2], def.AdminUserID)
	if target != "admin" || items[3].Status != ItemEscalated {
		t.Fatalf("escalate = %s %+v", target, items[3])
	}
	items = append(items, WorkItem{ID: "w5", Step: 3, AssigneeID: "admin", Status: ItemPending})
	inst, items[4], err = ApplyAction(inst, items[4], def, Action{Kind: "approve", Actor: "admin"})
	if err != nil {
		t.Fatal(err)
	}
	inst = FinishInstance(inst, true, "")
	if inst.Status != InstanceApproved {
		t.Fatalf("status = %s", inst.Status)
	}
	if len(items) != 5 {
		t.Fatalf("timeline items = %d, want 5", len(items))
	}
}

func TestEmptyAssigneeEscalates(t *testing.T) {
	if got := ResolveAssignees(Step{AssigneeType: AssigneeExpression, Assignee: "nobody"}, func(string, string) []string { return nil }); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestProductJobCodes(t *testing.T) {
	got := ProductJobCodes()
	if len(got) != 2 || got[0] != "product.explode" {
		t.Fatalf("%v", got)
	}
}
