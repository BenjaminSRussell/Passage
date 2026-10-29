package domain

import (
	"fmt"
	"sort"
	"time"

	"github.com/shopspring/decimal"
)

// Audit case statuses (FR-48.15).
type CaseStatus string

const (
	CaseOpen            CaseStatus = "OPEN"
	CaseInReview        CaseStatus = "IN_REVIEW"
	CaseResolvedNoIssue CaseStatus = "RESOLVED_NO_ISSUE"
	CaseResolvedIssue   CaseStatus = "RESOLVED_ISSUE"
	CaseClosed          CaseStatus = "CLOSED"
	CaseArchived        CaseStatus = "ARCHIVED"
)

// Audit case actions. Each action moves the case along one edge of the status machine.
type CaseAction string

const (
	ActionStartReview    CaseAction = "START_REVIEW"
	ActionResolveNoIssue CaseAction = "RESOLVE_NO_ISSUE"
	ActionResolveIssue   CaseAction = "RESOLVE_ISSUE"
	ActionClose          CaseAction = "CLOSE"
	ActionArchive        CaseAction = "ARCHIVE"
)

// Audit case codes.
const (
	CodeCaseTransition     = "AUD-0713" // action not allowed from the current status
	CodeCaseResolution     = "AUD-0714" // resolution code missing or not valid for the action
	CodeCaseArchiveTooSoon = "AUD-0715" // archive before the retention window has passed
)

// Case categories derived from the rule that produced a violation.
const (
	CategoryDuplicatePayment = "DUPLICATE_PAYMENT"
	CategoryPolicyBreach     = "POLICY_BREACH"
	CategorySampledReview    = "SAMPLED_REVIEW"
)

// Default and bounds for audit.case_archive_days (parameter table, 9.48.13).
const (
	DefaultCaseArchiveDays = 365
	MinCaseArchiveDays     = 30
	MaxCaseArchiveDays     = 3650
)

var (
	noIssueCodes = map[string]bool{"NO_ISSUE": true, "FALSE_POSITIVE": true, "SAMPLE_CLEARED": true}
	issueCodes   = map[string]bool{"DUPLICATE_RECOVERED": true, "POLICY_CORRECTED": true, "REFERRED_FRAUD": true}
	transitions  = map[CaseStatus]map[CaseAction]CaseStatus{
		CaseOpen:            {ActionStartReview: CaseInReview},
		CaseInReview:        {ActionResolveNoIssue: CaseResolvedNoIssue, ActionResolveIssue: CaseResolvedIssue},
		CaseResolvedNoIssue: {ActionClose: CaseClosed},
		CaseResolvedIssue:   {ActionClose: CaseClosed},
		CaseClosed:          {ActionArchive: CaseArchived},
	}
)

// CaseState is the part of an audit_case the status machine reads and writes.
type CaseState struct {
	Status         CaseStatus
	ResolutionCode string
	RecoveryAmount decimal.Decimal
	Currency       string
	ClosedAt       *time.Time
}

// CaseTransition is one requested action with its inputs.
type CaseTransition struct {
	Action         CaseAction
	ResolutionCode string
	RecoveryAmount decimal.Decimal
	Currency       string
	Now            time.Time
	ArchiveDays    int
}

