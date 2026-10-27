package domain

import (
	"errors"
	"fmt"
)

// Journal batch statuses (FR-11.9, BR-11.10).
const (
	BatchDraft      = "draft"
	BatchValidated  = "validated"
	BatchInApproval = "in_approval"
	BatchApproved   = "approved"
	BatchRejected   = "rejected"
	BatchPosting    = "posting"
	BatchPosted     = "posted"
	BatchCancelled  = "cancelled"
)

// DefaultBatchLinesLimit is the default journal_name.lines_limit (PRD §2.11.13).
const DefaultBatchLinesLimit = 5000

// batchTransitions is the BR-11.10 state machine. Edits return a batch to
// draft from every editable state (see BatchEditable), so that edge is not
// listed here; posted and cancelled are terminal. posting is the lock held
// while the entry is written: the store holds it as the batch row lock inside
// the posting transaction, so a failed post leaves the batch where it was.
var batchTransitions = map[string][]string{
	BatchDraft:      {BatchValidated, BatchCancelled},
	BatchValidated:  {BatchInApproval, BatchPosting, BatchCancelled},
	BatchInApproval: {BatchApproved, BatchRejected},
	BatchApproved:   {BatchPosting},
	BatchRejected:   {BatchCancelled},
	BatchPosting:    {BatchPosted, BatchApproved, BatchValidated},
	BatchPosted:     nil,
	BatchCancelled:  nil,
}

// ErrBatchState is matched by every rejected batch transition.
var ErrBatchState = errors.New("ledger: journal batch transition not allowed")

// ErrSelfApproval is returned when the submitter tries to approve their own batch.
var ErrSelfApproval = errors.New("ledger: the submitter may not approve this batch")

// ErrApprovalRequired is returned when a validated batch is posted although its
// journal name now has an approval workflow.
var ErrApprovalRequired = errors.New("ledger: the journal name requires approval before posting")

// StateError names the rejected transition.
type StateError struct{ From, To string }

func (e *StateError) Error() string {
	return fmt.Sprintf("journal batch cannot move from %s to %s", e.From, e.To)
}

// Unwrap lets callers match ErrBatchState.
func (e *StateError) Unwrap() error { return ErrBatchState }

// CanTransition reports whether the state machine allows from -> to.
func CanTransition(from, to string) bool {
	for _, next := range batchTransitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// CheckTransition returns a *StateError when from -> to is not allowed.
func CheckTransition(from, to string) error {
	if CanTransition(from, to) {
		return nil
	}
	return &StateError{From: from, To: to}
}

// BatchEditable reports whether lines and header may change in status. Any edit
// of a validated, approved or rejected batch returns it to draft (BR-11.10).
func BatchEditable(status string) bool {
	switch status {
	case BatchDraft, BatchValidated, BatchApproved, BatchRejected:
		return true
	}
	return false
}

// SubmitTarget is the status a successfully validated batch moves to: in_approval
// when its journal name has a workflow, otherwise validated (ready to post).
func SubmitTarget(requiresApproval bool) string {
	if requiresApproval {
		return BatchInApproval
	}
	return BatchValidated
}

// CheckSubmit allows submitting a draft, and re-submitting a validated batch
// for approval after its journal name gained a workflow.
func CheckSubmit(status string, requiresApproval bool) error {
	target := SubmitTarget(requiresApproval)
	if status == BatchDraft || (status == BatchValidated && target == BatchInApproval) {
		return nil
	}
	return &StateError{From: status, To: target}
}

// CheckPost allows posting an approved batch, or a validated batch whose
// journal name has no workflow (BR-11.10).
func CheckPost(status string, requiresApproval bool) error {
	if err := CheckTransition(status, BatchPosting); err != nil {
		return err
	}
	if status == BatchValidated && requiresApproval {
		return ErrApprovalRequired
	}
	return nil
}

// CheckApproval enforces approver != submitter unless the journal name allows
// self-approval (§2.11.5).
func CheckApproval(submitter, approver string, selfApprovalAllowed bool) error {
	if !selfApprovalAllowed && submitter != "" && submitter == approver {
		return ErrSelfApproval
	}
	return nil
}
