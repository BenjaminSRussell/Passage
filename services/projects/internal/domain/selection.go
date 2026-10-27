package domain

import "time"

// Exclusion reasons: the "why not billed" explanation for one transaction (BR-41.3).
const (
	ReasonNotChargeable = "NOT_CHARGEABLE"
	ReasonProposed      = "ALREADY_PROPOSED"
	ReasonOutOfRange    = "OUT_OF_RANGE"
	ReasonStage         = "PROJECT_STAGE"
	ReasonTxnType       = "TXN_TYPE"
	ReasonProject       = "PROJECT_NOT_IN_SCOPE"
)

const lineChargeable = "CHARGEABLE"

// Candidate is a project transaction as the selection sees it. Dates are
// compared at UTC midnight.
type Candidate struct {
	ID           string
	ProjectID    string
	TxnType      string
	LineProperty string
	ProjectStage Stage
	TxnDate      time.Time
	Proposed     bool // proposal_id is set
}

// Criteria is the billing rule's selection window (BR-41.3). Empty TxnTypes or
// ProjectIDs mean "all".
type Criteria struct {
	From       time.Time
	To         time.Time
	TxnTypes   []string
	ProjectIDs []string
}

// Explain returns the first reason c is not selected, or "" when it is selected.
// A transaction qualifies when it is CHARGEABLE, not yet proposed, dated within
// [From, To], on a project in IN_PROCESS or FINISHED, and of a listed type and
// project.
func Explain(c Candidate, crit Criteria) string {
	switch {
	case c.LineProperty != lineChargeable:
		return ReasonNotChargeable
	case c.Proposed:
		return ReasonProposed
	case c.TxnDate.Before(crit.From) || c.TxnDate.After(crit.To):
		return ReasonOutOfRange
	case c.ProjectStage != StageInProcess && c.ProjectStage != StageFinished:
		return ReasonStage
	case len(crit.TxnTypes) > 0 && !contains(crit.TxnTypes, c.TxnType):
		return ReasonTxnType
	case len(crit.ProjectIDs) > 0 && !contains(crit.ProjectIDs, c.ProjectID):
		return ReasonProject
	}
	return ""
}

// Select returns the IDs of the qualifying candidates in input order and the
// reason for every excluded one. Every candidate lands in exactly one of the two.
func Select(crit Criteria, cands []Candidate) (selected []string, excluded map[string]string) {
	excluded = map[string]string{}
	for _, c := range cands {
		if reason := Explain(c, crit); reason != "" {
			excluded[c.ID] = reason
			continue
		}
		selected = append(selected, c.ID)
	}
	return selected, excluded
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
