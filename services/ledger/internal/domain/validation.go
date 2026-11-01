package domain

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// User-defined validation rules (FR-11.21, AC-11.14). A rule has a prerequisite
// and a check, each a list of conditions that must all hold. A rule fails on a
// line (level `line`) or on the entry (level `header`) when its prerequisite
// holds and its check does not. The PRD writes the two expressions in CEL; CEL is
// not in the allowed dependency list, so conditions are structured, the same shape
// as a derivation step's condition with the comparison operators added.

// Rule levels and severities.
const (
	RuleLevelLine   = "line"
	RuleLevelHeader = "header"
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Condition operators beyond those of derivation steps (CondEq, CondNe, CondIn, CondBlank, CondNotBlank).
const (
	CondGt      = "gt"
	CondGte     = "gte"
	CondLt      = "lt"
	CondLte     = "lte"
	CondBetween = "between"
)

// Fields a validation condition may read. Line rules read every field; header rules
// read the header fields only. Dimension attributes of the ledger are line fields.
const (
	FieldAmount       = "AMOUNT"
	FieldSide         = "SIDE"
	FieldText         = "TEXT"
	FieldSourceSystem = "SOURCE_SYSTEM"
	FieldSourceType   = "SOURCE_TYPE"
	FieldPostingDate  = "POSTING_DATE"
	FieldDocumentDate = "DOCUMENT_DATE"
	FieldTotalDebit   = "TOTAL_DEBIT"
	FieldLineCount    = "LINE_COUNT"
)

// Limits and the problem code of FR-11.21.
const (
	MaxValidationRules        = 200
	MaxRuleConditions         = 10
	MaxRuleMessage            = 200
	MaxRuleSequence           = 9999
	CodeValidationRuleFail    = "LEDGER_VALIDATION_RULE_FAILED"
	CodeValidationRuleInvalid = "LEDGER_VALIDATION_RULE_INVALID"
)

// RuleCondition compares one field with the condition's values.
type RuleCondition struct {
	Field  string   `json:"field"`
	Op     string   `json:"op"`
	Values []string `json:"values,omitempty"`
}

// ValidationRule is one rule of a ledger. LockVersion is the entity tag.
type ValidationRule struct {
	ID           string          `json:"id"`
	LedgerID     string          `json:"ledgerId"`
	Sequence     int             `json:"sequence"`
	Name         string          `json:"name"`
	Level        string          `json:"level"`
	Severity     string          `json:"severity"`
	Prerequisite []RuleCondition `json:"prerequisite"`
	Checks       []RuleCondition `json:"checks"`
	Message      string          `json:"message"`
	Active       bool            `json:"active"`
	LockVersion  int             `json:"lockVersion"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

var headerFields = map[string]bool{FieldSourceSystem: true, FieldSourceType: true, FieldPostingDate: true,
	FieldDocumentDate: true, FieldTotalDebit: true, FieldLineCount: true}

var lineOnlyFields = map[string]bool{FieldAccount: true, FieldAccountType: true, FieldAmount: true, FieldSide: true, FieldText: true}

var numericFields = map[string]bool{FieldAmount: true, FieldTotalDebit: true, FieldLineCount: true}

var dateFields = map[string]bool{FieldPostingDate: true, FieldDocumentDate: true}

// ValidateRule checks a rule definition against the ledger's dimension attributes.
// The result maps a field name to the problem.
func ValidateRule(r ValidationRule, attributes []string) map[string]string {
	attrs := attrSet(attributes)
	out := map[string]string{}
	if n := len([]rune(r.Name)); n < 1 || n > 80 {
		out["name"] = "must be 1 to 80 characters"
	}
	if r.Level != RuleLevelLine && r.Level != RuleLevelHeader {
		out["level"] = "must be line or header"
	}
	if r.Severity != SeverityError && r.Severity != SeverityWarning {
		out["severity"] = "must be error or warning"
	}
	if r.Sequence < 1 || r.Sequence > MaxRuleSequence {
		out["sequence"] = fmt.Sprintf("must be 1 to %d", MaxRuleSequence)
	}
	if n := len([]rune(strings.TrimSpace(r.Message))); n < 1 || n > MaxRuleMessage {
		out["message"] = fmt.Sprintf("must be 1 to %d characters", MaxRuleMessage)
	}
	if n := len(r.Checks); n < 1 || n > MaxRuleConditions {
		out["checks"] = fmt.Sprintf("needs 1 to %d conditions", MaxRuleConditions)
	}
	if len(r.Prerequisite) > MaxRuleConditions {
		out["prerequisite"] = fmt.Sprintf("has at most %d conditions", MaxRuleConditions)
	}
	check := func(name string, cs []RuleCondition) {
		for i, c := range cs {
			if msg := validateCondition(c, r.Level, attrs); msg != "" {
				out[fmt.Sprintf("%s[%d]", name, i)] = msg
			}
		}
	}
	check("prerequisite", r.Prerequisite)
	check("checks", r.Checks)
	return out
}

func validateCondition(c RuleCondition, level string, attrs map[string]bool) string {
	switch {
	case headerFields[c.Field]:
	case lineOnlyFields[c.Field] || attrs[c.Field]:
		if level == RuleLevelHeader {
			return fmt.Sprintf("field %s is a line field; a header rule reads header fields only", c.Field)
		}
	default:
		return fmt.Sprintf("field %q is not a header field, line field or dimension attribute of the ledger", c.Field)
	}
	n := len(c.Values)
	switch c.Op {
	case CondEq, CondNe, CondGt, CondGte, CondLt, CondLte:
		if n != 1 {
			return fmt.Sprintf("%s needs exactly one value", c.Op)
		}
	case CondIn:
		if n < 1 || n > MaxConditionValues {
			return fmt.Sprintf("in needs 1 to %d values", MaxConditionValues)
		}
	case CondBetween:
		if n != 2 {
			return "between needs two values, the lower and the upper bound"
		}
	case CondBlank, CondNotBlank:
		if n != 0 {
			return fmt.Sprintf("%s takes no values", c.Op)
		}
	default:
		return fmt.Sprintf("operator %q must be eq, ne, in, gt, gte, lt, lte, between, blank or notblank", c.Op)
	}
	if numericFields[c.Field] {
		for _, v := range c.Values {
			if _, err := decimal.NewFromString(v); err != nil {
				return fmt.Sprintf("%s compares numbers; %q is not a number", c.Field, v)
			}
		}
	}
	if dateFields[c.Field] {
		for _, v := range c.Values {
			if _, err := time.Parse(time.DateOnly, v); err != nil {
				return fmt.Sprintf("%s compares dates; %q is not a YYYY-MM-DD date", c.Field, v)
			}
		}
	}
	return ""
}

// compare orders a and b: numerically when both are numbers (amounts, account
// numbers), otherwise as text (ISO dates sort correctly as text).
func compare(a, b string) int {
	da, errA := decimal.NewFromString(a)
	db, errB := decimal.NewFromString(b)
	if errA == nil && errB == nil {
		return da.Cmp(db)
	}
	return strings.Compare(a, b)
}

// RuleConditionHolds evaluates one condition on the field's value. A blank value
// satisfies `blank` and `ne`; the comparison operators never match it. On the
// numeric fields eq, ne and in compare numbers, so 1500.00 equals 1500.
func RuleConditionHolds(c RuleCondition, value string) bool {
	if numericFields[c.Field] && value != "" {
		switch c.Op {
		case CondEq:
			return len(c.Values) == 1 && compare(value, c.Values[0]) == 0
		case CondNe:
			return len(c.Values) == 1 && compare(value, c.Values[0]) != 0
		case CondIn:
			return slices.ContainsFunc(c.Values, func(v string) bool { return compare(value, v) == 0 })
		}
	}
	switch c.Op {
	case CondEq, CondNe, CondIn, CondBlank, CondNotBlank:
		return conditionHolds(&StepCondition{Field: c.Field, Op: c.Op, Values: c.Values}, value)
	}
	if value == "" {
		return false
	}
	switch c.Op {
	case CondGt:
		return len(c.Values) == 1 && compare(value, c.Values[0]) > 0
	case CondGte:
		return len(c.Values) == 1 && compare(value, c.Values[0]) >= 0
	case CondLt:
		return len(c.Values) == 1 && compare(value, c.Values[0]) < 0
	case CondLte:
		return len(c.Values) == 1 && compare(value, c.Values[0]) <= 0
	case CondBetween:
		return len(c.Values) == 2 && compare(value, c.Values[0]) >= 0 && compare(value, c.Values[1]) <= 0
	}
	return false
}

// RuleHeader is the entry-level data validation rules read.
type RuleHeader struct {
	SourceSystem string
	SourceType   string
	PostingDate  time.Time
	DocumentDate *time.Time
}

// RuleOutcome is what the active rules say about an entry: errors refuse it,
// warnings are returned to the caller.
type RuleOutcome struct {
	Errors   []Violation
	Warnings []Violation
}

// EvaluateRules runs the active rules in sequence order against a derived entry
// (AC-11.14). Line rules run for every line, header rules once. Every failure is
// reported; nothing short-circuits.
func EvaluateRules(rules []ValidationRule, h RuleHeader, accounts map[string]Account, lines []LineInput) RuleOutcome {
	var out RuleOutcome
	total := decimal.Zero
	for _, l := range lines {
		if l.Side != SideCredit {
			total = total.Add(l.Amount)
		}
	}
	headerValue := func(field string) string {
		switch field {
		case FieldSourceSystem:
			return h.SourceSystem
		case FieldSourceType:
			return h.SourceType
		case FieldPostingDate:
			return h.PostingDate.Format(time.DateOnly)
		case FieldDocumentDate:
			if h.DocumentDate != nil {
				return h.DocumentDate.Format(time.DateOnly)
			}
		case FieldTotalDebit:
			return total.String()
		case FieldLineCount:
			return strconv.Itoa(len(lines))
		}
		return ""
	}
	holdsAll := func(cs []RuleCondition, read func(string) string) bool {
		for _, c := range cs {
			if !RuleConditionHolds(c, read(c.Field)) {
				return false
			}
		}
		return true
	}
	report := func(r ValidationRule, v Violation) {
		if r.Severity == SeverityWarning {
			out.Warnings = append(out.Warnings, v)
		} else {
			out.Errors = append(out.Errors, v)
		}
	}
	for _, r := range rules {
		if !r.Active {
			continue
		}
		if r.Level == RuleLevelHeader {
			if holdsAll(r.Prerequisite, headerValue) && !holdsAll(r.Checks, headerValue) {
				report(r, Violation{Code: CodeValidationRuleFail, Field: "lines",
					Message: fmt.Sprintf("%s (rule %s)", r.Message, r.ID)})
			}
			continue
		}
		for i, l := range lines {
			acct := accounts[l.Account]
			read := func(field string) string {
				switch field {
				case FieldAccount:
					return l.Account
				case FieldAccountType:
					return acct.Type
				case FieldAmount:
					return l.Amount.String()
				case FieldSide:
					return l.Side
				case FieldText:
					return l.Text
				}
				if headerFields[field] {
					return headerValue(field)
				}
				return l.Dimensions[field]
			}
			if holdsAll(r.Prerequisite, read) && !holdsAll(r.Checks, read) {
				idx := i
				report(r, Violation{Code: CodeValidationRuleFail, Field: "lines", LineIndex: &idx,
					Message: fmt.Sprintf("%s (rule %s, line %d)", r.Message, r.ID, i+1)})
			}
		}
	}
	return out
}
