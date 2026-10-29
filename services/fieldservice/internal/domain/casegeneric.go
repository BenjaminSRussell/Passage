package domain

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Case category types (FR-45.12).
const (
	CategoryService         = "SERVICE"
	CategoryCustomerService = "CUSTOMER_SERVICE"
	CategoryHR              = "HR"
	CategoryCollections     = "COLLECTIONS"
	CategoryProductChange   = "PRODUCT_CHANGE"
	CategoryAudit           = "AUDIT"
	CategoryProject         = "PROJECT"
	CategoryVendor          = "VENDOR"
	CategoryInternal        = "INTERNAL"
)

// Generic case statuses. CLOSED and CANCELED are final.
const (
	GenericOpen     = "OPEN"
	GenericResolved = "RESOLVED"
	GenericClosed   = "CLOSED"
	GenericCanceled = "CANCELED"
)

// Codes returned by the generic case rules. Each is listed in the service README.
const (
	CodeStageGate          = "CASE-STG-001"
	CodeStageInvalid       = "CASE-STG-002"
	CodeResolutionRequired = "CASE-RES-001"
	CodeResolutionUnknown  = "CASE-RES-002"
	CodeCaseState          = "CASE-ST-001"
	CodeCategoryRouted     = "CASE-CAT-002"
	CodeCustomerRequired   = "CASE-CUS-001"
	CodeCaseSecurity       = "CASE-SEC-001"
)

// CategoryTypes lists every category type a case category may carry.
var CategoryTypes = []string{CategoryService, CategoryCustomerService, CategoryHR, CategoryCollections,
	CategoryProductChange, CategoryAudit, CategoryProject, CategoryVendor, CategoryInternal}

// ValidCategoryType reports whether t is a known category type.
func ValidCategoryType(t string) bool { return slices.Contains(CategoryTypes, t) }

// ServiceRouted reports whether a category keeps the entitlement and SLA behaviour of
// FR-45.5. Those cases open through the service endpoints, not the generic ones.
func ServiceRouted(categoryType string) bool {
	return categoryType == CategoryService || categoryType == CategoryCustomerService
}

// RequiresCustomer reports whether cases in the category must name a customer party.
func RequiresCustomer(categoryType string) bool {
	return ServiceRouted(categoryType) || categoryType == CategoryCollections
}

// Stage is one ordered step of a case process (BR-45.9). RequiredActivities are the
// activity templates of the stage, matched by activity subject: each must be DONE on
// the case before the case moves on to the next stage.
type Stage struct {
	Code               string   `json:"code"`
	Sequence           int      `json:"sequence"`
	RequiredActivities []string `json:"requiredActivities"`
}

// ProcessProblems lists what is wrong with a process definition. An empty result
// means the stages are usable: codes unique, sequences 1..n in order, at most 20 stages.
func ProcessProblems(stages []Stage) []string {
	var problems []string
	if len(stages) < 1 || len(stages) > 20 {
		return append(problems, "must hold 1 to 20 stages")
	}
	seen := map[string]bool{}
	for i, s := range stages {
		if s.Sequence != i+1 {
			problems = append(problems, fmt.Sprintf("stage %s has sequence %d, expected %d", s.Code, s.Sequence, i+1))
		}
		if !codePattern(s.Code) {
			problems = append(problems, fmt.Sprintf("stage %d code must be 2 to 40 characters of A-Z, 0-9, _ or -", i+1))
		}
		if seen[s.Code] {
			problems = append(problems, "stage codes must be unique: "+s.Code)
		}
		seen[s.Code] = true
		for _, a := range s.RequiredActivities {
			if n := len([]rune(strings.TrimSpace(a))); n < 1 || n > 200 {
				problems = append(problems, "required activity subjects must be 1 to 200 characters")
			}
		}
	}
	return problems
}

func codePattern(code string) bool {
	if n := len(code); n < 2 || n > 40 {
		return false
	}
	for _, r := range code {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// ValidCode reports whether a category or process code is well formed.
func ValidCode(code string) bool { return codePattern(code) }

// CheckStage applies BR-45.9 to a move from one stage to another. done holds the
// subjects of DONE activities on the case. Only the adjacent next stage may be
// entered when the current stage's required activities are DONE; moving back one
// stage needs a reason.
func CheckStage(stages []Stage, from, to string, done map[string]bool, reason string) error {
	fromIdx, toIdx := -1, -1
	for i, s := range stages {
		if s.Code == from {
			fromIdx = i
		}
		if s.Code == to {
			toIdx = i
		}
	}
	if fromIdx < 0 || toIdx < 0 {
		return &Error{Code: CodeStageInvalid, Detail: "the target stage is not part of this case's process"}
	}
	switch toIdx - fromIdx {
	case 1:
		var missing []string
		for _, req := range stages[fromIdx].RequiredActivities {
			if !done[req] {
				missing = append(missing, req)
			}
		}
		if len(missing) > 0 {
			return &Error{Code: CodeStageGate, Detail: fmt.Sprintf("stage %s exit criteria not met: required activities not DONE: %s",
				from, strings.Join(missing, ", "))}
		}
		return nil
	case -1:
		if strings.TrimSpace(reason) == "" {
			return &Error{Code: CodeStageInvalid, Detail: "moving back a stage needs a reason"}
		}
		return nil
	case 0:
		return &Error{Code: CodeStageInvalid, Detail: "the case is already in that stage"}
	default:
		return &Error{Code: CodeStageInvalid, Detail: "only the adjacent stage can be entered"}
	}
}

// CheckTransition validates a status change of a generic case. Resolving needs a
// resolution code, and when the category lists codes the code must be one of them.
func CheckTransition(status, to, resolutionCode string, categoryCodes []string) error {
	switch {
	case status == GenericOpen && (to == GenericResolved || to == GenericCanceled):
	case status == GenericResolved && to == GenericClosed:
	default:
		return &Error{Code: CodeCaseState, Detail: fmt.Sprintf("a case in status %s cannot move to %s", status, to)}
	}
	if to != GenericResolved {
		return nil
	}
	if resolutionCode == "" {
		return &Error{Code: CodeResolutionRequired, Detail: "a resolution code is required to resolve a case"}
	}
	if len(categoryCodes) > 0 && !slices.Contains(categoryCodes, resolutionCode) {
		return &Error{Code: CodeResolutionUnknown, Detail: "the resolution code is not one of this category's codes"}
	}
	return nil
}

// ReopenInPlace applies the BR-45.9 reopen rule. A RESOLVED or CLOSED case resolved
// no more than windowDays ago reopens itself (true); an older one, or one in a
// category whose window is 0, is reopened as a new case linked by reopened_from
// (false). Only RESOLVED and CLOSED cases can be reopened.
func ReopenInPlace(status string, resolvedAt *time.Time, now time.Time, windowDays int) (bool, error) {
	if status != GenericResolved && status != GenericClosed {
		return false, &Error{Code: CodeCaseState, Detail: fmt.Sprintf("a case in status %s cannot be reopened", status)}
	}
	if resolvedAt == nil || windowDays == 0 {
		return false, nil
	}
	return !now.After(resolvedAt.AddDate(0, 0, windowDays)), nil
}

// CanAccess applies BR-45.10: a case is readable and writable by its owner and by
// holders of the category's access duty. A category with no access duty is open to
// anyone holding the generic maintain duty.
func CanAccess(accessDuty string, holdsAccessDuty, isOwner bool) bool {
	if accessDuty == "" {
		return true
	}
	return isOwner || holdsAccessDuty
}
