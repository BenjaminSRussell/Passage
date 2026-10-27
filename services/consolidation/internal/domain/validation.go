package domain

import (
	"fmt"

	"github.com/shopspring/decimal"
)

// Severity of a validation rule (FR-40.20). Error blocks, Warning does not.
type Severity string

const (
	SeverityError   Severity = "Error"
	SeverityWarning Severity = "Warning"
)

// Built-in validation rules (the three seeded rules of consol.validationRules).
const (
	RuleBalanceSheet     = "BS_BALANCES"
	RuleRetainedEarnings = "RE_ROLL"
	RuleICPartner        = "IC_PARTNER_PRESENT"
)

// Task statuses of the consolidation monitor (FR-40.20).
type TaskStatus string

const (
	TaskNotStarted            TaskStatus = "NotStarted"
	TaskRunning               TaskStatus = "Running"
	TaskCompleted             TaskStatus = "Completed"
	TaskCompletedWithWarnings TaskStatus = "CompletedWithWarnings"
	TaskError                 TaskStatus = "Error"
	TaskBlocked               TaskStatus = "Blocked"
)

// ValidationRule is one rule with its severity and tolerance.
type ValidationRule struct {
	ID        string
	Severity  Severity
	Tolerance decimal.Decimal
}

// DefaultValidationRules returns the three seeded rules: the balance sheet balances
// to the cent, the retained earnings roll is within 1.00, and IC FS items carry a
// partner.
func DefaultValidationRules() []ValidationRule {
	return []ValidationRule{
		{ID: RuleBalanceSheet, Severity: SeverityError, Tolerance: decimal.New(1, -2)},
		{ID: RuleRetainedEarnings, Severity: SeverityError, Tolerance: decimal.NewFromInt(1)},
		{ID: RuleICPartner, Severity: SeverityWarning},
	}
}

// UnitFS holds the FS totals of one unit for one period, in the unit's currency.
type UnitFS struct {
	TotalAssets           decimal.Decimal
	TotalLiabilities      decimal.Decimal
	TotalEquity           decimal.Decimal
	OpeningRetained       decimal.Decimal
	NetIncome             decimal.Decimal
	Dividends             decimal.Decimal
	ClosingRetained       decimal.Decimal
	ICItemsWithoutPartner int
}

// RuleResult is the outcome of one rule for one unit.
type RuleResult struct {
	RuleID     string
	Severity   Severity
	Difference decimal.Decimal
	Failed     bool
}

// EvaluateRules checks each rule against the unit's FS totals. The differences are:
// balance sheet = assets − (liabilities + equity); retained earnings = closing −
// (opening + net income − dividends); IC partner = count of IC items with no partner.
// A rule fails when the absolute difference exceeds its tolerance.
func EvaluateRules(rules []ValidationRule, fs UnitFS) ([]RuleResult, error) {
	out := make([]RuleResult, 0, len(rules))
	for _, r := range rules {
		if r.Severity != SeverityError && r.Severity != SeverityWarning {
			return nil, invalid(fmt.Sprintf("rule %s severity must be Error or Warning", r.ID))
		}
		if r.Tolerance.IsNegative() {
			return nil, invalid(fmt.Sprintf("rule %s tolerance must not be negative", r.ID))
		}
		var diff decimal.Decimal
		switch r.ID {
		case RuleBalanceSheet:
			diff = fs.TotalAssets.Sub(fs.TotalLiabilities.Add(fs.TotalEquity))
		case RuleRetainedEarnings:
			diff = fs.ClosingRetained.Sub(fs.OpeningRetained.Add(fs.NetIncome).Sub(fs.Dividends))
		case RuleICPartner:
			diff = decimal.NewFromInt(int64(fs.ICItemsWithoutPartner))
			out = append(out, RuleResult{RuleID: r.ID, Severity: r.Severity, Difference: diff, Failed: diff.IsPositive()})
			continue
		default:
			return nil, invalid(fmt.Sprintf("unknown validation rule %s", r.ID))
		}
		out = append(out, RuleResult{RuleID: r.ID, Severity: r.Severity, Difference: diff, Failed: diff.Abs().GreaterThan(r.Tolerance)})
	}
	return out, nil
}

// UnitTaskStatus is the monitor status of a unit validate task: Error if any Error
// rule failed, CompletedWithWarnings if only Warning rules failed, else Completed.
func UnitTaskStatus(results []RuleResult) TaskStatus {
	status := TaskCompleted
	for _, r := range results {
		if !r.Failed {
			continue
		}
		if r.Severity == SeverityError {
			return TaskError
		}
		status = TaskCompletedWithWarnings
	}
	return status
}

// GroupTranslationBlocked reports whether the group translation task is Blocked
// because a unit task it depends on ended in Error or is itself Blocked, so a
// block propagates down the task chain (AC-40.15).
func GroupTranslationBlocked(units []TaskStatus) bool {
	for _, s := range units {
		if s == TaskError || s == TaskBlocked {
			return true
		}
	}
	return false
}

// Failure returns the CNS-0906 error for a failed rule of a unit, or nil when
// the rule passed.
func (r RuleResult) Failure(unit string) *Error {
	if !r.Failed {
		return nil
	}
	return &Error{Code: CodeValidationFailed, Detail: fmt.Sprintf("validation %s failed for %s: %s", r.RuleID, unit, r.Difference.String())}
}
