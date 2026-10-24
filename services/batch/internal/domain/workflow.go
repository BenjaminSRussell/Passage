package domain

import "time"

const (
	InstanceSubmitted       = "submitted"
	InstanceInReview        = "in_review"
	InstanceChangeRequested = "change_requested"
	InstanceApproved        = "approved"
	InstanceRejected        = "rejected"
	InstanceRecalled        = "recalled"
	InstanceCanceled        = "canceled"
	InstanceFailed          = "failed"

	ItemPending         = "pending"
	ItemClaimed         = "claimed"
	ItemApproved        = "approved"
	ItemRejected        = "rejected"
	ItemDelegated       = "delegated"
	ItemEscalated       = "escalated"
	ItemChangeRequested = "change_requested"
	ItemCanceled        = "canceled"
	ItemExpired         = "expired"

	PolicySingle     = "single"
	PolicyAll        = "all"
	PolicyMajority   = "majority"
	PolicyPercentage = "percentage"

	EscalateDelegate    = "delegate_to"
	EscalateAutoApprove = "auto_approve"
	EscalateAutoReject  = "auto_reject"
	EscalateNotify      = "notify_only"

	AssigneeUser       = "user"
	AssigneeRole       = "role"
	AssigneeExpression = "expression"
	DefaultDelegation  = 3
	BulkApproveLimit   = 100
)

// Step is one approval level on a definition.
type Step struct {
	Index            int           `json:"index"`
	AssigneeType     string        `json:"assigneeType"`
	Assignee         string        `json:"assignee"`
	EscalationAction string        `json:"escalationAction"`
	EscalationTarget string        `json:"escalationTarget"`
	SLA              time.Duration `json:"slaMs"`
	CompletionPolicy string        `json:"completionPolicy"`
	Percentage       int           `json:"percentage"`
}

// Definition is a versioned, configuration-driven approval chain.
type Definition struct {
	ID              string
	Entity          string
	LegalEntityID   string
	Version         int
	CommentRequired bool
	AdminUserID     string
	DelegationMax   int
	Steps           []Step
}

// Instance is a submitted document under approval.
type Instance struct {
	ID                 string
	DefinitionID       string
	DefinitionVersion  int
	Entity             string
	RecordID           string
	SubmittedBy        string
	Status             string
	CurrentStep        int
	OutcomeReason      string
}

// WorkItem is one assignee's task on a step.
type WorkItem struct {
	ID                  string
	InstanceID          string
	Step                int
	AssigneeID          string
	OriginalAssigneeID  string
	Status              string
	Comment             string
	DelegationDepth     int
}

// Action is an inbox or instance command.
type Action struct {
	Kind     string
	Actor    string
	Comment  string
	Delegate string
}

// ResolveAssignees maps a step onto user ids. An empty set means escalate.
func ResolveAssignees(step Step, resolve func(assigneeType, assignee string) []string) []string {
	if resolve == nil {
		if step.AssigneeType == AssigneeUser && step.Assignee != "" {
			return []string{step.Assignee}
		}
		return nil
	}
	return resolve(step.AssigneeType, step.Assignee)
}

// Completion reports whether a step is finished and whether it was rejected.
func Completion(policy string, percentage, approved, rejected, total int) (done, rejectedStep bool) {
	if total <= 0 {
		return true, false
	}
	switch policy {
	case PolicyAll:
		if rejected > 0 {
			return true, true
		}
		return approved == total, false
	case PolicyMajority:
		need := (total / 2) + 1
		if rejected > total-need {
			return true, true
		}
		return approved >= need, false
	case PolicyPercentage:
		if percentage <= 0 {
			percentage = 100
		}
		need := (total*percentage + 99) / 100
		if need < 1 {
			need = 1
		}
		if rejected > total-need {
			return true, true
		}
		return approved >= need, false
	default: // single
		if rejected > 0 {
			return true, true
		}
		return approved >= 1, false
	}
}

