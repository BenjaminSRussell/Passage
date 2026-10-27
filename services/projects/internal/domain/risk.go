package domain

import (
	"time"
	"unicode/utf8"

	"github.com/BenjaminSRussell/passage/platform/ids"
)

// CodeRiskStatus is raised for a risk or issue status move that is not allowed
// (service-specific; not in §8.41.18).
const CodeRiskStatus = "PROJ-RISK-001"

// RiskKind separates risks (may happen) from issues (happening now) (FR-41.20).
type RiskKind string

const (
	KindRisk  RiskKind = "RISK"
	KindIssue RiskKind = "ISSUE"
)

// RiskStatus is the lifecycle of a risk or issue.
type RiskStatus string

const (
	RiskOpen      RiskStatus = "OPEN"
	RiskMitigated RiskStatus = "MITIGATED"
	RiskClosed    RiskStatus = "CLOSED"
)

// RiskIssue is one entry of a project's risk and issue register. Probability and
// impact are 1 to 5; Score is their product and is derived, never stored.
type RiskIssue struct {
	ID            string     `json:"id"`
	ProjectID     string     `json:"projectId"`
	Kind          RiskKind   `json:"kind"`
	Title         string     `json:"title"`
	Description   string     `json:"description"`
	Probability   int        `json:"probability"`
	Impact        int        `json:"impact"`
	Score         int        `json:"score"`
	OwnerWorkerID *string    `json:"ownerWorkerId,omitempty"`
	Mitigation    string     `json:"mitigation"`
	DueDate       *string    `json:"dueDate,omitempty"`
	Status        RiskStatus `json:"status"`
	Version       int        `json:"version"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
}

// RiskScore is probability × impact (1 to 25).
func RiskScore(probability, impact int) int { return probability * impact }

// ValidateRiskIssue returns field-level errors (empty when valid).
func ValidateRiskIssue(r RiskIssue) map[string]string {
	fields := map[string]string{}
	if r.Kind != KindRisk && r.Kind != KindIssue {
		fields["kind"] = "must be RISK or ISSUE"
	}
	if n := utf8.RuneCountInString(r.Title); n < 1 || n > 120 {
		fields["title"] = "must be 1 to 120 characters"
	}
	if utf8.RuneCountInString(r.Description) > 2000 {
		fields["description"] = "must be at most 2000 characters"
	}
	if r.Probability < 1 || r.Probability > 5 {
		fields["probability"] = "must be 1 to 5"
	}
	if r.Impact < 1 || r.Impact > 5 {
		fields["impact"] = "must be 1 to 5"
	}
	if utf8.RuneCountInString(r.Mitigation) > 2000 {
		fields["mitigation"] = "must be at most 2000 characters"
	}
	if r.OwnerWorkerID != nil && !ids.Valid(*r.OwnerWorkerID) {
		fields["ownerWorkerId"] = "must be a UUID"
	}
	if r.DueDate != nil {
		if _, err := time.Parse(time.DateOnly, *r.DueDate); err != nil {
			fields["dueDate"] = "must be a date (YYYY-MM-DD)"
		}
	}
	if r.Status != "" && !validRiskStatus(r.Status) {
		fields["status"] = "must be OPEN, MITIGATED or CLOSED"
	}
	return fields
}

func validRiskStatus(s RiskStatus) bool {
	switch s {
	case RiskOpen, RiskMitigated, RiskClosed:
		return true
	}
	return false
}

// riskMoveAllowed lists the allowed status moves. CLOSED is terminal; a mitigated
// risk may reopen when the mitigation fails.
func riskMoveAllowed(from, to RiskStatus) bool {
	switch from {
	case RiskOpen:
		return to == RiskMitigated || to == RiskClosed
	case RiskMitigated:
		return to == RiskOpen || to == RiskClosed
	}
	return false
}

// CheckRiskMove returns PROJ-RISK-001 when from → to is not an allowed move.
// A move to the same status is an edit that keeps the status; a CLOSED entry
// is final and cannot be edited.
func CheckRiskMove(from, to RiskStatus) error {
	if from == RiskClosed {
		return rule(CodeRiskStatus, "A CLOSED risk or issue cannot change")
	}
	if from == to || riskMoveAllowed(from, to) {
		return nil
	}
	return rule(CodeRiskStatus, "Risk or issue cannot move from %s to %s", from, to)
}
