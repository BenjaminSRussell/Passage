package domain

import (
	"fmt"
	"regexp"
)

// Procedure lifecycle (FR-26.1): DRAFT → ACTIVE → SUPERSEDED. Activating a version
// supersedes the ACTIVE version of the same legal entity and name. ACTIVE and SUPERSEDED
// versions are immutable; the remedy is a new version (PRC-0008).
const (
	ProcedureDraft      = "DRAFT"
	ProcedureActive     = "ACTIVE"
	ProcedureSuperseded = "SUPERSEDED"
)

// Problem codes for procedures (message catalog §5.26.18).
const (
	// CodeProcedureImmutable: "Procedure version {v} is active and immutable".
	CodeProcedureImmutable = "PRC-0008"
	// CodeProcedureNotFound is raised for an unknown procedure id.
	CodeProcedureNotFound = "PRC-0042"
	// CodeProcedureNotActive: a named or assigned procedure has no ACTIVE version.
	CodeProcedureNotActive = "PRC-0063"
)

// maxSalesChannel bounds a sales channel key (procedure determination, §5.26.13).
const maxSalesChannel = 32

var salesChannelPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_\-]*$`)

// ValidateSalesChannel returns a field error for a sales channel, or "". The empty channel
// is valid: it is the legal entity's default.
func ValidateSalesChannel(c string) string {
	if c == "" || (len(c) <= maxSalesChannel && salesChannelPattern.MatchString(c)) {
		return ""
	}
	return fmt.Sprintf("must be empty or 1 to %d upper-case letters, digits, '_' or '-', such as WEB", maxSalesChannel)
}

// Procedure step bounds (FR-26.1: steps 10–990, requirement ≤ 512 characters) and size limits.
const (
	MinProcedureStep     = 10
	MaxProcedureStep     = 990
	MaxRequirementLength = 512
	MaxProcedureSteps    = 99
	maxProcedureName     = 64
	maxGroupLength       = 64
	maxAccountKeyLength  = 32
)

// Exclusion modes (FR-26.8): BEST keeps the lowest net among the group's steps, FIRST the lowest step.
const (
	ExclusionBest  = "BEST"
	ExclusionFirst = "FIRST"
)

// Condition types a procedure may sequence beyond the ones the engine prices today
// (FR-26.1, FR-26.22). Records of these types cannot be created yet.
const (
	TypeFreight       ConditionType = "FREIGHT"
	TypeRebateAccrual ConditionType = "REBATE_ACCRUAL"
	TypeRounding      ConditionType = "ROUNDING"
	TypeFreeGoods     ConditionType = "FREE_GOODS"
)

// procedureNamePattern keeps names usable as reference keys (AX_STANDARD, SAP_RVAA01).
var procedureNamePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_\-]*$`)

// ProcedureStep is one step of a pricing procedure (data model pricing_procedure.steps[]):
// a condition type evaluated in StepNo order, an optional CEL requirement, an optional
// exclusion group with its mode, the statistical flag (computed, not added to the net)
// and the account key that names the revenue or expense account of the step.
type ProcedureStep struct {
	StepNo         int           `json:"stepNo"`
	ConditionType  ConditionType `json:"conditionType"`
	RequirementCEL string        `json:"requirementCel"`
	ExclusionGroup string        `json:"exclusionGroup"`
	ExclusionMode  string        `json:"exclusionMode"`
	Statistical    bool          `json:"statistical"`
	AccountKey     string        `json:"accountKey"`
}

// Procedure is one version of a pricing procedure for a legal entity (FR-26.1).
// Revision is the optimistic version of a DRAFT's steps.
type Procedure struct {
	ID            string
	LegalEntityID string
	Name          string
	Version       int
	Status        string
	Revision      int
	Steps         []ProcedureStep
}

// ValidStepType reports whether t may appear in a procedure.
func ValidStepType(t ConditionType) bool {
	switch t {
	case TypePrice, TypeLineDisc, TypeMultiline, TypeTotalDisc, TypeSurcharge,
		TypeFreight, TypeRebateAccrual, TypeRounding, TypeFreeGoods:
		return true
	}
	return false
}

