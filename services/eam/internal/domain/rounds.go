package domain

import (
	"fmt"
	"strings"
	"time"
)

// Maintenance rounds (FR-44.14, BR-44.9) and the two reminder rules this slice
// adds next to them: emergency regularization (BR-44.7) and the plan review of
// critical assets (BR-44.15).

// Service codes for rounds; the PRD message catalog names none, so they follow
// the EAM-SEQ pattern of slice 4.
const (
	// CodeRoundIncomplete: a ROUND work order cannot complete while a stop has
	// no outcome (BR-44.9, the rounds analogue of EAM-WO-004).
	CodeRoundIncomplete = "EAM-RND-001"
	// CodeStopOutcome: a stop outcome is not allowed (order not ROUND or not in
	// progress, stop already recorded, skip without a reason).
	CodeStopOutcome = "EAM-RND-002"
	// CodeRoundDefinition: a round definition breaks a rule (stop order, job
	// type code, a job type that needs a permit to work).
	CodeRoundDefinition = "EAM-RND-003"
	// CodeRegularization is the PRD warning for an emergency work order that is
	// still unplanned after the window (BR-44.7).
	CodeRegularization = "EAM-WO-003"
)

// MaxRoundStops bounds one round, as steps bound a sequence.
const MaxRoundStops = 50

// MaxRoundTravelMinutes bounds the walking or driving time to one stop.
const MaxRoundTravelMinutes = 480

// Stop outcomes recorded on a ROUND work order.
const (
	StopDone    = "DONE"
	StopSkipped = "SKIPPED"
)

// ReviewCriticality is the criticality from which an asset's plans need a
// periodic review, and ReviewMonths the review period (BR-44.15).
const (
	ReviewCriticality = 4
	ReviewMonths      = 12
)

// RegularizationWindow is the default time an emergency order may stay
// unplanned (BR-44.7; site parameter 8.44.13, 1-72 h, default 24).
const RegularizationWindow = 24 * time.Hour

// RoundStop is one ordered stop of a round: an asset inspected with one job
// type. TravelMinutes is the walking or driving time from the previous stop.
type RoundStop struct {
	Sequence      int    `json:"sequence"`
	AssetID       string `json:"assetId"`
	JobType       string `json:"jobType"`
	Mandatory     bool   `json:"mandatory"`
	TravelMinutes int    `json:"travelMinutes"`
}

// ValidateRoundStops checks the stops of a round: 1 to 50, numbered 1..n in
// order, each with an asset, a job type code and 0 to 480 travel minutes.
func ValidateRoundStops(stops []RoundStop) error {
	if len(stops) == 0 || len(stops) > MaxRoundStops {
		return GateError{Code: CodeRoundDefinition, Detail: fmt.Sprintf("a round needs 1 to %d stops", MaxRoundStops)}
	}
	for i, s := range stops {
		switch {
		case s.Sequence != i+1:
			return GateError{Code: CodeRoundDefinition, Detail: fmt.Sprintf("stops must be numbered 1 to %d in order", len(stops))}
		case s.AssetID == "":
			return GateError{Code: CodeRoundDefinition, Detail: fmt.Sprintf("stop %d needs an asset", s.Sequence)}
		case !jobTypeRe.MatchString(s.JobType):
			return GateError{Code: CodeRoundDefinition, Detail: fmt.Sprintf("stop %d needs a job type code like INSPECT", s.Sequence)}
		case s.TravelMinutes < 0 || s.TravelMinutes > MaxRoundTravelMinutes:
			return GateError{Code: CodeRoundDefinition, Detail: fmt.Sprintf("stop %d travel must be 0 to %d minutes", s.Sequence, MaxRoundTravelMinutes)}
		}
	}
	return nil
}

// RoundTravelMinutes sums the travel between stops.
func RoundTravelMinutes(stops []RoundStop) int {
	total := 0
	for _, s := range stops {
		total += s.TravelMinutes
	}
	return total
}

// NormalizeStopReason trims a stop reason and checks it against the outcome: a
// SKIPPED stop needs a reason of 1 to 120 characters (BR-44.9); a DONE stop
// keeps an optional remark of the same length.
func NormalizeStopReason(outcome, reason string) (string, error) {
	r := strings.TrimSpace(reason)
	switch outcome {
	case StopDone, StopSkipped:
	default:
		return "", GateError{Code: CodeStopOutcome, Detail: "outcome must be DONE or SKIPPED"}
	}
	if outcome == StopSkipped && r == "" {
		return "", GateError{Code: CodeStopOutcome, Detail: "a skipped stop needs a reason"}
	}
	if len([]rune(r)) > 120 {
		return "", GateError{Code: CodeStopOutcome, Detail: "the reason must be at most 120 characters"}
	}
	return r, nil
}

// FollowUpRequired reports whether a stop outcome raises a maintenance request:
// a skipped mandatory stop does (BR-44.9).
func FollowUpRequired(mandatory bool, outcome string) bool {
	return mandatory && outcome == StopSkipped
}

// RegularizationOverdue reports whether an emergency work order still needs
// regularizing (BR-44.7). Priority 1 orders may skip SCHEDULED and start before
// RELEASED; the order is regularized once it has been scheduled or released.
// It is overdue when it is still open, was never scheduled or released, and was
// created at least window ago.
func RegularizationOverdue(priority int, status Status, planned bool, createdAt, now time.Time, window time.Duration) bool {
	if priority != 1 || planned || !status.Open() {
		return false
	}
	return !now.Before(createdAt.Add(window))
}

// ReviewDue reports whether an asset at or above ReviewCriticality needs its
// plans reviewed (BR-44.15): it has never been reviewed, or the last review is
// ReviewMonths old.
func ReviewDue(criticality int, lastReviewed *time.Time, now time.Time) bool {
	if criticality < ReviewCriticality {
		return false
	}
	if lastReviewed == nil {
		return true
	}
	return !now.Before(lastReviewed.AddDate(0, ReviewMonths, 0))
}
