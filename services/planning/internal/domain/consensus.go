package domain

import (
	"errors"
	"slices"
)

// Forecast consensus (FR-22.9 notes, 4.22.17). A forecast model is a draft
// while planners write cells, submitted while it waits for approval, and
// published once a supply chain manager approves it. Submitted and published
// models lock their cells. An approver may return a submitted model to draft
// (reject); a published model stays locked.
const (
	ConsensusDraft     = "draft"
	ConsensusSubmitted = "submitted"
	ConsensusPublished = "published"
)

var (
	// ErrConsensusState is a transition the model's status does not allow.
	ErrConsensusState = errors.New("forecast consensus: transition not allowed from the model's status")
	// ErrConsensusSoD is an approval or rejection by someone who maintained the
	// same version (4.22.17: Approve forecast consensus conflicts with
	// Maintain forecasts on the same version), or by no identified user.
	ErrConsensusSoD = errors.New("forecast consensus: the approver submitted or edited this version")
	// ErrConsensusEmpty is a submit of a model without cells.
	ErrConsensusEmpty = errors.New("forecast consensus: the model has no cells")
)

// ConsensusWritable reports whether forecast cells may change in the status.
func ConsensusWritable(status string) bool { return status == ConsensusDraft }

// SubmitConsensus returns the status after a planner submits a model with
// cells cells for approval.
func SubmitConsensus(status, user string, cells int) (string, error) {
	if user == "" {
		return "", ErrConsensusSoD
	}
	if status != ConsensusDraft {
		return "", ErrConsensusState
	}
	if cells == 0 {
		return "", ErrConsensusEmpty
	}
	return ConsensusSubmitted, nil
}

// DecideConsensus returns the status after approver approves (publish) or
// rejects a submitted model. The approver must be identified and must be
// neither the submitter nor a writer of any cell of the version (editors).
func DecideConsensus(status, submitter string, editors []string, approver string, approve bool) (string, error) {
	if status != ConsensusSubmitted {
		return "", ErrConsensusState
	}
	if approver == "" || approver == submitter || slices.Contains(editors, approver) {
		return "", ErrConsensusSoD
	}
	if approve {
		return ConsensusPublished, nil
	}
	return ConsensusDraft, nil
}