// ValidateProcedureName returns a field error for a procedure name, or "".
func ValidateProcedureName(name string) string {
	if len(name) < 1 || len(name) > maxProcedureName || !procedureNamePattern.MatchString(name) {
		return fmt.Sprintf("must be 1 to %d upper-case letters, digits, '_' or '-', such as AX_STANDARD", maxProcedureName)
	}
	return ""
}

// ValidateSteps returns field errors for a procedure's steps; an empty map means valid.
// Steps are strictly increasing within 10–990 and include exactly one PRICE step. A condition
// type appears at most once: records are keyed by type, so a repeated type would apply the
// same record twice. Every step of one exclusion group uses the same mode.
func ValidateSteps(steps []ProcedureStep) map[string]string {
	fields := map[string]string{}
	switch {
	case len(steps) == 0:
		fields["steps"] = "at least one step is required"
		return fields
	case len(steps) > MaxProcedureSteps:
		fields["steps"] = fmt.Sprintf("must not exceed %d steps", MaxProcedureSteps)
		return fields
	}
	prev, prices := 0, 0
	modes := map[string]string{}
	typeAt := map[ConditionType]int{}
	for i, s := range steps {
		key := fmt.Sprintf("steps[%d]", i)
		switch {
		case s.StepNo < MinProcedureStep || s.StepNo > MaxProcedureStep:
			fields[key+".stepNo"] = fmt.Sprintf("must be between %d and %d", MinProcedureStep, MaxProcedureStep)
		case s.StepNo <= prev:
			fields[key+".stepNo"] = "must be greater than the previous step"
		}
		prev = s.StepNo
		if !ValidStepType(s.ConditionType) {
			fields[key+".conditionType"] = "must be PRICE, LINE_DISC, MULTILINE_DISC, TOTAL_DISC, SURCHARGE, FREIGHT, REBATE_ACCRUAL, ROUNDING or FREE_GOODS"
		}
		if s.ConditionType == TypePrice {
			prices++
		} else if first, ok := typeAt[s.ConditionType]; ok && ValidStepType(s.ConditionType) {
			fields[key+".conditionType"] = fmt.Sprintf("%s is already step %d; a condition type appears once", s.ConditionType, first)
		} else {
			typeAt[s.ConditionType] = s.StepNo
		}
		if len(s.RequirementCEL) > MaxRequirementLength {
			fields[key+".requirementCel"] = fmt.Sprintf("must be at most %d characters", MaxRequirementLength)
		}
		if len(s.AccountKey) > maxAccountKeyLength {
			fields[key+".accountKey"] = fmt.Sprintf("must be at most %d characters", maxAccountKeyLength)
		}
		switch {
		case s.ExclusionGroup == "" && s.ExclusionMode != "":
			fields[key+".exclusionMode"] = "is only allowed with an exclusion group"
		case s.ExclusionGroup == "":
		case len(s.ExclusionGroup) > maxGroupLength:
			fields[key+".exclusionGroup"] = fmt.Sprintf("must be at most %d characters", maxGroupLength)
		case s.ConditionType == TypePrice:
			fields[key+".exclusionGroup"] = "a PRICE step cannot be in an exclusion group"
		case s.ExclusionMode != ExclusionBest && s.ExclusionMode != ExclusionFirst:
			fields[key+".exclusionMode"] = "must be BEST or FIRST"
		case modes[s.ExclusionGroup] != "" && modes[s.ExclusionGroup] != s.ExclusionMode:
			fields[key+".exclusionMode"] = fmt.Sprintf("must match the other steps of group %s (%s)", s.ExclusionGroup, modes[s.ExclusionGroup])
		default:
			modes[s.ExclusionGroup] = s.ExclusionMode
		}
	}
	if prices != 1 && fields["steps"] == "" {
		fields["steps"] = "must contain exactly one PRICE step"
	}
	return fields
}

// ProcedureImmutable is the PRC-0008 rule error for a change to a non-DRAFT version.
func ProcedureImmutable(p Procedure) error {
	return &RuleError{Code: CodeProcedureImmutable, Status: 409,
		Message: fmt.Sprintf("procedure %s version %d is %s and immutable; create a new version", p.Name, p.Version, p.Status)}
}