// ApplyCaseAction validates the action against the status machine and returns the new state.
// RESOLVE_ISSUE records the recovery amount; CLOSE requires a resolution code (FR-48.15);
// ARCHIVE is allowed only after archiveDays since closing.
func ApplyCaseAction(s CaseState, t CaseTransition) (CaseState, error) {
	next, ok := transitions[s.Status][t.Action]
	if !ok {
		return s, &Error{Code: CodeCaseTransition, Detail: fmt.Sprintf("action %s is not allowed from status %s", t.Action, s.Status)}
	}
	out := s
	switch t.Action {
	case ActionResolveNoIssue:
		if !noIssueCodes[t.ResolutionCode] {
			return s, &Error{Code: CodeCaseResolution, Detail: fmt.Sprintf("resolution %q does not close without issue", t.ResolutionCode)}
		}
		out.ResolutionCode = t.ResolutionCode
	case ActionResolveIssue:
		if !issueCodes[t.ResolutionCode] {
			return s, &Error{Code: CodeCaseResolution, Detail: fmt.Sprintf("resolution %q does not record an issue", t.ResolutionCode)}
		}
		if t.RecoveryAmount.IsNegative() || (t.RecoveryAmount.IsPositive() && t.Currency == "") {
			return s, &Error{Code: CodeCaseResolution, Detail: "recovery amount needs a non-negative value and a currency"}
		}
		out.ResolutionCode = t.ResolutionCode
		out.RecoveryAmount = t.RecoveryAmount
		out.Currency = t.Currency
	case ActionClose:
		if out.ResolutionCode == "" {
			return s, &Error{Code: CodeCaseResolution, Detail: "closing requires a resolution code"}
		}
		closed := t.Now.UTC()
		out.ClosedAt = &closed
	case ActionArchive:
		days := t.ArchiveDays
		if days == 0 {
			days = DefaultCaseArchiveDays
		}
		if days < MinCaseArchiveDays || days > MaxCaseArchiveDays {
			return s, &Error{Code: CodeCaseArchiveTooSoon, Detail: fmt.Sprintf("archive window must be %d..%d days", MinCaseArchiveDays, MaxCaseArchiveDays)}
		}
		if s.ClosedAt == nil || t.Now.UTC().Before(s.ClosedAt.AddDate(0, 0, days)) {
			return s, &Error{Code: CodeCaseArchiveTooSoon, Detail: fmt.Sprintf("case stays in the active tier for %d days after closing", days)}
		}
	}
	out.Status = next
	return out, nil
}

// Violation is a stored audit_violation as the case grouping reads it.
type Violation struct {
	ID         string `json:"id"`
	PolicyID   string `json:"policyId"`
	RuleType   string `json:"ruleType"`
	DocumentID string `json:"documentId"`
	PartyID    string `json:"partyId,omitempty"`
	CaseID     string `json:"caseId,omitempty"`
}

// CaseDraft is one audit case to create from a group of violations.
type CaseDraft struct {
	Key          string   `json:"key"`
	Category     string   `json:"category"`
	ViolationIDs []string `json:"violationIds"`
	DocumentIDs  []string `json:"documentIds"`
}

// CategoryFor maps a rule type to its case category.
func CategoryFor(ruleType string) (string, error) {
	switch ruleType {
	case AuditRuleAggregate, AuditRuleKeyword, AuditRuleListSearch, AuditRuleThreshold, AuditRuleWeekendHoliday:
		return CategoryPolicyBreach, nil
	case AuditRuleSampling:
		return CategorySampledReview, nil
	case AuditRuleDuplicate:
		return CategoryDuplicatePayment, nil
	}
	return "", &Error{Code: CodeAuditRuleType, Detail: fmt.Sprintf("audit rule type %q has no case category", ruleType)}
}

// GroupViolations groups unassigned violations into case drafts. Duplicates group per
// vendor (one case per vendor, AC-48.13); other rules group per policy. A violation that
// already belongs to a case is skipped, so it belongs to at most one case. Drafts are
// sorted by key.
func GroupViolations(vs []Violation) ([]CaseDraft, error) {
	byKey := map[string]*CaseDraft{}
	for _, v := range vs {
		if v.CaseID != "" {
			continue
		}
		cat, err := CategoryFor(v.RuleType)
		if err != nil {
			return nil, err
		}
		key := cat + ":" + v.PolicyID
		if cat == CategoryDuplicatePayment {
			key = cat + ":" + v.PartyID
		}
		d, ok := byKey[key]
		if !ok {
			d = &CaseDraft{Key: key, Category: cat}
			byKey[key] = d
		}
		d.ViolationIDs = append(d.ViolationIDs, v.ID)
		d.DocumentIDs = append(d.DocumentIDs, v.DocumentID)
	}
	out := make([]CaseDraft, 0, len(byKey))
	for _, d := range byKey {
		sort.Strings(d.ViolationIDs)
		sort.Strings(d.DocumentIDs)
		out = append(out, *d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}
