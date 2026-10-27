package domain

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Stage of a project (FR-41.1).
type Stage string

const (
	StageCreated   Stage = "CREATED"
	StageInProcess Stage = "IN_PROCESS"
	StageOnHold    Stage = "ON_HOLD"
	StageFinished  Stage = "FINISHED"
)

// ProjectType classifies a project (FR-41.1).
type ProjectType string

const (
	TypeTimeMaterial ProjectType = "TIME_MATERIAL"
	TypeFixedPrice   ProjectType = "FIXED_PRICE"
	TypeInternal     ProjectType = "INTERNAL"
	TypeCost         ProjectType = "COST"
	TypeInvestment   ProjectType = "INVESTMENT"
	TypeTime         ProjectType = "TIME"
)

var (
	projectIDPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9-]{0,19}$`)
	currencyPattern  = regexp.MustCompile(`^[A-Z]{3}$`)
)

// transitions lists the allowed stage moves: CREATED → IN_PROCESS, IN_PROCESS ⇄ ON_HOLD,
// IN_PROCESS → FINISHED. FINISHED is terminal.
var transitions = map[Stage][]Stage{
	StageCreated:   {StageInProcess},
	StageInProcess: {StageOnHold, StageFinished},
	StageOnHold:    {StageInProcess},
}

// Project is the project master as the domain sees it. Dates are YYYY-MM-DD.
type Project struct {
	ID              string      `json:"id"`
	LegalEntityID   string      `json:"legalEntityId"`
	ParentProjectID *string     `json:"parentProjectId,omitempty"`
	ProjectGroupID  string      `json:"projectGroupId"`
	ProjectType     ProjectType `json:"projectType"`
	CustomerPartyID *string     `json:"customerPartyId,omitempty"`
	ContractID      *string     `json:"contractId,omitempty"`
	Currency        string      `json:"currency"`
	Stage           Stage       `json:"stage"`
	StartDate       string      `json:"startDate"`
	EndDate         *string     `json:"endDate,omitempty"`
	Version         int         `json:"version"`
	CreatedAt       time.Time   `json:"createdAt"`
	UpdatedAt       time.Time   `json:"updatedAt"`
}

// ValidProjectType reports whether t is one of the six PRD types.
func ValidProjectType(t ProjectType) bool {
	switch t {
	case TypeTimeMaterial, TypeFixedPrice, TypeInternal, TypeCost, TypeInvestment, TypeTime:
		return true
	}
	return false
}

// ExternalType reports whether projects of t bill a customer and so need one.
// TIME_MATERIAL and FIXED_PRICE are the billable types (an assumption; the PRD
// says only "external types").
func ExternalType(t ProjectType) bool { return t == TypeTimeMaterial || t == TypeFixedPrice }

// ValidateProject returns field-level errors for a complete project (empty when valid).
func ValidateProject(p Project) map[string]string { return validate(p, false) }

// ValidateCreate returns field-level errors for a create request. A sub-project
// (ParentProjectID set) may omit the project group and customer, because it
// inherits them from its parent (FR-41.1); ValidateProject runs again on the
// result of InheritFromParent.
func ValidateCreate(p Project) map[string]string {
	return validate(p, p.ParentProjectID != nil && *p.ParentProjectID != "")
}

func validate(p Project, inheritable bool) map[string]string {
	fields := map[string]string{}
	if !projectIDPattern.MatchString(p.ID) {
		fields["id"] = "must be 1 to 20 characters of A-Z, 0-9 or hyphen"
	}
	if !ValidProjectType(p.ProjectType) {
		fields["projectType"] = "must be TIME_MATERIAL, FIXED_PRICE, INTERNAL, COST, INVESTMENT or TIME"
	}
	if ExternalType(p.ProjectType) && !inheritable && (p.CustomerPartyID == nil || *p.CustomerPartyID == "") {
		fields["customerPartyId"] = "is required for billable project types"
	}
	if !currencyPattern.MatchString(p.Currency) {
		fields["currency"] = "must be an ISO 4217 alpha-3 code in capitals"
	}
	if (p.ProjectGroupID == "" && !inheritable) || len(p.ProjectGroupID) > 10 {
		fields["projectGroupId"] = "must be 1 to 10 characters"
	}
	start, err := time.Parse(time.DateOnly, p.StartDate)
	if err != nil {
		fields["startDate"] = "must be a date (YYYY-MM-DD)"
	}
	if p.EndDate != nil {
		end, endErr := time.Parse(time.DateOnly, *p.EndDate)
		switch {
		case endErr != nil:
			fields["endDate"] = "must be a date (YYYY-MM-DD)"
		case err == nil && end.Before(start):
			fields["endDate"] = "must not be before startDate"
		}
	}
	return fields
}

// InheritFromParent applies the FR-41.1 hierarchy rules to a new sub-project:
// an omitted project group, customer or contract is taken from the parent, a
// sub-project cannot name a customer other than its parent's, and it stays in
// its parent's legal entity. Both violations return PROJ-PARENT-002.
func InheritFromParent(child, parent Project) (Project, error) {
	if child.LegalEntityID != parent.LegalEntityID {
		return Project{}, rule(CodeParentConflict, "Sub-project %s must be in the legal entity of its parent %s", child.ID, parent.ID)
	}
	if parent.CustomerPartyID != nil && child.CustomerPartyID != nil && *child.CustomerPartyID != *parent.CustomerPartyID {
		return Project{}, rule(CodeParentConflict, "Sub-project %s cannot change the customer of its parent %s", child.ID, parent.ID)
	}
	if child.ProjectGroupID == "" {
		child.ProjectGroupID = parent.ProjectGroupID
	}
	if child.CustomerPartyID == nil {
		child.CustomerPartyID = parent.CustomerPartyID
	}
	if child.ContractID == nil {
		child.ContractID = parent.ContractID
	}
	return child, nil
}

// ValidStage reports whether s is a known stage.
func ValidStage(s Stage) bool {
	switch s {
	case StageCreated, StageInProcess, StageOnHold, StageFinished:
		return true
	}
	return false
}

// Transition checks a stage move and returns PROJ-STAGE-001 when it is not allowed.
func Transition(from, to Stage) error {
	for _, next := range transitions[from] {
		if next == to {
			return nil
		}
	}
	return rule(CodeStage, "Project cannot move from %s to %s", from, to)
}

// FinishBlockers are the open items that stop a project reaching FINISHED (BR-41.7).
type FinishBlockers struct {
	OpenJournals     int
	OpenProposals    int
	WIPBalance       string // decimal string; "0" when clear
	FinalCompletedCN bool   // a COMPLETED_CONTRACT estimate was posted for the final period
}

// CanFinish returns PROJ-STAGE-002 listing every reason the project cannot finish.
func CanFinish(b FinishBlockers) error {
	var reasons []string
	if b.OpenJournals > 0 {
		reasons = append(reasons, fmt.Sprintf("%d open journals", b.OpenJournals))
	}
	if b.OpenProposals > 0 {
		reasons = append(reasons, fmt.Sprintf("%d DRAFT or APPROVED proposals", b.OpenProposals))
	}
	if b.WIPBalance != "" && b.WIPBalance != "0" && !b.FinalCompletedCN {
		reasons = append(reasons, "non-zero WIP balance "+b.WIPBalance)
	}
	if len(reasons) == 0 {
		return nil
	}
	return rule(CodeFinish, "cannot be FINISHED: %s", strings.Join(reasons, ", "))
}