// CanRecall is allowed only before any approval (AC-4.6, BR-4.1).
func CanRecall(inst Instance, items []WorkItem, actor string) error {
	if inst.SubmittedBy != actor {
		return errf(CodeInvalid, "only the submitter can recall")
	}
	switch inst.Status {
	case InstanceSubmitted, InstanceInReview:
	default:
		return errf(CodeRecallNotAllowed, "recall is not allowed in status "+inst.Status)
	}
	for _, item := range items {
		if item.Status == ItemApproved {
			return errf(CodeRecallNotAllowed, "recall is not allowed after a step was approved")
		}
	}
	return nil
}

// CanAct checks work-item action rules before the store applies them.
func CanAct(inst Instance, item WorkItem, def Definition, act Action) error {
	if inst.SubmittedBy == act.Actor && (act.Kind == "approve" || act.Kind == "reject") {
		return errf(CodeSelfApprove, "you cannot approve a document you submitted")
	}
	if item.AssigneeID != act.Actor {
		return errf(CodeInvalid, "only the assignee can act")
	}
	switch item.Status {
	case ItemPending, ItemClaimed:
	default:
		return errf(CodeInvalid, "work item is not open")
	}
	if def.CommentRequired && (act.Kind == "approve" || act.Kind == "reject" || act.Kind == "request_change") && act.Comment == "" {
		return errf(CodeCommentRequired, "a comment is required")
	}
	if act.Kind == "delegate" {
		if act.Delegate == "" {
			return errf(CodeInvalid, "delegate target is required")
		}
		max := def.DelegationMax
		if max == 0 {
			max = DefaultDelegation
		}
		if item.DelegationDepth >= max {
			return errf(CodeDelegationLimit, "delegation chain limit reached")
		}
	}
	return nil
}

// ApplyAction returns the updated item and instance after a human action.
func ApplyAction(inst Instance, item WorkItem, def Definition, act Action) (Instance, WorkItem, error) {
	if err := CanAct(inst, item, def, act); err != nil {
		return inst, item, err
	}
	switch act.Kind {
	case "approve":
		item.Status = ItemApproved
		item.Comment = act.Comment
		inst.Status = InstanceInReview
	case "reject":
		item.Status = ItemRejected
		item.Comment = act.Comment
		inst.Status = InstanceRejected
		inst.OutcomeReason = act.Comment
	case "request_change":
		item.Status = ItemChangeRequested
		item.Comment = act.Comment
		inst.Status = InstanceChangeRequested
		inst.OutcomeReason = act.Comment
	case "delegate":
		item.Status = ItemDelegated
		item.Comment = act.Comment
		if item.OriginalAssigneeID == "" {
			item.OriginalAssigneeID = item.AssigneeID
		}
	default:
		return inst, item, errf(CodeInvalid, "unknown action "+act.Kind)
	}
	return inst, item, nil
}

// Escalate applies the step SLA action. Timers live in Temporal; this is the
// deterministic effect used by the worker activity and by clock-advance tests.
func Escalate(inst Instance, item WorkItem, step Step, admin string) (Instance, WorkItem, string) {
	item.Status = ItemEscalated
	target := step.EscalationTarget
	if target == "" {
		target = admin
	}
	switch step.EscalationAction {
	case EscalateAutoApprove:
		item.Status = ItemApproved
		inst.Status = InstanceInReview
	case EscalateAutoReject:
		item.Status = ItemExpired
		inst.Status = InstanceRejected
		inst.OutcomeReason = "SLA auto_reject"
	default:
		inst.Status = InstanceInReview
	}
	return inst, item, target
}

// FinishInstance marks a terminal approved outcome.
func FinishInstance(inst Instance, approved bool, reason string) Instance {
	if approved {
		inst.Status = InstanceApproved
	} else {
		inst.Status = InstanceRejected
	}
	inst.OutcomeReason = reason
	return inst
}

// ProductJobCodes are the Wave 08 explode/group-change handlers this engine
// is ready to host. The product service still runs them in-process.
func ProductJobCodes() []string {
	return []string{"product.explode", "product.group-change"}
}

// MetadataJobCodes are Wave 03 collectors this engine can schedule.
// The metadata service still invokes them in-process until it enqueues here.
func MetadataJobCodes() []string {
	return []string{"metadata.usage"}
}
